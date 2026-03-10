// Package upgrade implements zero-downtime Karpenter upgrades.
//
// For each minor-version hop the sequence is:
//  1. Apply CRDs from the official Helm chart (helm show crds | kubectl apply --server-side)
//  2. Scale the controller to ≥ 2 replicas so one pod stays up during rollover
//  3. helm upgrade --reuse-values
//  4. kubectl rollout status (wait up to 3 minutes)
//
// When upgrading across multiple minor versions the hop is split into one
// step per minor (e.g. 1.0 → 1.1 → 1.2 → 1.3) as recommended by upstream.
package upgrade

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
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
	KubeCtx     string
	Namespace   string   // defaults to "karpenter"
	ReleaseName string   // defaults to "karpenter"
	Current     string   // installed version, bare semver e.g. "1.0.3"
	Target      string   // desired version, bare semver e.g. "1.3.0"
	AllVersions []string // all stable releases (newest first) — used for path
	ReuseValues bool
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

	path, err := BuildPath(p.Current, p.Target, p.AllVersions)
	if err != nil {
		return err
	}

	if len(path) > 1 {
		report(Step{
			Name:   "Upgrade path",
			Detail: fmt.Sprintf("%s → %s  (%d hops: %s)", p.Current, p.Target, len(path), strings.Join(path, " → ")),
			OK:     true,
		})
	}

	origReplicas := currentReplicas(p.KubeCtx, p.Namespace)

	from := p.Current
	for _, to := range path {
		if err := runHop(p.KubeCtx, p.Namespace, p.ReleaseName, from, to, p.ReuseValues, origReplicas, report); err != nil {
			return err
		}
		from = to
	}

	// Restore original replica count after all hops complete.
	if origReplicas > 0 && origReplicas < 2 {
		if scaleErr := scaleDeployment(p.KubeCtx, p.Namespace, origReplicas); scaleErr == nil {
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

func runHop(kubeCtx, namespace, release, from, to string, reuseVals bool, origReplicas int, report Reporter) error {
	// ── 1. Apply CRDs ────────────────────────────────────────────────────
	crdStep := fmt.Sprintf("Apply CRDs  v%s", to)
	report(Step{Name: crdStep, Detail: "helm show crds → kubectl apply --server-side"})
	if err := applyCRDs(kubeCtx, to); err != nil {
		report(Step{Name: crdStep, Err: err.Error()})
		return fmt.Errorf("apply CRDs for v%s: %w", to, err)
	}
	report(Step{Name: crdStep, Detail: "CRDs updated", OK: true})

	// ── 2. Scale to ≥ 2 replicas ─────────────────────────────────────────
	if origReplicas < 2 {
		scaleStep := "Scale to 2 replicas"
		report(Step{Name: scaleStep, Detail: "ensures one pod stays available during rollover"})
		if err := scaleDeployment(kubeCtx, namespace, 2); err != nil {
			// Non-fatal — single-replica installs on small clusters may not
			// have enough capacity; log and continue.
			report(Step{Name: scaleStep, Detail: fmt.Sprintf("skipped (%v)", err), OK: true})
		} else {
			// Brief pause so the new pod can reach Running before we proceed.
			time.Sleep(5 * time.Second)
			report(Step{Name: scaleStep, OK: true})
		}
	}

	// ── 3. helm upgrade ───────────────────────────────────────────────────
	helmStep := fmt.Sprintf("helm upgrade  v%s → v%s", from, to)
	report(Step{Name: helmStep})
	if err := helmUpgrade(kubeCtx, namespace, release, to, reuseVals); err != nil {
		report(Step{Name: helmStep, Err: err.Error()})
		return fmt.Errorf("helm upgrade to v%s: %w", to, err)
	}
	report(Step{Name: helmStep, OK: true})

	// ── 4. Verify rollout ─────────────────────────────────────────────────
	rollStep := "Verify rollout"
	report(Step{Name: rollStep, Detail: "kubectl rollout status (timeout 3m)"})
	if err := waitRollout(kubeCtx, namespace, 3*time.Minute); err != nil {
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

func scaleDeployment(kubeCtx, namespace string, replicas int) error {
	args := []string{
		"scale", "deployment", "karpenter",
		fmt.Sprintf("--replicas=%d", replicas),
		"-n", namespace,
	}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	return exec.Command("kubectl", args...).Run()
}

func waitRollout(kubeCtx, namespace string, timeout time.Duration) error {
	args := []string{
		"rollout", "status", "deployment/karpenter",
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

func currentReplicas(kubeCtx, namespace string) int {
	args := []string{
		"get", "deployment", "karpenter",
		"-n", namespace,
		"-o", "jsonpath={.spec.replicas}",
	}
	if kubeCtx != "" {
		args = append(args, "--context", kubeCtx)
	}
	out, _ := exec.Command("kubectl", args...).Output()
	n := 1
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
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
