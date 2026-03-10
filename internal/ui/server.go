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
	"github.com/kemilad/karpx/internal/nodes"
)

//go:embed static/index.html
var staticFiles embed.FS

// ClusterStatus is the JSON payload returned by /api/clusters.
type ClusterStatus struct {
	Context              string `json:"context"`
	Provider             string `json:"provider"`
	DocsURL              string `json:"docs_url,omitempty"`
	K8sVersion           string `json:"k8s_version"`
	KarpenterInstalled   bool   `json:"karpenter_installed"`
	KarpenterVersion     string `json:"karpenter_version,omitempty"`
	KarpenterNamespace   string `json:"karpenter_namespace,omitempty"`
	KarpenterRelease     string `json:"karpenter_release,omitempty"`
	Compatible           *bool  `json:"compatible,omitempty"`
	UpgradeAvailable     bool   `json:"upgrade_available"`
	LatestCompatible     string `json:"latest_compatible,omitempty"`
	MinCompatible        string `json:"min_compatible,omitempty"`
	Error                string `json:"error,omitempty"`
}

// InstallRequest is the JSON body for POST /api/install.
type InstallRequest struct {
	Context           string `json:"context"`
	Version           string `json:"version"`
	Namespace         string `json:"namespace"`
	ClusterName       string `json:"cluster_name"`
	Region            string `json:"region"`
	ControllerRoleARN string `json:"controller_role_arn"`
}

// VersionsResponse is returned by GET /api/versions.
type VersionsResponse struct {
	Recommended string   `json:"recommended"`
	Compatible  []string `json:"compatible"`
	Error       string   `json:"error,omitempty"`
}

// InstallResponse is the JSON body returned by POST /api/install.
type InstallResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// UninstallRequest is the JSON body for POST /api/uninstall.
type UninstallRequest struct {
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Release   string `json:"release"`
}

// UpgradeRequest is the JSON body for POST /api/upgrade.
type UpgradeRequest struct {
	Context           string `json:"context"`
	Version           string `json:"version"`
	Namespace         string `json:"namespace"`
	Release           string `json:"release"`
	ClusterName       string `json:"cluster_name"`
	Region            string `json:"region"`
	ControllerRoleARN string `json:"controller_role_arn"`
}

// NodePoolDetail is a single NodePool with full status, returned by /api/nodepools.
type NodePoolDetail struct {
	Name        string `json:"name"`
	Mode        string `json:"mode,omitempty"`
	Ready       bool   `json:"ready"`
	NotReadyMsg string `json:"not_ready_msg,omitempty"`
	CPULim      string `json:"cpu_lim,omitempty"`
	MemLim      string `json:"mem_lim,omitempty"`
}

// NodeClassDetail is a single EC2NodeClass with status, returned by /api/nodepools.
type NodeClassDetail struct {
	Name        string `json:"name"`
	Ready       bool   `json:"ready"`
	NotReadyMsg string `json:"not_ready_msg,omitempty"`
}

// NodePoolListResponse is returned by GET /api/nodepools.
type NodePoolListResponse struct {
	NodePools   []NodePoolDetail  `json:"node_pools"`
	NodeClasses []NodeClassDetail `json:"node_classes"`
	Error       string            `json:"error,omitempty"`
}

// RecommendRequest is the JSON body for POST /api/nodes/recommend.
type RecommendRequest struct {
	Context     string `json:"context"`
	Mode        string `json:"mode"`
	ClusterName string `json:"cluster_name"`
	RoleARN     string `json:"role_arn"`
}

// RecommendResponse is returned by POST /api/nodes/recommend.
type RecommendResponse struct {
	Manifest   string   `json:"manifest"`
	Reasoning  []string `json:"reasoning"`
	Families   []string `json:"families"`
	Capacities []string `json:"capacities"`
	Archs      []string `json:"architectures"`
	Error      string   `json:"error,omitempty"`
}

// ApplyRequest is the JSON body for POST /api/nodes/apply.
type ApplyRequest struct {
	Context  string `json:"context"`
	Manifest string `json:"manifest"`
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

	// ── Instant compatibility check (embedded matrix, no network) ──────────
	mux.HandleFunc("/api/check-compat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		k8sVer := r.URL.Query().Get("k8s")
		karpVer := r.URL.Query().Get("karpenter")
		if k8sVer == "" || karpVer == "" {
			json.NewEncoder(w).Encode(map[string]any{"error": "k8s and karpenter params required"})
			return
		}
		ok := compat.IsCompatible(karpVer, k8sVer)
		json.NewEncoder(w).Encode(map[string]any{"compatible": ok, "k8s": k8sVer, "karpenter": karpVer})
	})

	// ── Compatible versions for a K8s version ──────────────────────────────
	mux.HandleFunc("/api/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		k8sVer := r.URL.Query().Get("k8s")
		if k8sVer == "" {
			json.NewEncoder(w).Encode(VersionsResponse{Error: "k8s query param required"})
			return
		}
		latest, all, err := compat.LatestCompatible(k8sVer)
		if err != nil {
			// GitHub may be rate-limited — return the min compatible as a
			// fallback so the UI can still suggest something.
			minVer := compat.MinCompatibleKarpenter(k8sVer)
			json.NewEncoder(w).Encode(VersionsResponse{
				Recommended: minVer,
				Compatible:  nil,
				Error:       err.Error(),
			})
			return
		}
		json.NewEncoder(w).Encode(VersionsResponse{Recommended: latest, Compatible: all})
	})

	mux.HandleFunc("/api/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		var req InstallRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: "invalid request body"})
			return
		}
		if req.Context == "" || req.Version == "" || req.ClusterName == "" || req.Region == "" {
			json.NewEncoder(w).Encode(InstallResponse{Error: "context, version, cluster_name, and region are required"})
			return
		}

		ns := req.Namespace
		if ns == "" {
			ns = "karpenter"
		}
		ver := strings.TrimPrefix(req.Version, "v")

		args := []string{
			"install", "karpenter",
			"oci://public.ecr.aws/karpenter/karpenter",
			"--version", ver,
			"--namespace", ns,
			"--create-namespace",
			"--kube-context", req.Context,
			"--set", "settings.clusterName=" + req.ClusterName,
			"--set", "controller.env[0].name=AWS_REGION",
			"--set", "controller.env[0].value=" + req.Region,
		}
		if req.ControllerRoleARN != "" {
			args = append(args, "--set",
				`serviceAccount.annotations.eks\.amazonaws\.com/role-arn=`+req.ControllerRoleARN)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		out, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
		if err != nil {
			json.NewEncoder(w).Encode(InstallResponse{
				Error: fmt.Sprintf("%v\n%s", err, strings.TrimSpace(string(out))),
			})
			return
		}
		json.NewEncoder(w).Encode(InstallResponse{
			Success: true,
			Output:  strings.TrimSpace(string(out)),
		})
	})

	// ── Uninstall ───────────────────────────────────────────────────────────
	mux.HandleFunc("/api/uninstall", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		var req UninstallRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: "invalid request body"})
			return
		}
		if req.Context == "" {
			json.NewEncoder(w).Encode(InstallResponse{Error: "context is required"})
			return
		}
		release := req.Release
		if release == "" {
			release = "karpenter"
		}
		ns := req.Namespace
		if ns == "" {
			ns = "karpenter"
		}
		args := []string{"uninstall", release, "--namespace", ns, "--kube-context", req.Context}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
		if err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: fmt.Sprintf("%v\n%s", err, strings.TrimSpace(string(out)))})
			return
		}
		json.NewEncoder(w).Encode(InstallResponse{Success: true, Output: strings.TrimSpace(string(out))})
	})

	// ── Upgrade ─────────────────────────────────────────────────────────────
	mux.HandleFunc("/api/upgrade", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		var req UpgradeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: "invalid request body"})
			return
		}
		if req.Context == "" || req.Version == "" {
			json.NewEncoder(w).Encode(InstallResponse{Error: "context and version are required"})
			return
		}
		ns := req.Namespace
		if ns == "" {
			ns = "karpenter"
		}
		release := req.Release
		if release == "" {
			release = "karpenter"
		}
		ver := strings.TrimPrefix(req.Version, "v")
		args := []string{
			"upgrade", release,
			"oci://public.ecr.aws/karpenter/karpenter",
			"--version", ver,
			"--namespace", ns,
			"--kube-context", req.Context,
			"--reuse-values",
		}
		if req.ClusterName != "" {
			args = append(args, "--set", "settings.clusterName="+req.ClusterName)
		}
		if req.Region != "" {
			args = append(args,
				"--set", "controller.env[0].name=AWS_REGION",
				"--set", "controller.env[0].value="+req.Region,
			)
		}
		if req.ControllerRoleARN != "" {
			args = append(args, "--set",
				`serviceAccount.annotations.eks\.amazonaws\.com/role-arn=`+req.ControllerRoleARN)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
		if err != nil {
			json.NewEncoder(w).Encode(InstallResponse{
				Error: fmt.Sprintf("%v\n%s", err, strings.TrimSpace(string(out))),
			})
			return
		}
		json.NewEncoder(w).Encode(InstallResponse{Success: true, Output: strings.TrimSpace(string(out))})
	})

	// ── NodePools list ──────────────────────────────────────────────────────
	mux.HandleFunc("/api/nodepools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		kubeCtxParam := r.URL.Query().Get("context")

		// Inline types for k8s JSON parsing (mirrors tui/nodepools.go).
		type k8sCond struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		}
		type k8sMeta struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Limits map[string]string `json:"limits"`
			} `json:"spec"`
			Status struct {
				Conditions []k8sCond `json:"conditions"`
			} `json:"status"`
		}
		type k8sList struct {
			Items []json.RawMessage `json:"items"`
		}
		readyStatus := func(m k8sMeta) (bool, string) {
			for _, c := range m.Status.Conditions {
				if c.Type == "Ready" {
					if c.Status == "True" {
						return true, ""
					}
					msg := c.Reason
					if c.Message != "" {
						msg = c.Message
					}
					return false, msg
				}
			}
			return false, ""
		}

		resp := NodePoolListResponse{
			NodePools:   []NodePoolDetail{},
			NodeClasses: []NodeClassDetail{},
		}

		// NodePools
		npArgs := []string{"get", "nodepools", "-o", "json"}
		if kubeCtxParam != "" {
			npArgs = append(npArgs, "--context", kubeCtxParam)
		}
		if npOut, err := exec.CommandContext(r.Context(), "kubectl", npArgs...).Output(); err == nil {
			var list k8sList
			if json.Unmarshal(npOut, &list) == nil {
				for _, raw := range list.Items {
					var m k8sMeta
					if json.Unmarshal(raw, &m) != nil {
						continue
					}
					ready, msg := readyStatus(m)
					resp.NodePools = append(resp.NodePools, NodePoolDetail{
						Name:        m.Metadata.Name,
						Mode:        m.Metadata.Annotations["karpx.io/generated-mode"],
						Ready:       ready,
						NotReadyMsg: msg,
						CPULim:      m.Spec.Limits["cpu"],
						MemLim:      m.Spec.Limits["memory"],
					})
				}
			}
		}

		// EC2NodeClasses
		ncArgs := []string{"get", "ec2nodeclasses", "-o", "json"}
		if kubeCtxParam != "" {
			ncArgs = append(ncArgs, "--context", kubeCtxParam)
		}
		if ncOut, err := exec.CommandContext(r.Context(), "kubectl", ncArgs...).Output(); err == nil {
			var list k8sList
			if json.Unmarshal(ncOut, &list) == nil {
				for _, raw := range list.Items {
					var m k8sMeta
					if json.Unmarshal(raw, &m) != nil {
						continue
					}
					ready, msg := readyStatus(m)
					resp.NodeClasses = append(resp.NodeClasses, NodeClassDetail{
						Name:        m.Metadata.Name,
						Ready:       ready,
						NotReadyMsg: msg,
					})
				}
			}
		}

		json.NewEncoder(w).Encode(resp)
	})

	// ── Node recommendation ─────────────────────────────────────────────────
	mux.HandleFunc("/api/nodes/recommend", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		var req RecommendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(RecommendResponse{Error: "invalid request body"})
			return
		}

		provider := kube.DetectProvider(req.Context)
		profile, err := kube.AnalyzeWorkloads(req.Context)
		if err != nil {
			profile = &kube.WorkloadProfile{NoRequests: true}
		}

		var mode nodes.OptimizationMode
		switch req.Mode {
		case "cost":
			mode = nodes.ModeCostOptimized
		case "performance":
			mode = nodes.ModeHighPerformance
		default:
			mode = nodes.ModeBalanced
		}

		rec := nodes.Build(profile, mode, provider)
		manifest := nodes.GenerateManifest(rec, req.ClusterName, req.RoleARN)
		json.NewEncoder(w).Encode(RecommendResponse{
			Manifest:   manifest,
			Reasoning:  rec.Reasoning,
			Families:   rec.InstanceFamilies,
			Capacities: rec.CapacityTypes,
			Archs:      rec.Architectures,
		})
	})

	// ── Apply NodePool manifest ─────────────────────────────────────────────
	mux.HandleFunc("/api/nodes/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		var req ApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: "invalid request body"})
			return
		}
		args := []string{"apply", "-f", "-"}
		if req.Context != "" {
			args = append(args, "--context", req.Context)
		}
		cmd := exec.CommandContext(r.Context(), "kubectl", args...)
		cmd.Stdin = strings.NewReader(req.Manifest)
		out, err := cmd.CombinedOutput()
		if err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: fmt.Sprintf("%v\n%s", err, strings.TrimSpace(string(out)))})
			return
		}
		json.NewEncoder(w).Encode(InstallResponse{Success: true, Output: strings.TrimSpace(string(out))})
	})

	srv := &http.Server{
		Addr:        addr,
		Handler:     mux,
		ReadTimeout: 10 * time.Second,
		// WriteTimeout is generous to accommodate the /api/install endpoint,
		// which shells out to helm and can take several minutes.
		WriteTimeout: 10 * time.Minute,
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
	s.DocsURL = provider.Meta().DocsURL

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
		s.KarpenterNamespace = info.Namespace
		s.KarpenterRelease = info.ReleaseName
		if s.KarpenterRelease == "" {
			s.KarpenterRelease = "karpenter"
		}
	}

	// Compatibility + upgrade check (AWS only for now).
	if provider == kube.ProviderAWS {
		// Minimum compatible version from the embedded matrix (no network).
		s.MinCompatible = compat.MinCompatibleKarpenter(k8sVer)

		// Latest compatible version from GitHub (one network call per cluster).
		latest, _, _ := compat.LatestCompatible(k8sVer)

		if info.Installed {
			installed := strings.TrimPrefix(info.Version, "v")
			ok := compat.IsCompatible(installed, k8sVer)
			s.Compatible = &ok
			if latest != "" && latest != installed {
				s.UpgradeAvailable = true
				s.LatestCompatible = latest
			}
		} else {
			// Not installed — surface the latest compatible version for the
			// install button in the dashboard.
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
