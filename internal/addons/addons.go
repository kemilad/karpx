// Package addons manages optional open-source add-ons that karpx can install
// into a Kubernetes cluster via Helm.
package addons

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Status represents the install state of an add-on on a cluster.
type Status int

const (
	StatusUnknown      Status = iota
	StatusInstalled           // helm release found and deployed
	StatusNotInstalled        // no matching helm release found
	StatusError               // could not determine status
)

// Addon is the static definition of an installable add-on.
type Addon struct {
	ID          string   // unique identifier, e.g. "loki-stack"
	Name        string   // human-readable name, e.g. "Logging Stack"
	Description string   // one-line description
	Category    string   // grouping label: Logging / Monitoring / Autoscaling / Security
	RepoName    string   // helm repo alias, e.g. "grafana"
	RepoURL     string   // helm repo URL
	Chart       string   // chart ref: "<repoName>/<chartName>"
	Namespace   string   // default install namespace
	Release     string   // helm release name
	SetValues   []string // extra --set key=value pairs applied on install
}

// Entry pairs an Addon definition with its live detection result.
type Entry struct {
	Addon
	Status           Status
	InstalledVersion string
	Checking         bool
	Error            string
}

// Registry returns the catalog of all supported add-ons.
func Registry() []Addon {
	return []Addon{
		{
			ID:          "loki-stack",
			Name:        "Logging Stack",
			Description: "Grafana + Loki + Promtail — log aggregation and live exploration",
			Category:    "Logging",
			RepoName:    "grafana",
			RepoURL:     "https://grafana.github.io/helm-charts",
			Chart:       "grafana/loki-stack",
			Namespace:   "monitoring",
			Release:     "loki-stack",
			SetValues:   []string{"grafana.enabled=true", "promtail.enabled=true"},
		},
		{
			ID:          "kube-prometheus-stack",
			Name:        "Monitoring Stack",
			Description: "Grafana + Prometheus + Node Exporter — metrics collection and dashboards",
			Category:    "Monitoring",
			RepoName:    "prometheus-community",
			RepoURL:     "https://prometheus-community.github.io/helm-charts",
			Chart:       "prometheus-community/kube-prometheus-stack",
			Namespace:   "monitoring",
			Release:     "kube-prometheus-stack",
		},
		{
			ID:          "keda",
			Name:        "KEDA",
			Description: "Kubernetes Event-Driven Autoscaling — scale on queues, topics, and more",
			Category:    "Autoscaling",
			RepoName:    "kedacore",
			RepoURL:     "https://kedacore.github.io/charts",
			Chart:       "kedacore/keda",
			Namespace:   "keda",
			Release:     "keda",
		},
		{
			ID:          "cert-manager",
			Name:        "cert-manager",
			Description: "Automatic TLS certificate provisioning and renewal via Let's Encrypt / ACME",
			Category:    "Security",
			RepoName:    "jetstack",
			RepoURL:     "https://charts.jetstack.io",
			Chart:       "jetstack/cert-manager",
			Namespace:   "cert-manager",
			Release:     "cert-manager",
			SetValues:   []string{"installCRDs=true"},
		},
	}
}

// ByID returns the Addon with the given ID, or false if not found.
func ByID(id string) (Addon, bool) {
	for _, a := range Registry() {
		if a.ID == id {
			return a, true
		}
	}
	return Addon{}, false
}

// helmRelease is the minimal shape returned by `helm list -o json`.
type helmRelease struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Status     string `json:"status"`
	AppVersion string `json:"app_version"`
}

// Detect queries helm to determine whether an add-on is installed in a cluster.
func Detect(kubeCtx string, a Addon) Entry {
	e := Entry{Addon: a, Status: StatusNotInstalled}

	args := []string{"list", "--all-namespaces", "--output", "json"}
	if kubeCtx != "" {
		args = append(args, "--kube-context", kubeCtx)
	}
	out, err := exec.Command("helm", args...).Output()
	if err != nil {
		e.Status = StatusError
		e.Error = "helm list failed: " + err.Error()
		return e
	}

	var releases []helmRelease
	if err := json.Unmarshal(out, &releases); err != nil {
		e.Status = StatusError
		e.Error = "failed to parse helm output"
		return e
	}

	for _, r := range releases {
		if r.Name == a.Release {
			e.Status = StatusInstalled
			e.InstalledVersion = r.AppVersion
			return e
		}
	}
	return e
}

// Install adds the Helm repo, updates it, creates the namespace, and runs
// `helm upgrade --install` for the given add-on.  All helm output is streamed
// directly to the caller's stdout/stderr so progress is visible interactively.
func Install(kubeCtx string, a Addon) error {
	// ── Step 1: add helm repo ─────────────────────────────────────────────
	fmt.Printf("\n  Adding Helm repo %s …\n", a.RepoName)
	repoAddArgs := []string{"repo", "add", a.RepoName, a.RepoURL, "--force-update"}
	if out, err := exec.Command("helm", repoAddArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("helm repo add: %s", strings.TrimSpace(string(out)))
	}

	// ── Step 2: update repos ──────────────────────────────────────────────
	fmt.Printf("  Updating Helm repos …\n")
	if out, err := exec.Command("helm", "repo", "update").CombinedOutput(); err != nil {
		return fmt.Errorf("helm repo update: %s", strings.TrimSpace(string(out)))
	}

	// ── Step 3: ensure namespace exists ──────────────────────────────────
	fmt.Printf("  Ensuring namespace %q exists …\n", a.Namespace)
	nsArgs := []string{"create", "namespace", a.Namespace}
	if kubeCtx != "" {
		nsArgs = append(nsArgs, "--context", kubeCtx)
	}
	// Ignore error — namespace may already exist.
	_ = exec.Command("kubectl", nsArgs...).Run()

	// ── Step 4: helm upgrade --install ────────────────────────────────────
	fmt.Printf("  Installing %s (this may take a few minutes) …\n\n", a.Name)
	installArgs := []string{
		"upgrade", "--install", a.Release, a.Chart,
		"--namespace", a.Namespace,
		"--wait", "--timeout", "10m",
	}
	for _, sv := range a.SetValues {
		installArgs = append(installArgs, "--set", sv)
	}
	if kubeCtx != "" {
		installArgs = append(installArgs, "--kube-context", kubeCtx)
	}

	cmd := exec.Command("helm", installArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("helm install failed (see output above)")
	}
	return nil
}

// Uninstall removes the helm release for an add-on.
func Uninstall(kubeCtx string, a Addon) error {
	fmt.Printf("\n  Uninstalling %s …\n\n", a.Name)
	args := []string{"uninstall", a.Release, "--namespace", a.Namespace}
	if kubeCtx != "" {
		args = append(args, "--kube-context", kubeCtx)
	}
	cmd := exec.Command("helm", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("helm uninstall failed (see output above)")
	}
	return nil
}
