// Package ui serves the karpx web dashboard.
// It embeds the static HTML at compile time so the binary has no external
// file dependencies.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kemilad/karpx/internal/compat"
	"github.com/kemilad/karpx/internal/helm"
	"github.com/kemilad/karpx/internal/kube"
)

//go:embed static/index.html
var staticFiles embed.FS

// ClusterStatus is the JSON payload returned by /api/clusters.
type ClusterStatus struct {
	Context           string `json:"context"`
	Provider          string `json:"provider"`
	K8sVersion        string `json:"k8s_version"`
	KarpenterInstalled bool  `json:"karpenter_installed"`
	KarpenterVersion  string `json:"karpenter_version,omitempty"`
	Compatible        *bool  `json:"compatible,omitempty"`
	UpgradeAvailable  bool   `json:"upgrade_available"`
	LatestCompatible  string `json:"latest_compatible,omitempty"`
	Error             string `json:"error,omitempty"`
}

// Serve starts the dashboard HTTP server on the given port.
// If port is 0 a free port is chosen automatically.
// kubeCtx restricts the dashboard to a single context; pass "" to show all.
func Serve(port int, kubeCtx string) error {
	// Resolve the address.
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// If port == 0 pick a free one.
	if port == 0 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("could not bind to a free port: %w", err)
		}
		addr = ln.Addr().String()
		ln.Close()
	}

	mux := http.NewServeMux()

	// ── Static assets ──────────────────────────────────────────────────────
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// ── API endpoint ───────────────────────────────────────────────────────
	mux.HandleFunc("/api/clusters", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		var contexts []string
		if kubeCtx != "" {
			contexts = []string{kubeCtx}
		} else {
			contexts = allContexts()
		}

		results := checkClusters(contexts)
		json.NewEncoder(w).Encode(results)
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	url := "http://" + addr
	fmt.Printf("\n  ⚡ karpx dashboard\n\n")
	fmt.Printf("  URL     : %s\n", url)
	fmt.Printf("  Refresh : every 30 s (or click Refresh in the browser)\n")
	fmt.Printf("  Stop    : Ctrl+C\n\n")

	// Open browser in the background.
	go openBrowser(url)

	// Graceful shutdown on SIGINT / SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-stop
		fmt.Printf("\n  Shutting down dashboard…\n\n")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Cluster inspection helpers
// ─────────────────────────────────────────────────────────────────────────────

// allContexts returns every context name from the active kubeconfig.
func allContexts() []string {
	out, err := exec.Command("kubectl", "config", "get-contexts",
		"-o", "name").Output()
	if err != nil {
		return nil
	}
	var ctxs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ctxs = append(ctxs, line)
		}
	}
	return ctxs
}

// checkClusters inspects each context concurrently.
func checkClusters(contexts []string) []ClusterStatus {
	results := make([]ClusterStatus, len(contexts))
	var wg sync.WaitGroup

	// Limit parallelism to avoid hammering kubeconfig / network.
	sem := make(chan struct{}, 8)

	for i, ctx := range contexts {
		wg.Add(1)
		go func(i int, ctx string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = inspectContext(ctx)
		}(i, ctx)
	}
	wg.Wait()
	return results
}

// inspectContext gathers all status fields for one kubeconfig context.
func inspectContext(ctx string) ClusterStatus {
	s := ClusterStatus{Context: ctx}

	// Provider.
	provider := kube.DetectProvider(ctx)
	s.Provider = string(provider)

	// Kubernetes version (with a short timeout).
	k8sVer, err := withTimeout(5*time.Second, func() (string, error) {
		return kube.GetServerVersion(ctx)
	})
	if err != nil {
		s.Error = fmt.Sprintf("cluster unreachable: %v", err)
		return s
	}
	s.K8sVersion = k8sVer

	// Karpenter via helm.
	info, err := helm.DetectKarpenter(ctx)
	if err != nil {
		s.Error = fmt.Sprintf("helm error: %v", err)
		return s
	}
	s.KarpenterInstalled = info.Installed
	if info.Installed {
		s.KarpenterVersion = strings.TrimPrefix(info.Version, "v")
	}

	// Compatibility + upgrade check (AWS only for now).
	if info.Installed && provider == kube.ProviderAWS {
		installed := strings.TrimPrefix(info.Version, "v")
		ok := compat.IsCompatible(installed, k8sVer)
		s.Compatible = &ok

		latest, _, _ := compat.LatestCompatible(k8sVer)
		if latest != "" && latest != installed {
			s.UpgradeAvailable = true
			s.LatestCompatible = latest
		}
	}

	return s
}

// withTimeout runs fn in a goroutine and returns its result or an error if
// the deadline is exceeded.
func withTimeout(d time.Duration, fn func() (string, error)) (string, error) {
	type res struct {
		v   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		v, err := fn()
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(d):
		return "", fmt.Errorf("timeout after %s", d)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Browser launcher
// ─────────────────────────────────────────────────────────────────────────────

func openBrowser(url string) {
	time.Sleep(400 * time.Millisecond) // wait for server to start
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default: // Linux / BSD
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
