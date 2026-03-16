// Package ui serves the karpx web dashboard.
// It embeds the static HTML at compile time so the binary has no external
// file dependencies.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/kemilad/karpx/internal/addons"
	"github.com/kemilad/karpx/internal/compat"
	"github.com/kemilad/karpx/internal/helm"
	"github.com/kemilad/karpx/internal/kube"
	"github.com/kemilad/karpx/internal/nodes"
	karpupgrade "github.com/kemilad/karpx/internal/upgrade"
)

//go:embed static/index.html static/karpx-logo.svg
var staticFiles embed.FS

// ── Grafana port-forward manager ─────────────────────────────────────────────
// Tracks the one kubectl port-forward process managed by karpx so we can
// reuse it or replace it when the user clicks the Grafana button.
var (
	pfMu      sync.Mutex
	pfProcess *os.Process // nil when no port-forward is running
)

// isPortListening returns true when something is accepting TCP connections on
// localhost:<port> (checked with a short dial timeout).
func isPortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// startGrafanaPortForward starts kubectl port-forward for the given service and
// waits (up to 15 s) until port 3000 is ready.  If port 3000 is already in use
// it returns immediately.  A previously managed port-forward is killed first.
func startGrafanaPortForward(kubeCtx, namespace, svc string) error {
	const localPort = 3000

	if isPortListening(localPort) {
		return nil // already forwarding (or something else on port 3000)
	}

	// Kill any port-forward we previously started.
	pfMu.Lock()
	if pfProcess != nil {
		_ = pfProcess.Kill()
		pfProcess = nil
	}
	pfMu.Unlock()

	args := []string{"port-forward", "-n", namespace, "svc/" + svc, fmt.Sprintf("%d:80", localPort)}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("kubectl port-forward failed to start: %w", err)
	}

	pfMu.Lock()
	pfProcess = cmd.Process
	pfMu.Unlock()

	// Wait up to 15 s for the port to become available.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)
		if isPortListening(localPort) {
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for port-forward to become ready on :%d", localPort)
}

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

// InstallResponse is the JSON body returned by POST /api/install and /api/upgrade.
type InstallResponse struct {
	Success    bool     `json:"success"`
	Output     string   `json:"output,omitempty"`
	Steps      []string `json:"steps,omitempty"` // upgrade step log
	Error      string   `json:"error,omitempty"`
	GrafanaURL string   `json:"grafana_url,omitempty"` // e.g. "http://localhost:3000"
	GrafanaCmd string   `json:"grafana_cmd,omitempty"` // kubectl port-forward command
}

// addonInstallEvent is a single NDJSON line streamed by POST /api/addons/install.
// The client reads the body as a stream; each newline-terminated JSON object
// carries a step message and cumulative percentage.  The final object has
// Done==true and carries the success/error outcome.
type addonInstallEvent struct {
	Step       string `json:"step,omitempty"`
	Pct        int    `json:"pct"`
	Done       bool   `json:"done,omitempty"`
	Success    bool   `json:"success,omitempty"`
	Error      string `json:"error,omitempty"`
	GrafanaURL string `json:"grafana_url,omitempty"`
	GrafanaCmd string `json:"grafana_cmd,omitempty"`
}

// UninstallRequest is the JSON body for POST /api/uninstall.
type UninstallRequest struct {
	Context         string `json:"context"`
	Namespace       string `json:"namespace"`
	Release         string `json:"release"`
	DeleteCRDs      bool   `json:"delete_crds"`
	DeleteNamespace bool   `json:"delete_namespace"`
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
	Role        string `json:"role,omitempty"`
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

// AddonStatusEntry is one row in the GET /api/addons response.
type AddonStatusEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Release     string `json:"release"`
	Namespace   string `json:"namespace"`
	Status      string `json:"status"` // "installed" | "not_installed" | "error"
	Version     string `json:"version,omitempty"`
	Error       string `json:"error,omitempty"`
	GrafanaURL  string `json:"grafana_url,omitempty"` // set when addon provides/shares Grafana
	GrafanaCmd  string `json:"grafana_cmd,omitempty"` // kubectl port-forward command
}

// AddonActionRequest is the JSON body for POST /api/addons/install and /api/addons/uninstall.
type AddonActionRequest struct {
	Context string `json:"context"`
	AddonID string `json:"addon_id"`
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

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		var steps []string
		addStep := func(s string) { steps = append(steps, s) }

		// ── Step 1: helm uninstall ────────────────────────────────────────
		addStep("Running helm uninstall…")
		helmArgs := []string{"uninstall", release, "--namespace", ns, "--kube-context", req.Context}
		out, err := exec.CommandContext(ctx, "helm", helmArgs...).CombinedOutput()
		if err != nil {
			addStep(fmt.Sprintf("✗ helm uninstall failed: %v — %s", err, strings.TrimSpace(string(out))))
			json.NewEncoder(w).Encode(InstallResponse{Error: strings.Join(steps, "\n"), Steps: steps})
			return
		}
		addStep("✓ Helm release removed")

		// ── Step 2: delete custom resources ──────────────────────────────
		if req.DeleteCRDs {
			addStep("Deleting NodeClaims…")
			kubectlDel := func(resource string) {
				args := []string{"delete", resource, "--all", "--context", req.Context, "--ignore-not-found"}
				o, e := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
				if e != nil {
					addStep(fmt.Sprintf("⚠ kubectl delete %s: %v — %s", resource, e, strings.TrimSpace(string(o))))
				} else {
					addStep(fmt.Sprintf("✓ %s deleted", resource))
				}
			}
			kubectlDel("nodeclaims")
			kubectlDel("nodepools")
			// provider-specific node classes
			for _, res := range []string{
				"ec2nodeclasses.karpenter.k8s.aws",
				"aksnodeclasses.karpenter.azure.com",
				"gcpnodeclasses.karpenter.k8s.gcp",
			} {
				args := []string{"delete", res, "--all", "--context", req.Context, "--ignore-not-found"}
				o, e := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
				if e == nil && strings.TrimSpace(string(o)) != "" {
					addStep(fmt.Sprintf("✓ %s deleted", res))
				}
			}

			// ── Step 3: delete CRDs ───────────────────────────────────────
			addStep("Deleting Karpenter CRDs…")
			crdArgs := []string{
				"delete", "crd", "--ignore-not-found", "--context", req.Context,
				"nodepools.karpenter.sh",
				"nodeclaims.karpenter.sh",
				"ec2nodeclasses.karpenter.k8s.aws",
				"aksnodeclasses.karpenter.azure.com",
				"gcpnodeclasses.karpenter.k8s.gcp",
			}
			o, e := exec.CommandContext(ctx, "kubectl", crdArgs...).CombinedOutput()
			if e != nil {
				addStep(fmt.Sprintf("⚠ CRD deletion: %v — %s", e, strings.TrimSpace(string(o))))
			} else {
				addStep("✓ Karpenter CRDs removed")
			}
		}

		// ── Step 4: delete namespace ──────────────────────────────────────
		if req.DeleteNamespace {
			addStep(fmt.Sprintf("Deleting namespace %q…", ns))
			nsArgs := []string{"delete", "namespace", ns, "--context", req.Context, "--ignore-not-found"}
			o, e := exec.CommandContext(ctx, "kubectl", nsArgs...).CombinedOutput()
			if e != nil {
				addStep(fmt.Sprintf("⚠ namespace deletion: %v — %s", e, strings.TrimSpace(string(o))))
			} else {
				addStep(fmt.Sprintf("✓ Namespace %q deleted", ns))
			}
		}

		addStep("✓ Uninstall complete")
		json.NewEncoder(w).Encode(InstallResponse{Success: true, Steps: steps})
	})

	// ── Zero-downtime upgrade ────────────────────────────────────────────────
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

		// Detect current install for namespace / release name / upgrade method.
		info, err := helm.DetectKarpenter(req.Context)
		if err != nil || !info.Installed {
			json.NewEncoder(w).Encode(InstallResponse{Error: "Karpenter not detected on this cluster"})
			return
		}

		// info.Chart is only populated for Helm-managed releases; empty = raw manifests.
		viaHelm := info.Chart != ""

		ns := req.Namespace
		if ns == "" {
			ns = info.Namespace
		}
		if ns == "" {
			ns = "karpenter"
		}
		release := req.Release
		if release == "" {
			release = info.ReleaseName
		}
		if release == "" {
			release = "karpenter"
		}
		// For non-Helm (API-detected) installs ReleaseName == deployment name.
		deploymentName := release
		if deploymentName == "" {
			deploymentName = "karpenter"
		}

		installed := strings.TrimPrefix(info.Version, "v")
		target := strings.TrimPrefix(req.Version, "v")

		// Fetch all available versions for path calculation.
		k8sVer, _ := kube.GetServerVersion(req.Context)
		_, allVersions, _ := compat.LatestCompatible(k8sVer)

		// Collect steps for the response.
		var steps []string
		reporter := func(s karpupgrade.Step) {
			if s.Err != "" {
				steps = append(steps, fmt.Sprintf("✗ %s: %s", s.Name, s.Err))
			} else if s.Detail != "" {
				steps = append(steps, fmt.Sprintf("✓ %s: %s", s.Name, s.Detail))
			} else {
				steps = append(steps, fmt.Sprintf("✓ %s", s.Name))
			}
		}

		// 15-minute timeout covers multi-hop upgrades.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		_ = ctx // upgrade.Run uses exec directly; context enforced above

		if err := karpupgrade.Run(karpupgrade.Params{
			KubeCtx:        req.Context,
			Namespace:      ns,
			ReleaseName:    release,
			DeploymentName: deploymentName,
			Current:        installed,
			Target:         target,
			AllVersions:    allVersions,
			ReuseValues:    true,
			ViaHelm:        viaHelm,
		}, reporter); err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: err.Error(), Steps: steps})
			return
		}
		json.NewEncoder(w).Encode(InstallResponse{Success: true, Steps: steps})
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
		// k8sMeta covers NodePool (v1beta1) and EC2NodeClass (v1beta1).
		type k8sMeta struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Limits          map[string]string `json:"limits"`          // NodePool v1beta1
				Role            string            `json:"role"`            // EC2NodeClass v1beta1
				InstanceProfile string            `json:"instanceProfile"` // AWSNodeTemplate v1alpha1
			} `json:"spec"`
			Status struct {
				Conditions []k8sCond `json:"conditions"`
			} `json:"status"`
		}
		// provisionerMeta covers Provisioner (v1alpha5) with nested limits.
		type provisionerMeta struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Limits struct {
					Resources map[string]string `json:"resources"`
				} `json:"limits"`
			} `json:"spec"`
			Status struct {
				Conditions []k8sCond `json:"conditions"`
			} `json:"status"`
		}
		type k8sList struct {
			Items []json.RawMessage `json:"items"`
		}
		readyStatus := func(conds []k8sCond) (bool, string) {
			for _, c := range conds {
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
		isCRDMissing := func(errStr string) bool {
			return strings.Contains(errStr, "no matches for kind") ||
				strings.Contains(errStr, "the server doesn't have a resource type")
		}

		resp := NodePoolListResponse{
			NodePools:   []NodePoolDetail{},
			NodeClasses: []NodeClassDetail{},
		}

		// ── NodePools (v1beta1, Karpenter ≥ v0.31) ────────────────────────
		npArgs := []string{"get", "nodepools.karpenter.sh", "-o", "json"}
		if kubeCtxParam != "" {
			npArgs = append(npArgs, "--context", kubeCtxParam)
		}
		npOut, npErr := exec.CommandContext(r.Context(), "kubectl", npArgs...).Output()
		if npErr != nil {
			var exitErr *exec.ExitError
			if errors.As(npErr, &exitErr) {
				errStr := strings.TrimSpace(string(exitErr.Stderr))
				if errStr != "" && !isCRDMissing(errStr) {
					resp.Error = errStr
				}
			}
		} else {
			var list k8sList
			if json.Unmarshal(npOut, &list) == nil {
				for _, raw := range list.Items {
					var m k8sMeta
					if json.Unmarshal(raw, &m) != nil {
						continue
					}
					ready, msg := readyStatus(m.Status.Conditions)
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

		// ── Provisioners (v1alpha5, Karpenter < v0.31) — fallback ─────────
		if len(resp.NodePools) == 0 && resp.Error == "" {
			provArgs := []string{"get", "provisioners.karpenter.sh", "-o", "json"}
			if kubeCtxParam != "" {
				provArgs = append(provArgs, "--context", kubeCtxParam)
			}
			if provOut, provErr := exec.CommandContext(r.Context(), "kubectl", provArgs...).Output(); provErr == nil {
				var list k8sList
				if json.Unmarshal(provOut, &list) == nil {
					for _, raw := range list.Items {
						var m provisionerMeta
						if json.Unmarshal(raw, &m) != nil {
							continue
						}
						ready, msg := readyStatus(m.Status.Conditions)
						resp.NodePools = append(resp.NodePools, NodePoolDetail{
							Name:        m.Metadata.Name,
							Mode:        m.Metadata.Annotations["karpx.io/generated-mode"],
							Ready:       ready,
							NotReadyMsg: msg,
							CPULim:      m.Spec.Limits.Resources["cpu"],
							MemLim:      m.Spec.Limits.Resources["memory"],
						})
					}
				}
			}
		}

		// ── EC2NodeClasses (v1beta1, Karpenter ≥ v0.31) ───────────────────
		ncArgs := []string{"get", "ec2nodeclasses.karpenter.k8s.aws", "-o", "json"}
		if kubeCtxParam != "" {
			ncArgs = append(ncArgs, "--context", kubeCtxParam)
		}
		ncOut, ncErr := exec.CommandContext(r.Context(), "kubectl", ncArgs...).Output()
		if ncErr == nil {
			var list k8sList
			if json.Unmarshal(ncOut, &list) == nil {
				for _, raw := range list.Items {
					var m k8sMeta
					if json.Unmarshal(raw, &m) != nil {
						continue
					}
					ready, msg := readyStatus(m.Status.Conditions)
					role := m.Spec.Role
					if role == "" {
						role = m.Spec.InstanceProfile
					}
					resp.NodeClasses = append(resp.NodeClasses, NodeClassDetail{
						Name:        m.Metadata.Name,
						Role:        role,
						Ready:       ready,
						NotReadyMsg: msg,
					})
				}
			}
		}

		// ── AWSNodeTemplates (v1alpha1, Karpenter < v0.31) — fallback ─────
		if len(resp.NodeClasses) == 0 {
			antArgs := []string{"get", "awsnodetemplates.karpenter.k8s.aws", "-o", "json"}
			if kubeCtxParam != "" {
				antArgs = append(antArgs, "--context", kubeCtxParam)
			}
			if antOut, antErr := exec.CommandContext(r.Context(), "kubectl", antArgs...).Output(); antErr == nil {
				var list k8sList
				if json.Unmarshal(antOut, &list) == nil {
					for _, raw := range list.Items {
						var m k8sMeta
						if json.Unmarshal(raw, &m) != nil {
							continue
						}
						ready, msg := readyStatus(m.Status.Conditions)
						role := m.Spec.InstanceProfile
						if role == "" {
							role = m.Spec.Role
						}
						resp.NodeClasses = append(resp.NodeClasses, NodeClassDetail{
							Name:        m.Metadata.Name,
							Role:        role,
							Ready:       ready,
							NotReadyMsg: msg,
						})
					}
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
		case "freetier":
			mode = nodes.ModeFreeTier
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

	// ── Validate NodePool manifest (dry-run) ────────────────────────────────
	mux.HandleFunc("/api/nodes/validate", func(w http.ResponseWriter, r *http.Request) {
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
		if strings.TrimSpace(req.Manifest) == "" {
			json.NewEncoder(w).Encode(InstallResponse{Error: "manifest is empty"})
			return
		}
		args := []string{"apply", "--dry-run=server", "-f", "-"}
		if req.Context != "" {
			args = append(args, "--context", req.Context)
		}
		cmd := exec.CommandContext(r.Context(), "kubectl", args...)
		cmd.Stdin = strings.NewReader(req.Manifest)
		out, err := cmd.CombinedOutput()
		if err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: strings.TrimSpace(string(out))})
			return
		}
		json.NewEncoder(w).Encode(InstallResponse{Success: true, Output: strings.TrimSpace(string(out))})
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

	// ── Add-ons list ────────────────────────────────────────────────────────
	mux.HandleFunc("/api/addons", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		kubeCtxParam := r.URL.Query().Get("context")
		catalog := addons.Registry()

		// Detect all addons and track which releases are installed.
		type detected struct {
			addon  addons.Addon
			entry  addons.Entry
			status string
		}
		rows := make([]detected, len(catalog))
		installedByRelease := map[string]bool{}
		for i, a := range catalog {
			e := addons.Detect(kubeCtxParam, a)
			status := "not_installed"
			switch e.Status {
			case addons.StatusInstalled:
				status = "installed"
				installedByRelease[a.Release] = true
			case addons.StatusError:
				status = "error"
			}
			rows[i] = detected{addon: a, entry: e, status: status}
		}

		// Build response entries, computing Grafana URLs where applicable.
		entries := make([]AddonStatusEntry, len(catalog))
		for i, d := range rows {
			a := d.addon
			se := AddonStatusEntry{
				ID:          a.ID,
				Name:        a.Name,
				Description: a.Description,
				Category:    a.Category,
				Release:     a.Release,
				Namespace:   a.Namespace,
				Status:      d.status,
				Version:     d.entry.InstalledVersion,
				Error:       d.entry.Error,
			}

			// Attach Grafana URL for any installed addon that has a Grafana service.
			if d.status == "installed" && a.GrafanaSvc != "" {
				grafanaSvc := a.GrafanaSvc
				grafanaNS := a.Namespace
				grafanaCreds := a.GrafanaDefaultCreds
				// If another release provides Grafana (shared-Grafana scenario), use that one.
				for _, rel := range a.DisableGrafanaIfReleases {
					if installedByRelease[rel] {
						for _, other := range catalog {
							if other.Release == rel && other.GrafanaSvc != "" {
								grafanaSvc = other.GrafanaSvc
								grafanaNS = other.Namespace
								grafanaCreds = other.GrafanaDefaultCreds
								break
							}
						}
						break
					}
				}
				pfCmd := fmt.Sprintf("kubectl port-forward -n %s svc/%s 3000:80", grafanaNS, grafanaSvc)
				if kubeCtxParam != "" {
					pfCmd += " --context " + kubeCtxParam
				}
				se.GrafanaURL = "http://localhost:3000"
				se.GrafanaCmd = pfCmd
				if grafanaCreds != "" {
					se.GrafanaCmd += "\n# credentials: " + grafanaCreds
				}
			}
			entries[i] = se
		}
		json.NewEncoder(w).Encode(entries)
	})

	// ── Add-on install ───────────────────────────────────────────────────────
	mux.HandleFunc("/api/addons/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Stream newline-delimited JSON so the browser can update progress in real time.
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		flusher, _ := w.(http.Flusher)

		sendEvt := func(evt addonInstallEvent) {
			b, _ := json.Marshal(evt)
			w.Write(append(b, '\n'))
			if flusher != nil {
				flusher.Flush()
			}
		}
		sendStep := func(step string, pct int) { sendEvt(addonInstallEvent{Step: step, Pct: pct}) }
		sendFail := func(step, errMsg string, pct int) {
			sendEvt(addonInstallEvent{Step: step, Pct: pct, Done: true, Success: false, Error: errMsg})
		}

		var req AddonActionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sendFail("✗ invalid request body", "invalid request body", 0)
			return
		}
		a, ok := addons.ByID(req.AddonID)
		if !ok {
			sendFail("✗ unknown add-on: "+req.AddonID, "unknown add-on: "+req.AddonID, 0)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		// ── Build effective set-values (shared-Grafana + cluster name) ────
		setValues := make([]string, len(a.SetValues))
		copy(setValues, a.SetValues)

		grafanaDisabledBy := ""
		for _, rel := range a.DisableGrafanaIfReleases {
			if addons.IsReleaseInstalled(req.Context, rel) {
				setValues = addonOverrideSetValue(setValues, "grafana.enabled", "false")
				grafanaDisabledBy = rel
				sendStep(fmt.Sprintf("ℹ  Grafana already provided by %s — skipping duplicate", rel), 3)
				break
			}
		}

		if a.RequiresClusterName {
			if cn := addonClusterName(req.Context); cn != "" {
				setValues = append(setValues, "clusterName="+cn)
				sendStep(fmt.Sprintf("ℹ  Cluster name: %s", cn), 3)
			}
		}

		if a.RequiresRegion {
			if region := addonRegion(req.Context); region != "" {
				setValues = append(setValues, "region="+region)
				sendStep(fmt.Sprintf("ℹ  AWS region: %s", region), 3)
			}
		}

		if a.RequiresVPCID {
			region := addonRegion(req.Context)
			clusterName := addonClusterName(req.Context)
			sendStep(fmt.Sprintf("ℹ  Looking up VPC ID for cluster %q…", clusterName), 3)
			if vpcID := addonVPCID(region, clusterName); vpcID != "" {
				setValues = append(setValues, "vpcId="+vpcID)
				sendStep(fmt.Sprintf("ℹ  VPC ID: %s", vpcID), 4)
			} else {
				sendStep("⚠ Could not resolve VPC ID — install may fail if IMDS is unavailable", 4)
			}
		}

		// Step 1: add helm repo
		sendStep(fmt.Sprintf("Adding Helm repo %s…", a.RepoName), 5)
		out, err := exec.CommandContext(ctx, "helm", "repo", "add", a.RepoName, a.RepoURL, "--force-update").CombinedOutput()
		if err != nil {
			sendFail("✗ "+strings.TrimSpace(string(out)), strings.TrimSpace(string(out)), 5)
			return
		}
		sendStep("✓ Repo added", 12)

		// Step 2: update repos
		sendStep("Updating Helm repos…", 12)
		if out, err = exec.CommandContext(ctx, "helm", "repo", "update").CombinedOutput(); err != nil {
			sendStep("⚠ repo update: "+strings.TrimSpace(string(out)), 18)
		} else {
			sendStep("✓ Repos updated", 18)
		}

		// Step 3: create namespace (ignore error — may already exist)
		nsArgs := []string{"create", "namespace", a.Namespace}
		if req.Context != "" {
			nsArgs = append(nsArgs, "--context", req.Context)
		}
		_ = exec.CommandContext(ctx, "kubectl", nsArgs...).Run()
		sendStep(fmt.Sprintf("✓ Namespace %q ready", a.Namespace), 22)

		// Step 3.5: disable sibling's Grafana BEFORE installing to prevent
		// "multiple default datasources" crashloop in our Grafana.
		siblingReconfigured := false
		if a.ReconfigureSiblingID != "" {
			if sib, sibOK := addons.ByID(a.ReconfigureSiblingID); sibOK && addons.IsReleaseInstalled(req.Context, sib.Release) {
				sendStep(fmt.Sprintf("Disabling %s Grafana to prevent datasource conflicts…", sib.Name), 25)
				upArgs := []string{
					"upgrade", sib.Release, sib.Chart,
					"--namespace", sib.Namespace,
					"--reuse-values", "--set", "grafana.enabled=false",
					"--wait", "--timeout", "5m",
				}
				if req.Context != "" {
					upArgs = append(upArgs, "--kube-context", req.Context)
				}
				upOut, upErr := exec.CommandContext(ctx, "helm", upArgs...).CombinedOutput()
				if upErr != nil {
					sendStep("⚠ "+strings.TrimSpace(string(upOut)), 30)
				} else {
					sendStep(fmt.Sprintf("✓ %s Grafana disabled", sib.Name), 30)
				}
				siblingReconfigured = true

				if sib.ID == "loki-stack" {
					setValues = append(setValues,
						"grafana.additionalDataSources[0].name=Loki",
						"grafana.additionalDataSources[0].type=loki",
						"grafana.additionalDataSources[0].url=http://loki-stack:3100",
						"grafana.additionalDataSources[0].access=proxy",
						"grafana.additionalDataSources[0].isDefault=false",
						"grafana.sidecar.dashboards.enabled=true",
						"grafana.dashboards.default.loki-logs.gnetId=13639",
						"grafana.dashboards.default.loki-logs.revision=2",
						"grafana.dashboards.default.loki-logs.datasource=Loki",
					)
				}
			}
		}

		// Step 3.8: purge stale Grafana datasource ConfigMaps before our Grafana starts.
		// Helm does not always garbage-collect the loki-stack datasource ConfigMap
		// (label: grafana_datasource=1) when grafana.enabled is toggled off, leaving
		// an isDefault:true entry that conflicts with Prometheus and crashloops Grafana.
		if a.GrafanaSvc != "" {
			purgeArgs := []string{
				"delete", "configmap",
				"-n", a.Namespace,
				"-l", "grafana_datasource=1",
				"--ignore-not-found",
			}
			if req.Context != "" {
				purgeArgs = append(purgeArgs, "--context", req.Context)
			}
			purgeOut, _ := exec.CommandContext(ctx, "kubectl", purgeArgs...).CombinedOutput()
			if len(strings.TrimSpace(string(purgeOut))) > 0 {
				sendStep("ℹ  Removed stale datasource ConfigMaps: "+strings.TrimSpace(string(purgeOut)), 32)
			}
		}

		// Step 4: helm upgrade --install — run in goroutine and tick progress.
		helmStartPct := 25
		if siblingReconfigured {
			helmStartPct = 33
		}
		sendStep(fmt.Sprintf("Installing %s (this may take several minutes)…", a.Name), helmStartPct)

		installArgs := []string{
			"upgrade", "--install", a.Release, a.Chart,
			"--namespace", a.Namespace,
			"--wait", "--timeout", "9m",
		}
		for _, sv := range setValues {
			installArgs = append(installArgs, "--set", sv)
		}
		if req.Context != "" {
			installArgs = append(installArgs, "--kube-context", req.Context)
		}

		type helmRes struct {
			out []byte
			err error
		}
		helmDone := make(chan helmRes, 1)
		go func() {
			o, e := exec.CommandContext(ctx, "helm", installArgs...).CombinedOutput()
			helmDone <- helmRes{o, e}
		}()

		curPct := helmStartPct
		ticker := time.NewTicker(10 * time.Second)
		var hRes helmRes
	helmLoop:
		for {
			select {
			case hRes = <-helmDone:
				break helmLoop
			case <-ticker.C:
				curPct += 5
				if curPct > 84 {
					curPct = 84
				}
				sendStep(fmt.Sprintf("Installing %s…", a.Name), curPct)
			}
		}
		ticker.Stop()

		if hRes.err != nil {
			sendFail("✗ helm install failed:\n"+strings.TrimSpace(string(hRes.out)),
				"helm install failed", curPct)
			return
		}
		sendStep(fmt.Sprintf("✓ %s installed successfully", a.Name), 88)

		// Step 5: add this stack's datasource to the sibling's Grafana.
		_ = siblingReconfigured // already done in Step 3.5
		if grafanaDisabledBy != "" && a.ID == "loki-stack" {
			if prom, promOK := addons.ByID("kube-prometheus-stack"); promOK {
				sendStep("Adding Loki datasource to the shared Grafana…", 91)
				upArgs := []string{
					"upgrade", prom.Release, prom.Chart,
					"--namespace", prom.Namespace,
					"--reuse-values",
					"--set", "grafana.additionalDataSources[0].name=Loki",
					"--set", "grafana.additionalDataSources[0].type=loki",
					"--set", "grafana.additionalDataSources[0].url=http://loki-stack:3100",
					"--set", "grafana.additionalDataSources[0].access=proxy",
					"--set", "grafana.additionalDataSources[0].isDefault=false",
					"--set", "grafana.sidecar.dashboards.enabled=true",
					"--set", "grafana.dashboards.default.loki-logs.gnetId=13639",
					"--set", "grafana.dashboards.default.loki-logs.revision=2",
					"--set", "grafana.dashboards.default.loki-logs.datasource=Loki",
					"--wait", "--timeout", "5m",
				}
				if req.Context != "" {
					upArgs = append(upArgs, "--kube-context", req.Context)
				}
				upOut, upErr := exec.CommandContext(ctx, "helm", upArgs...).CombinedOutput()
				if upErr != nil {
					sendStep("⚠ "+strings.TrimSpace(string(upOut)), 94)
				} else {
					sendStep("✓ Loki datasource and dashboard added to shared Grafana", 94)
				}
			}
		}

		// Step 6: build Grafana access info and send final done event.
		grafanaSvc := a.GrafanaSvc
		grafanaNS := a.Namespace
		grafanaCreds := a.GrafanaDefaultCreds
		if grafanaDisabledBy != "" {
			for _, addon := range addons.Registry() {
				if addon.Release == grafanaDisabledBy && addon.GrafanaSvc != "" {
					grafanaSvc = addon.GrafanaSvc
					grafanaNS = addon.Namespace
					grafanaCreds = addon.GrafanaDefaultCreds
					break
				}
			}
		}

		finalEvt := addonInstallEvent{
			Step:    "✓ Done",
			Pct:     100,
			Done:    true,
			Success: true,
		}
		if grafanaSvc != "" {
			finalEvt.GrafanaURL = "http://localhost:3000"
			finalEvt.GrafanaCmd = fmt.Sprintf("kubectl port-forward -n %s svc/%s 3000:80", grafanaNS, grafanaSvc)
			if req.Context != "" {
				finalEvt.GrafanaCmd += " --context " + req.Context
			}
			if grafanaCreds != "" {
				finalEvt.GrafanaCmd += "\n# credentials: " + grafanaCreds
			}
		}
		sendEvt(finalEvt)
	})

	// ── Add-on uninstall ─────────────────────────────────────────────────────
	mux.HandleFunc("/api/addons/uninstall", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		var req AddonActionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(InstallResponse{Error: "invalid request body"})
			return
		}
		a, ok := addons.ByID(req.AddonID)
		if !ok {
			json.NewEncoder(w).Encode(InstallResponse{Error: "unknown add-on: " + req.AddonID})
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		var steps []string
		addStep := func(s string) { steps = append(steps, s) }

		addStep(fmt.Sprintf("Uninstalling %s…", a.Name))
		unArgs := []string{"uninstall", a.Release, "--namespace", a.Namespace}
		if req.Context != "" {
			unArgs = append(unArgs, "--kube-context", req.Context)
		}
		out, err := exec.CommandContext(ctx, "helm", unArgs...).CombinedOutput()
		if err != nil {
			addStep("✗ " + strings.TrimSpace(string(out)))
			json.NewEncoder(w).Encode(InstallResponse{Error: strings.Join(steps, "\n"), Steps: steps})
			return
		}
		addStep(fmt.Sprintf("✓ %s uninstalled", a.Name))
		json.NewEncoder(w).Encode(InstallResponse{Success: true, Steps: steps})
	})

	// ── Grafana port-forward ──────────────────────────────────────────────────
	// POST /api/addons/grafana-portforward
	// Starts (or reuses) a kubectl port-forward to the addon's Grafana service
	// on localhost:3000, then returns once the port is ready.
	mux.HandleFunc("/api/addons/grafana-portforward", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		var req AddonActionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "invalid request body"})
			return
		}
		a, ok := addons.ByID(req.AddonID)
		if !ok {
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "unknown add-on: " + req.AddonID})
			return
		}

		// Resolve effective Grafana service (shared-Grafana: prefer sibling's svc).
		grafanaSvc := a.GrafanaSvc
		grafanaNS := a.Namespace
		if grafanaSvc == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "this add-on does not provide a Grafana service"})
			return
		}
		for _, rel := range a.DisableGrafanaIfReleases {
			if addons.IsReleaseInstalled(req.Context, rel) {
				for _, other := range addons.Registry() {
					if other.Release == rel && other.GrafanaSvc != "" {
						grafanaSvc = other.GrafanaSvc
						grafanaNS = other.Namespace
						break
					}
				}
				break
			}
		}

		if err := startGrafanaPortForward(req.Context, grafanaNS, grafanaSvc); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "url": "http://localhost:3000"})
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
			if installed != "" {
				ok := compat.IsCompatible(installed, k8sVer)
				s.Compatible = &ok
				if latest != "" && installed != latest {
					s.UpgradeAvailable = true
				}
			} else {
				// Version unknown (installed outside Helm without a readable image tag).
				// Always recommend upgrading — we cannot determine if it's current.
				s.UpgradeAvailable = true
			}
			if latest != "" {
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

// addonOverrideSetValue replaces key=<old> with key=<new> in a set-values slice,
// or appends key=value if the key is not present.
func addonOverrideSetValue(setValues []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, len(setValues))
	copy(result, setValues)
	for i, sv := range result {
		if strings.HasPrefix(sv, prefix) {
			result[i] = key + "=" + value
			return result
		}
	}
	return append(result, key+"="+value)
}

// addonClusterName derives a bare cluster name from a kubeconfig context string.
// EKS ARN format: "arn:aws:eks:<region>:<account>:cluster/<name>" → "<name>"
// Otherwise the context string itself is returned.
func addonClusterName(kubeCtx string) string {
	if kubeCtx == "" {
		return ""
	}
	if idx := strings.LastIndex(kubeCtx, ":cluster/"); idx >= 0 {
		return kubeCtx[idx+9:]
	}
	return kubeCtx
}

// addonRegion extracts the AWS region from a kubeconfig context EKS ARN.
// EKS ARN format: "arn:aws:eks:<region>:<account>:cluster/<name>" → "<region>"
// Returns "" if the context is not an EKS ARN.
func addonRegion(kubeCtx string) string {
	parts := strings.Split(kubeCtx, ":")
	if len(parts) >= 6 && parts[0] == "arn" && parts[2] == "eks" {
		return parts[3]
	}
	return ""
}

// addonVPCID queries the EKS cluster via the AWS CLI to get the VPC ID.
func addonVPCID(region, clusterName string) string {
	if region == "" || clusterName == "" {
		return ""
	}
	out, err := exec.Command("aws", "eks", "describe-cluster",
		"--region", region,
		"--name", clusterName,
		"--query", "cluster.resourcesVpcConfig.vpcId",
		"--output", "text",
	).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
