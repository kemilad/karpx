// Package compat resolves Karpenter ↔ Kubernetes version compatibility.
//
// Available Karpenter releases are fetched live from the GitHub Releases API:
//   https://api.github.com/repos/aws/karpenter-provider-aws/releases
//
// The supported-Kubernetes range for each Karpenter version is encoded in
// compatMatrix below and mirrors the official compatibility page:
//   https://karpenter.sh/docs/upgrading/compatibility/
package compat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

const releasesURL = "https://api.github.com/repos/aws/karpenter-provider-aws/releases?per_page=50"

// ─────────────────────────────────────────────────────────────────────────────
// Embedded compatibility matrix
// Source: https://karpenter.sh/docs/upgrading/compatibility/
// Keep this table in sync with upstream when new Karpenter minor lines ship.
// ─────────────────────────────────────────────────────────────────────────────

type k8sRange struct {
	karpenterConstraint string // semver constraint on Karpenter version
	k8sMin              string // minimum supported Kubernetes (inclusive)
	k8sMax              string // maximum supported Kubernetes (inclusive, .99 patch wildcard)
}

var compatMatrix = []k8sRange{
	// Karpenter 1.4.x+ — k8s 1.29 – 1.33
	{">= 1.4.0, < 2.0.0", "1.29.0", "1.33.99"},
	// Karpenter 1.2.x – 1.3.x — k8s 1.29 – 1.32
	{">= 1.2.0, < 1.4.0", "1.29.0", "1.32.99"},
	// Karpenter 1.0.x – 1.1.x — k8s 1.28 – 1.31
	{">= 1.0.0, < 1.2.0", "1.28.0", "1.31.99"},
	// Karpenter 0.37.x — k8s 1.27 – 1.30
	{">= 0.37.0, < 1.0.0", "1.27.0", "1.30.99"},
	// Karpenter 0.35.x – 0.36.x — k8s 1.27 – 1.29
	{">= 0.35.0, < 0.37.0", "1.27.0", "1.29.99"},
	// Karpenter 0.33.x – 0.34.x — k8s 1.26 – 1.28
	{">= 0.33.0, < 0.35.0", "1.26.0", "1.28.99"},
}

// ─────────────────────────────────────────────────────────────────────────────
// GitHub releases
// ─────────────────────────────────────────────────────────────────────────────

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// FetchAvailableVersions fetches all stable Karpenter release tags from GitHub
// and returns them as bare semver strings (no leading "v"), newest first.
func FetchAvailableVersions() ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequest("GET", releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "karpx-cli")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Karpenter releases from GitHub: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var releases []ghRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("parse releases response: %w", err)
	}

	var tags []string
	for _, r := range releases {
		if r.Prerelease || r.Draft {
			continue
		}
		tag := strings.TrimPrefix(r.TagName, "v")
		if _, err := semver.NewVersion(tag); err != nil {
			continue // skip non-semver tags (e.g. chart-only tags)
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Compatibility helpers
// ─────────────────────────────────────────────────────────────────────────────

// IsCompatible reports whether a Karpenter version is compatible with the
// given Kubernetes version using the embedded compatibility matrix.
func IsCompatible(karpVersion, k8sVersion string) bool {
	kv, err1 := semver.NewVersion(strings.TrimPrefix(karpVersion, "v"))
	k8sv, err2 := semver.NewVersion(normalise(k8sVersion))
	if err1 != nil || err2 != nil {
		return false
	}
	for _, rule := range compatMatrix {
		c, err := semver.NewConstraint(rule.karpenterConstraint)
		if err != nil {
			continue
		}
		if !c.Check(kv) {
			continue
		}
		// This rule covers the Karpenter version; verify Kubernetes is in range.
		k8sMin, _ := semver.NewVersion(rule.k8sMin)
		k8sMax, _ := semver.NewVersion(rule.k8sMax)
		return k8sv.Compare(k8sMin) >= 0 && k8sv.Compare(k8sMax) <= 0
	}
	return false // no rule matched — unknown karpenter version
}

// FilterCompatible returns the subset of `available` versions that are
// compatible with k8sVersion, sorted descending (latest first).
func FilterCompatible(k8sVersion string, available []string) []string {
	var out []string
	for _, v := range available {
		if IsCompatible(v, k8sVersion) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		vi, _ := semver.NewVersion(out[i])
		vj, _ := semver.NewVersion(out[j])
		return vi.GreaterThan(vj)
	})
	return out
}

// LatestCompatible fetches available Karpenter releases from GitHub and
// returns the latest version compatible with k8sVersion, plus the full
// sorted list of compatible versions.
// Returns ("", nil, nil) when no compatible version is found.
func LatestCompatible(k8sVersion string) (latest string, all []string, err error) {
	available, err := FetchAvailableVersions()
	if err != nil {
		return "", nil, err
	}
	compatible := FilterCompatible(k8sVersion, available)
	if len(compatible) == 0 {
		return "", nil, nil
	}
	return compatible[0], compatible, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Utilities
// ─────────────────────────────────────────────────────────────────────────────

// MinCompatibleKarpenter returns the minimum Karpenter version compatible with
// the given Kubernetes version, derived from the embedded compatibility matrix
// (no network requests required).
// Returns "" if no rule in the matrix covers the given Kubernetes version.
func MinCompatibleKarpenter(k8sVersion string) string {
	k8sv, err := semver.NewVersion(normalise(k8sVersion))
	if err != nil {
		return ""
	}
	var minVer *semver.Version
	for _, rule := range compatMatrix {
		k8sMin, _ := semver.NewVersion(rule.k8sMin)
		k8sMax, _ := semver.NewVersion(rule.k8sMax)
		if k8sv.Compare(k8sMin) < 0 || k8sv.Compare(k8sMax) > 0 {
			continue
		}
		lb := lowerBoundOf(rule.karpenterConstraint)
		if lb == "" {
			continue
		}
		lv, err := semver.NewVersion(lb)
		if err != nil {
			continue
		}
		if minVer == nil || lv.LessThan(minVer) {
			minVer = lv
		}
	}
	if minVer == nil {
		return ""
	}
	return minVer.Original()
}

// lowerBoundOf parses a semver constraint like ">= 0.37.0, < 1.0.0" and
// returns the lower bound value ("0.37.0").
func lowerBoundOf(constraint string) string {
	for _, part := range strings.Split(constraint, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, ">=") {
			return strings.TrimSpace(strings.TrimPrefix(part, ">="))
		}
	}
	return ""
}

// normalise ensures a version string has three dot-separated components.
func normalise(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:3], ".")
}
