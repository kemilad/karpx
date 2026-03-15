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
	Category    string   // grouping label: Logging / Monitoring / Networking / Autoscaling / Security
	RepoName    string   // helm repo alias, e.g. "grafana"
	RepoURL     string   // helm repo URL
	Chart       string   // chart ref: "<repoName>/<chartName>"
	Namespace   string   // default install namespace
	Release     string   // helm release name
	SetValues   []string // extra --set key=value pairs applied on install

	// DisableGrafanaIfReleases: if any of these Helm release names are already
	// deployed, grafana.enabled=false is injected into the install args to avoid
	// a duplicate Grafana instance.
	DisableGrafanaIfReleases []string

	// ReconfigureSiblingID is the ID of another add-on in the Registry.  When
	// this add-on is installed and the sibling release is already deployed, karpx
	// upgrades the sibling with grafana.enabled=false so the two add-ons share the
	// single Grafana provided by this one.
	ReconfigureSiblingID string

	// GrafanaSvc is the Kubernetes Service name to use in the port-forward hint
	// printed after a successful install.  Empty means no hint is shown.
	GrafanaSvc string

	// GrafanaDefaultCreds is a short human-readable credential hint, e.g.
	// "admin / prom-operator".  Shown alongside the port-forward hint.
	GrafanaDefaultCreds string

	// RequiresClusterName, when true, injects clusterName=<name> into the
	// Helm set-values, deriving the name from the kubeconfig context.
	RequiresClusterName bool

	// PostInstallNotes is extra text printed after a successful install.
	PostInstallNotes string
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
			Description: "Loki + Promtail + Grafana — log aggregation (Grafana shared with Monitoring Stack when both installed)",
			Category:    "Logging",
			RepoName:    "grafana",
			RepoURL:     "https://grafana.github.io/helm-charts",
			Chart:       "grafana/loki-stack",
			Namespace:   "monitoring",
			Release:     "loki-stack",
			SetValues:   []string{"grafana.enabled=true", "promtail.enabled=true"},
			// When kube-prometheus-stack is already installed, skip the duplicate Grafana.
			DisableGrafanaIfReleases: []string{"kube-prometheus-stack"},
			GrafanaSvc:               "loki-stack-grafana",
			GrafanaDefaultCreds:      "admin / run: kubectl get secret -n monitoring loki-stack-grafana -o jsonpath='{.data.admin-password}' | base64 -d",
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
			// When loki-stack is already installed with its own Grafana, upgrade it
			// to use this Grafana instead.
			ReconfigureSiblingID: "loki-stack",
			GrafanaSvc:           "kube-prometheus-stack-grafana",
			GrafanaDefaultCreds:  "admin / prom-operator",
		},
		{
			ID:          "aws-load-balancer-controller",
			Name:        "AWS Load Balancer Controller",
			Description: "Provision AWS ALB/NLB for Kubernetes Services and Ingresses",
			Category:    "Networking",
			RepoName:    "eks",
			RepoURL:     "https://aws.github.io/eks-charts",
			Chart:       "eks/aws-load-balancer-controller",
			Namespace:   "kube-system",
			Release:     "aws-load-balancer-controller",
			// clusterName is injected automatically from kubeconfig context.
			RequiresClusterName: true,
			PostInstallNotes: `  ℹ  The AWS Load Balancer Controller needs an IAM role with the
  AWSLoadBalancerControllerIAMPolicy attached, annotated on its ServiceAccount:

    kubectl annotate serviceaccount -n kube-system aws-load-balancer-controller \
      eks.amazonaws.com/role-arn=arn:aws:iam::<ACCOUNT_ID>:role/<ROLE_NAME>

  See full setup guide: https://kubernetes-sigs.github.io/aws-load-balancer-controller/`,
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

// isReleaseInstalled reports whether a named Helm release is currently deployed.
func isReleaseInstalled(kubeCtx, releaseName string) bool {
	args := []string{"list", "--all-namespaces", "--output", "json"}
	if kubeCtx != "" {
		args = append(args, "--kube-context", kubeCtx)
	}
	out, err := exec.Command("helm", args...).Output()
	if err != nil {
		return false
	}
	var releases []helmRelease
	if err := json.Unmarshal(out, &releases); err != nil {
		return false
	}
	for _, r := range releases {
		if r.Name == releaseName {
			return true
		}
	}
	return false
}

// overrideSetValue replaces the value for key in setValues (key=oldVal → key=newVal).
// If the key is not found, it appends key=newVal.
func overrideSetValue(setValues []string, key, value string) []string {
	prefix := key + "="
	for i, sv := range setValues {
		if strings.HasPrefix(sv, prefix) {
			result := make([]string, len(setValues))
			copy(result, setValues)
			result[i] = key + "=" + value
			return result
		}
	}
	return append(setValues, key+"="+value)
}

// clusterNameFromCtx derives a bare cluster name from a kubeconfig context.
// EKS ARN: "arn:aws:eks:<region>:<account>:cluster/<name>" → "<name>"
// Otherwise the context string itself is returned unchanged.
func clusterNameFromCtx(kubeCtx string) string {
	if kubeCtx == "" {
		return ""
	}
	if idx := strings.LastIndex(kubeCtx, ":cluster/"); idx >= 0 {
		return kubeCtx[idx+9:]
	}
	return kubeCtx
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
//
// Shared-Grafana logic: if the add-on declares DisableGrafanaIfReleases and one
// of those releases is already deployed, grafana.enabled=false is injected so
// only one Grafana instance runs across both stacks.  Conversely, if the add-on
// declares ReconfigureSiblingID and that sibling is already installed with its
// own Grafana, karpx upgrades the sibling to disable its Grafana automatically.
func Install(kubeCtx string, a Addon) error {
	// ── Build effective set-values ────────────────────────────────────────
	setValues := make([]string, len(a.SetValues))
	copy(setValues, a.SetValues)

	grafanaDisabledBy := "" // non-empty when grafana is provided by another release
	for _, rel := range a.DisableGrafanaIfReleases {
		if isReleaseInstalled(kubeCtx, rel) {
			setValues = overrideSetValue(setValues, "grafana.enabled", "false")
			grafanaDisabledBy = rel
			fmt.Printf("  ℹ  Grafana is already provided by %s — skipping duplicate installation.\n", rel)
			break
		}
	}

	if a.RequiresClusterName {
		if cn := clusterNameFromCtx(kubeCtx); cn != "" {
			setValues = append(setValues, "clusterName="+cn)
		}
	}

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
	for _, sv := range setValues {
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

	// ── Step 5: reconfigure sibling to share Grafana ──────────────────────
	if a.ReconfigureSiblingID != "" {
		if sib, ok := ByID(a.ReconfigureSiblingID); ok && isReleaseInstalled(kubeCtx, sib.Release) {
			fmt.Printf("\n  ℹ  %s detected — upgrading it to share Grafana …\n", sib.Name)
			upArgs := []string{
				"upgrade", sib.Release, sib.Chart,
				"--namespace", sib.Namespace,
				"--reuse-values",
				"--set", "grafana.enabled=false",
				"--wait", "--timeout", "5m",
			}
			if kubeCtx != "" {
				upArgs = append(upArgs, "--kube-context", kubeCtx)
			}
			upCmd := exec.Command("helm", upArgs...)
			upCmd.Stdout = os.Stdout
			upCmd.Stderr = os.Stderr
			_ = upCmd.Run() // best-effort; don't fail the parent install
		}
	}

	// ── Step 6: print Grafana access hint ────────────────────────────────
	printGrafanaHint(kubeCtx, a, grafanaDisabledBy)

	// ── Step 7: extra post-install notes ─────────────────────────────────
	if a.PostInstallNotes != "" {
		fmt.Printf("\n%s\n", a.PostInstallNotes)
	}

	return nil
}

// printGrafanaHint prints port-forward instructions for the Grafana UI.
// If grafanaDisabledBy is non-empty, it looks up that release's Grafana service
// so the user knows where the shared instance lives.
func printGrafanaHint(kubeCtx string, a Addon, grafanaDisabledBy string) {
	svc := a.GrafanaSvc
	ns := a.Namespace
	creds := a.GrafanaDefaultCreds

	if grafanaDisabledBy != "" {
		// Point the user at the sibling's Grafana instead.
		for _, addon := range Registry() {
			if addon.Release == grafanaDisabledBy && addon.GrafanaSvc != "" {
				svc = addon.GrafanaSvc
				ns = addon.Namespace
				creds = addon.GrafanaDefaultCreds
				break
			}
		}
	}

	if svc == "" {
		return
	}

	pfCmd := fmt.Sprintf("kubectl port-forward -n %s svc/%s 3000:80", ns, svc)
	if kubeCtx != "" {
		pfCmd += " --context " + kubeCtx
	}

	fmt.Printf("\n  ─── Grafana ─────────────────────────────────────────────────────\n")
	fmt.Printf("  Port-forward:  %s\n", pfCmd)
	fmt.Printf("  URL:           http://localhost:3000\n")
	if creds != "" {
		fmt.Printf("  Credentials:   %s\n", creds)
	}
	fmt.Printf("  ─────────────────────────────────────────────────────────────────\n")
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
