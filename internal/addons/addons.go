// Package addons manages optional open-source add-ons that karpx can install
// into a Kubernetes cluster via Helm.
package addons

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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

	// RequiresRegion, when true, injects region=<region> into the Helm
	// set-values, deriving the AWS region from the kubeconfig context EKS ARN.
	RequiresRegion bool

	// RequiresVPCID, when true, injects vpcId=<id> into the Helm set-values
	// by querying the EKS cluster via the AWS CLI (avoids IMDS dependency).
	RequiresVPCID bool

	// PostInstallNotes is extra text printed after a successful install.
	PostInstallNotes string
}

// Entry pairs an Addon definition with its live detection result.
type Entry struct {
	Addon
	Status           Status
	InstalledVersion string
	// ActualRelease / ActualNamespace are set when an addon was installed outside
	// karpx (different release name).  Operations that need to reference the live
	// release (upgrade hints, etc.) should prefer these over Addon.Release/Namespace.
	ActualRelease   string
	ActualNamespace string
	Checking        bool
	Error           string
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
			SetValues: []string{
				"grafana.enabled=true",
				"promtail.enabled=true",
				// Provision a Loki logs-explorer dashboard from Grafana.com at startup.
				"grafana.sidecar.dashboards.enabled=true",
				"grafana.dashboards.default.loki-logs.gnetId=13639",
				"grafana.dashboards.default.loki-logs.revision=2",
				"grafana.dashboards.default.loki-logs.datasource=Loki",
			},
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
			RequiresRegion:      true,
			RequiresVPCID:       true,
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
	Chart      string `json:"chart"`      // e.g. "kube-prometheus-stack-65.1.1"
	AppVersion string `json:"app_version"`
}

// addonChartName extracts the chart name from an Addon.Chart value.
// "prometheus-community/kube-prometheus-stack" → "kube-prometheus-stack"
func addonChartName(chart string) string {
	if idx := strings.LastIndex(chart, "/"); idx >= 0 {
		return chart[idx+1:]
	}
	return chart
}

// releaseChartName strips the version suffix from a helm release chart field.
// "kube-prometheus-stack-65.1.1" → "kube-prometheus-stack"
func releaseChartName(helmChart string) string {
	parts := strings.Split(helmChart, "-")
	for i := len(parts) - 1; i > 0; i-- {
		if len(parts[i]) > 0 && parts[i][0] >= '0' && parts[i][0] <= '9' {
			return strings.Join(parts[:i], "-")
		}
	}
	return helmChart
}

// IsReleaseInstalled reports whether a named Helm release is currently deployed.
func IsReleaseInstalled(kubeCtx, releaseName string) bool {
	return isReleaseInstalled(kubeCtx, releaseName)
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

// regionFromCtx extracts the AWS region from a kubeconfig context EKS ARN.
// EKS ARN: "arn:aws:eks:<region>:<account>:cluster/<name>" → "<region>"
// Returns "" if the context is not an EKS ARN.
func regionFromCtx(kubeCtx string) string {
	// arn:aws:eks:<region>:<account>:cluster/<name>
	parts := strings.Split(kubeCtx, ":")
	if len(parts) >= 6 && parts[0] == "arn" && parts[2] == "eks" {
		return parts[3]
	}
	return ""
}

// vpcIDFromCluster queries the EKS cluster via the AWS CLI to get the VPC ID.
// This avoids the controller having to fetch it from EC2 instance metadata (IMDS).
func vpcIDFromCluster(region, clusterName string) string {
	if region == "" || clusterName == "" {
		return ""
	}
	args := []string{
		"eks", "describe-cluster",
		"--region", region,
		"--name", clusterName,
		"--query", "cluster.resourcesVpcConfig.vpcId",
		"--output", "text",
	}
	out, err := exec.Command("aws", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

	wantChart := addonChartName(a.Chart)
	for _, r := range releases {
		if r.Name == a.Release || releaseChartName(r.Chart) == wantChart {
			e.Status = StatusInstalled
			e.InstalledVersion = r.AppVersion
			e.ActualRelease = r.Name
			e.ActualNamespace = r.Namespace
			return e
		}
	}
	return e
}

// printProgress renders an in-place progress bar on the current terminal line.
// \r rewrites the line; \033[K clears any leftover characters.  When pct==100
// a newline is emitted to "commit" the final line.
func printProgress(pct int, label string) {
	const width = 30
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := width * pct / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	fmt.Printf("\r  [%s] %3d%%  %s\033[K", bar, pct, label)
	if pct >= 100 {
		fmt.Println()
	}
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

	if a.RequiresRegion {
		if region := regionFromCtx(kubeCtx); region != "" {
			setValues = append(setValues, "region="+region)
			fmt.Printf("  ℹ  Detected AWS region: %s\n", region)
		}
	}

	if a.RequiresVPCID {
		region := regionFromCtx(kubeCtx)
		clusterName := clusterNameFromCtx(kubeCtx)
		fmt.Printf("  ℹ  Looking up VPC ID for cluster %q …\n", clusterName)
		if vpcID := vpcIDFromCluster(region, clusterName); vpcID != "" {
			setValues = append(setValues, "vpcId="+vpcID)
			fmt.Printf("  ℹ  VPC ID: %s\n", vpcID)
		} else {
			fmt.Printf("  ⚠  Could not resolve VPC ID — install may fail if IMDS is unavailable\n")
		}
	}

	// ── Step 1: add helm repo ─────────────────────────────────────────────
	printProgress(5, "Adding Helm repo "+a.RepoName+"…")
	repoAddArgs := []string{"repo", "add", a.RepoName, a.RepoURL, "--force-update"}
	if out, err := exec.Command("helm", repoAddArgs...).CombinedOutput(); err != nil {
		fmt.Println()
		return fmt.Errorf("helm repo add: %s", strings.TrimSpace(string(out)))
	}

	// ── Step 2: update repos ──────────────────────────────────────────────
	printProgress(12, "Updating Helm repos…")
	if out, err := exec.Command("helm", "repo", "update").CombinedOutput(); err != nil {
		fmt.Println()
		return fmt.Errorf("helm repo update: %s", strings.TrimSpace(string(out)))
	}

	// ── Step 3: ensure namespace exists ──────────────────────────────────
	printProgress(18, "Ensuring namespace "+a.Namespace+"…")
	nsArgs := []string{"create", "namespace", a.Namespace}
	if kubeCtx != "" {
		nsArgs = append(nsArgs, "--context", kubeCtx)
	}
	_ = exec.Command("kubectl", nsArgs...).Run() // ignore error — may already exist

	// ── Step 3.5: disable sibling's Grafana BEFORE installing ────────────
	// Must run before our install so our Grafana doesn't start up and pick up
	// the sibling's datasource ConfigMap (which also has isDefault:true),
	// causing a "multiple default datasources" crashloop.
	siblingReconfigured := false
	if a.ReconfigureSiblingID != "" {
		if sib, ok := ByID(a.ReconfigureSiblingID); ok && isReleaseInstalled(kubeCtx, sib.Release) {
			printProgress(28, "Disabling "+sib.Name+" Grafana to prevent conflicts…")
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
			_, _ = exec.Command("helm", upArgs...).CombinedOutput() // best-effort
			siblingReconfigured = true

			// Also add the sibling's data service as a non-default datasource
			// in this addon's Grafana so data remains visible after install,
			// and provision the Loki logs dashboard in the shared Grafana.
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

	// ── Step 3.8: purge stale Grafana datasource ConfigMaps ──────────────
	// Helm does not always garbage-collect the loki-stack datasource ConfigMap
	// (label: grafana_datasource=1) when grafana.enabled is toggled off, leaving
	// an isDefault:true entry that conflicts with Prometheus.  Delete all such
	// ConfigMaps before our Grafana starts so it only sees its own datasources.
	if a.GrafanaSvc != "" {
		purgeArgs := []string{
			"delete", "configmap",
			"-n", a.Namespace,
			"-l", "grafana_datasource=1",
			"--ignore-not-found",
		}
		if kubeCtx != "" {
			purgeArgs = append(purgeArgs, "--context", kubeCtx)
		}
		_, _ = exec.Command("kubectl", purgeArgs...).CombinedOutput() // best-effort
	}

	// ── Step 4: helm upgrade --install ────────────────────────────────────
	helmStartPct := 25
	if siblingReconfigured {
		helmStartPct = 32
	}
	printProgress(helmStartPct, "Installing "+a.Name+" (this may take a few minutes)…")

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

	type helmResult struct {
		out []byte
		err error
	}
	helmDone := make(chan helmResult, 1)
	go func() {
		out, err := exec.Command("helm", installArgs...).CombinedOutput()
		helmDone <- helmResult{out, err}
	}()

	cur := helmStartPct
	ticker := time.NewTicker(9 * time.Second)
	var hRes helmResult
helmLoop:
	for {
		select {
		case hRes = <-helmDone:
			break helmLoop
		case <-ticker.C:
			cur += 4
			if cur > 84 {
				cur = 84
			}
			printProgress(cur, "Installing "+a.Name+"…")
		}
	}
	ticker.Stop()

	if hRes.err != nil {
		fmt.Println()
		fmt.Print(string(hRes.out))
		return fmt.Errorf("helm install failed (see output above)")
	}
	printProgress(88, "✓ "+a.Name+" installed")

	// ── Step 5: add this stack's datasource to the sibling's Grafana ────────
	// When this addon's Grafana is disabled (sibling provides it), upgrade the
	// sibling to expose this stack's data as a non-default datasource.
	if grafanaDisabledBy != "" && a.ID == "loki-stack" {
		if prom, ok := ByID("kube-prometheus-stack"); ok {
			printProgress(94, "Adding Loki datasource to shared Grafana…")
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
			if kubeCtx != "" {
				upArgs = append(upArgs, "--kube-context", kubeCtx)
			}
			_, _ = exec.Command("helm", upArgs...).CombinedOutput() // best-effort
		}
	}
	_ = siblingReconfigured // already done in Step 3.5

	printProgress(100, "✓ Done")

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
