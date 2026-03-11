// Package upgrade implements zero-downtime Karpenter upgrades.
//
// For each minor-version hop the sequence is:
//  1. Apply CRDs from the official Helm chart (helm show crds | kubectl apply --server-side)
//  2. Scale the controller to ≥ 2 replicas and wait for the extra pod to be Ready
//  3a. If Karpenter was installed via Helm: helm upgrade --reuse-values
//  3b. If installed via raw manifests: kubectl set image (preserves all existing config)
//  4. kubectl rollout status (wait up to 5 minutes)
//
// When upgrading across multiple minor versions the hop is split into one
// step per minor (e.g. 1.0 → 1.1 → 1.2 → 1.3) as recommended by upstream.
//
// When the installed version is unknown (Karpenter detected outside Helm
// without a readable image tag), a single direct hop to the target is used.
package upgrade

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// ─────────────────────────────────────────────────────────────────────────────
// Public types
// ─────────────────────────────────────────────────────────────────────────────

// Step is the result of one upgrade action reported to the caller.
type Step struct {
	Name   string // short label
	Detail string // optional extra info
	OK     bool   // true = success
	Err    string // non-empty = failed
}

// Reporter is called after every step so callers can print or collect progress.
type Reporter func(Step)

// Params holds all inputs for Run.
type Params struct {
	KubeCtx        string
	Namespace      string   // defaults to "karpenter"
	ReleaseName    string   // Helm release name; defaults to "karpenter"
	DeploymentName string   // controller Deployment name; defaults to "karpenter"
	Current        string   // installed version, bare semver e.g. "1.0.3"; "" = unknown
	Target         string   // desired version, bare semver e.g. "1.3.0"
	AllVersions    []string // all stable releases (newest first) — used for path building
	ReuseValues    bool
	ViaHelm        bool // true when a Helm release manages this install
}

// ─────────────────────────────────────────────────────────────────────────────
// Entry point
// ─────────────────────────────────────────────────────────────────────────────

// Run executes a zero-downtime upgrade and calls report after every step.
// Returns an error if any step fails (upgrade is not rolled back automatically).
func Run(p Params, report Reporter) error {
	if p.Namespace == "" {
		p.Namespace = "karpenter"
	}
	if p.ReleaseName == "" {
		p.ReleaseName = "karpenter"
	}
	if p.DeploymentName == "" {
		p.DeploymentName = "karpenter"
	}

	// When the installed version is unknown we cannot build a hop-by-hop path.
	// Perform a single direct upgrade to the target instead.
	if p.Current == "" {
		report(Step{
			Name:   "Version unknown",
			Detail: "installed version could not be determined; performing direct upgrade to v" + p.Target,
			OK:     true,
		})
		return runHop(p, p.Current, p.Target, report)
	}

	path, err := BuildPath(p.Current, p.Target, p.AllVersions)
	if err != nil {
		return err
	}

	if len(path) > 1 {
		report(Step{
			Name:   "Upgrade path",
			Detail: fmt.Sprintf("v%s → %s  (%d hops, one minor at a time)", p.Current, "v"+strings.Join(path, " → v"), len(path)),
			OK:     true,
		})
	}

	origReplicas := currentReplicas(p.KubeCtx, p.Namespace, p.DeploymentName)

	from := p.Current
	for _, to := range path {
		hopParams := p
		hopParams.Current = from
		if err := runHop(hopParams, from, to, report); err != nil {
			// Restore original replica count on failure so we don't leave
			// the cluster in an unexpected HA state.
			if origReplicas > 0 && origReplicas < 2 {
				_ = scaleDeployment(p.KubeCtx, p.Namespace, p.DeploymentName, origReplicas)
			}
			return err
		}
		from = to
	}

	// Restore original replica count after all hops complete.
	if origReplicas > 0 && origReplicas < 2 {
		if scaleErr := scaleDeployment(p.KubeCtx, p.Namespace, p.DeploymentName, origReplicas); scaleErr == nil {
			report(Step{
				Name:   "Restore replicas",
				Detail: fmt.Sprintf("scaled back to %d replica(s)", origReplicas),
				OK:     true,
			})
		}
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Single-hop logic
// ─────────────────────────────────────────────────────────────────────────────

func runHop(p Params, from, to string, report Reporter) error {
	// ── 1. Apply CRDs ─────────────────────────────────────────────────────
	crdStep := fmt.Sprintf("Apply CRDs  v%s", to)
	report(Step{Name: crdStep, Detail: "helm show crds → kubectl apply --server-side"})
	if err := applyCRDs(p.KubeCtx, to); err != nil {
		report(Step{Name: crdStep, Err: err.Error()})
		return fmt.Errorf("apply CRDs for v%s: %w", to, err)
	}
	report(Step{Name: crdStep, Detail: "CRDs updated", OK: true})

	// ── 2. Scale to ≥ 2 replicas and wait for HA ──────────────────────────
	origReplicas := currentReplicas(p.KubeCtx, p.Namespace, p.DeploymentName)
	if origReplicas < 2 {
		scaleStep := "Scale to 2 replicas"
		report(Step{Name: scaleStep, Detail: "ensures one pod stays available during rollover"})
		if err := scaleDeployment(p.KubeCtx, p.Namespace, p.DeploymentName, 2); err != nil {
			// Non-fatal on small clusters — log and continue.
			report(Step{Name: scaleStep, Detail: fmt.Sprintf("skipped (%v)", err), OK: true})
		} else {
			// Wait for the second replica to become Ready before proceeding.
			// This is the key zero-downtime guarantee: both pods must be healthy
			// before we start the rolling update so one stays up during rollover.
			if waitErr := waitForReadyReplicas(p.KubeCtx, p.Namespace, p.DeploymentName, 2, 90*time.Second); waitErr != nil {
				// Still non-fatal — the cluster may be resource-constrained.
				report(Step{Name: scaleStep, Detail: "second replica not ready within 90s — continuing anyway", OK: true})
			} else {
				report(Step{Name: scaleStep, Detail: "2 replicas ready", OK: true})
			}
		}
	}

	// ── 3. Upgrade the controller ─────────────────────────────────────────
	if p.ViaHelm {
		helmStep := fmt.Sprintf("helm upgrade  v%s → v%s", from, to)
		report(Step{Name: helmStep})
		if err := helmUpgrade(p.KubeCtx, p.Namespace, p.ReleaseName, to, p.ReuseValues); err != nil {
			report(Step{Name: helmStep, Err: err.Error()})
			return fmt.Errorf("helm upgrade to v%s: %w", to, err)
		}
		report(Step{Name: helmStep, OK: true})
	} else {
		imgStep := fmt.Sprintf("Update image  v%s → v%s", from, to)
		report(Step{Name: imgStep, Detail: "kubectl set image (preserves existing configuration)"})
		if err := imageUpgrade(p.KubeCtx, p.Namespace, p.DeploymentName, to); err != nil {
			report(Step{Name: imgStep, Err: err.Error()})
			return fmt.Errorf("image update to v%s: %w", to, err)
		}
		report(Step{Name: imgStep, OK: true})
	}

	// ── 4. Verify rollout ─────────────────────────────────────────────────
	rollStep := "Verify rollout"
	report(Step{Name: rollStep, Detail: "kubectl rollout status (timeout 5m)"})
	if err := waitRollout(p.KubeCtx, p.Namespace, p.DeploymentName, 5*time.Minute); err != nil {
		report(Step{Name: rollStep, Err: err.Error()})
		return fmt.Errorf("rollout verification: %w", err)
	}
	report(Step{Name: rollStep, Detail: "all pods healthy", OK: true})

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func applyCRDs(kubeCtx, version string) error {
	ver := strings.TrimPrefix(version, "v")

	// Pull CRDs directly from the official Helm chart — no GitHub URL dependency.
	crdOut, err := exec.Command("helm", "show", "crds",
		"oci://public.ecr.aws/karpenter/karpenter",
		"--version", ver,
	).Output()
	if err != nil {
		return fmt.Errorf("helm show crds: %w", err)
	}
	if len(bytes.TrimSpace(crdOut)) == 0 {
		return nil // nothing to apply
	}

	// --server-side + --force-conflicts handles CRD field ownership cleanly.
	args := []string{"apply", "-f", "-", "--server-side", "--force-conflicts"}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = bytes.NewReader(crdOut)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// helmUpgrade upgrades an existing Helm-managed Karpenter release.
func helmUpgrade(kubeCtx, namespace, release, version string, reuseVals bool) error {
	ver := strings.TrimPrefix(version, "v")
	args := []string{
		"upgrade", release,
		"oci://public.ecr.aws/karpenter/karpenter",
		"--version", ver,
		"--namespace", namespace,
	}
	if reuseVals {
		args = append(args, "--reuse-values")
	}
	if kubeCtx != "" {
		args = append(args, "--kube-context", kubeCtx)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// imageUpgrade updates the Karpenter controller image for manifest-installed
// (non-Helm) clusters. It uses kubectl set image so all existing Deployment
// settings (env vars, IRSA annotations, resource limits, etc.) are preserved.
func imageUpgrade(kubeCtx, namespace, deploymentName, version string) error {
	ver := strings.TrimPrefix(version, "v")
	image := fmt.Sprintf("public.ecr.aws/karpenter/controller:v%s", ver)
	args := []string{
		"set", "image",
		"-n", namespace,
		"deployment/" + deploymentName,
		"controller=" + image,
	}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func scaleDeployment(kubeCtx, namespace, deploymentName string, replicas int) error {
	args := []string{
		"scale", "deployment", deploymentName,
		fmt.Sprintf("--replicas=%d", replicas),
		"-n", namespace,
	}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	return exec.Command("kubectl", args...).Run()
}

// waitForReadyReplicas polls until the deployment reports at least n ready
// replicas or the timeout expires. It polls every 5 seconds.
func waitForReadyReplicas(kubeCtx, namespace, deploymentName string, n int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	args := []string{
		"get", "deployment", deploymentName,
		"-n", namespace,
		"-o", "jsonpath={.status.readyReplicas}",
	}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	for time.Now().Before(deadline) {
		out, err := exec.Command("kubectl", args...).Output()
		if err == nil {
			var ready int
			if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &ready); scanErr == nil && ready >= n {
				return nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %d ready replica(s) on %s", n, deploymentName)
}

func waitRollout(kubeCtx, namespace, deploymentName string, timeout time.Duration) error {
	args := []string{
		"rollout", "status", "deployment/" + deploymentName,
		"-n", namespace,
		fmt.Sprintf("--timeout=%s", timeout),
	}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func currentReplicas(kubeCtx, namespace, deploymentName string) int {
	args := []string{
		"get", "deployment", deploymentName,
		"-n", namespace,
		"-o", "jsonpath={.spec.replicas}",
	}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	out, _ := exec.Command("kubectl", args...).Output()
	s := strings.TrimSpace(string(out))
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return 1
}

// ─────────────────────────────────────────────────────────────────────────────
// Upgrade path builder
// ─────────────────────────────────────────────────────────────────────────────

// BuildPath returns the ordered list of bare semver versions to upgrade
// through, one minor version at a time, e.g. ["1.1.4","1.2.1","1.3.0"].
func BuildPath(current, target string, available []string) ([]string, error) {
	cur, err := semver.NewVersion(strings.TrimPrefix(current, "v"))
	if err != nil {
		return nil, fmt.Errorf("invalid current version %q: %w", current, err)
	}
	tgt, err := semver.NewVersion(strings.TrimPrefix(target, "v"))
	if err != nil {
		return nil, fmt.Errorf("invalid target version %q: %w", target, err)
	}
	if cur.Equal(tgt) {
		return nil, fmt.Errorf("already on %s", target)
	}
	if cur.GreaterThan(tgt) {
		return nil, fmt.Errorf("downgrade not supported (%s → %s)", current, target)
	}

	// Same minor: direct single hop.
	if cur.Major() == tgt.Major() && cur.Minor() == tgt.Minor() {
		return []string{tgt.Original()}, nil
	}

	// Build a map of minor → latest patch from available versions.
	type minorKey struct{ major, minor uint64 }
	latestPatch := map[minorKey]*semver.Version{}
	for _, v := range available {
		sv, err := semver.NewVersion(strings.TrimPrefix(v, "v"))
		if err != nil {
			continue
		}
		k := minorKey{sv.Major(), sv.Minor()}
		if ex, ok := latestPatch[k]; !ok || sv.GreaterThan(ex) {
			latestPatch[k] = sv
		}
	}

	// Walk intermediate minors (cur.minor+1 … tgt.minor-1).
	var path []string
	for minor := cur.Minor() + 1; minor < tgt.Minor(); minor++ {
		k := minorKey{cur.Major(), minor}
		if v, ok := latestPatch[k]; ok {
			path = append(path, v.Original())
		}
	}
	// Final hop is always the requested target.
	path = append(path, tgt.Original())
	return path, nil
}

// SortAsc sorts bare semver strings ascending in-place.
func SortAsc(vs []string) {
	sort.Slice(vs, func(i, j int) bool {
		vi, _ := semver.NewVersion(vs[i])
		vj, _ := semver.NewVersion(vs[j])
		if vi == nil || vj == nil {
			return vs[i] < vs[j]
		}
		return vi.LessThan(vj)
	})
}
