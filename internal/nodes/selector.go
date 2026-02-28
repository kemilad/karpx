// Package nodes contains the node-type recommendation engine.
// It takes a WorkloadProfile + user preference and produces an optimal
// Karpenter NodePool configuration for the detected cloud provider.
package nodes

import (
	"fmt"

	"github.com/kemilad/karpx/internal/kube"
)

// OptimizationMode represents the user's provisioning priority.
type OptimizationMode string

const (
	ModeCostOptimized  OptimizationMode = "cost"
	ModeBalanced       OptimizationMode = "balanced"
	ModeHighPerformance OptimizationMode = "performance"
)

// Recommendation holds all parameters needed to generate a NodePool manifest.
type Recommendation struct {
	Mode             OptimizationMode
	WorkloadType     kube.WorkloadType
	Provider         kube.Provider

	// Instance selection (meaning varies by provider — see manifest.go)
	InstanceFamilies []string // AWS families / Azure SKU families / GCP machine families
	CapacityTypes    []string // "spot", "on-demand"
	Architectures    []string // "arm64", "amd64"
	CPUSizes         []string // vCPU counts to include

	// Sizing hints derived from actual workloads
	MinNodeCPU  int // minimum vCPUs per node
	MinNodeMiB  int // minimum memory per node in MiB

	// Human-readable explanation bullets printed to the user
	Reasoning []string
}

// Build produces a Recommendation for the given workload profile, optimisation
// mode, and cloud provider.
func Build(
	profile *kube.WorkloadProfile,
	mode OptimizationMode,
	provider kube.Provider,
) Recommendation {
	wtype := kube.ClassifyWorkload(profile)

	r := Recommendation{
		Mode:         mode,
		WorkloadType: wtype,
		Provider:     provider,
	}

	// ── Sizing hints from observed workloads ────────────────────────────────
	r.MinNodeCPU = minCPU(profile.MaxPodCPUm)
	r.MinNodeMiB = minMemMiB(profile.MaxPodMemMiB)
	r.CPUSizes = cpuSizes(r.MinNodeCPU)

	// ── Provider-specific instance selection ────────────────────────────────
	switch provider {
	case kube.ProviderAWS:
		buildAWS(&r, profile, wtype, mode)
	case kube.ProviderAzure:
		buildAzure(&r, profile, wtype, mode)
	case kube.ProviderGCP:
		buildGCP(&r, profile, wtype, mode)
	default:
		r.Reasoning = append(r.Reasoning, "Provider unknown — showing generic guidance only")
	}

	return r
}

// ─────────────────────────────────────────────────────────────────────────────
// AWS EKS
// ─────────────────────────────────────────────────────────────────────────────

func buildAWS(r *Recommendation, p *kube.WorkloadProfile, wtype kube.WorkloadType, mode OptimizationMode) {
	switch mode {
	case ModeCostOptimized:
		r.CapacityTypes = []string{"spot", "on-demand"}
		switch wtype {
		case kube.WorkloadGPU:
			// Spot GPU is feasible on g5g (Graviton) or g4dn
			r.InstanceFamilies = []string{"g5g", "g4dn", "g5"}
			r.Architectures = []string{"arm64", "amd64"}
			r.Reasoning = addReasons(r.Reasoning,
				"GPU workloads detected — using spot-eligible GPU families (g5g Graviton first)",
				"Spot GPU saves ~70% vs on-demand; ensure GPU pods tolerate interruption",
			)
		case kube.WorkloadMemory:
			r.InstanceFamilies = []string{"r7g", "r6g", "r7i", "r6i"}
			r.Architectures = []string{"arm64", "amd64"}
			r.Reasoning = addReasons(r.Reasoning,
				"Memory-intensive workloads (>4 GiB/core) — memory-optimised families (r-series)",
				"Graviton r7g/r6g selected first for best $/GiB ratio on Spot",
			)
		case kube.WorkloadCPU:
			r.InstanceFamilies = []string{"c7g", "c6g", "c7i", "c6i", "c6a"}
			r.Architectures = []string{"arm64", "amd64"}
			r.Reasoning = addReasons(r.Reasoning,
				"Compute-intensive workloads (<2 GiB/core) — compute-optimised families (c-series)",
				"Graviton c7g/c6g offer best compute $/vCPU on Spot",
			)
		case kube.WorkloadBatch:
			r.InstanceFamilies = []string{"m7g", "m6g", "c7g", "c6g", "m7i", "m6i"}
			r.Architectures = []string{"arm64", "amd64"}
			r.Reasoning = addReasons(r.Reasoning,
				"Batch/job workloads — mixed general+compute families with Spot for lowest cost",
				"Karpenter's consolidation will terminate idle nodes between job runs",
			)
		default: // general / unknown
			r.InstanceFamilies = []string{"m7g", "m6g", "m7i", "m6i", "m6a"}
			r.Architectures = []string{"arm64", "amd64"}
			r.Reasoning = addReasons(r.Reasoning,
				"General-purpose workloads — latest Graviton + Intel m-series",
				"arm64 (Graviton) included for ~20% better price/performance on Spot",
			)
		}

	case ModeHighPerformance:
		r.CapacityTypes = []string{"on-demand"}
		r.Architectures = []string{"amd64"}
		switch wtype {
		case kube.WorkloadGPU:
			r.InstanceFamilies = []string{"p4d", "p3", "g5", "g4dn"}
			r.Reasoning = addReasons(r.Reasoning,
				"GPU workloads detected — high-performance NVIDIA GPU families (p4d/p3/g5)",
				"On-demand only to guarantee availability and avoid interruption",
			)
		case kube.WorkloadMemory:
			r.InstanceFamilies = []string{"r7i", "r6i", "r5n", "x2idn"}
			r.Reasoning = addReasons(r.Reasoning,
				"Memory-intensive workloads — Intel memory-optimised (r7i/r6i/x2idn)",
				"On-demand ensures consistent availability for stateful/memory services",
			)
		case kube.WorkloadCPU:
			r.InstanceFamilies = []string{"c7i", "c6i", "c5n", "hpc7g"}
			if p.HasBatchJobs {
				r.InstanceFamilies = append(r.InstanceFamilies, "c6a")
			}
			r.Reasoning = addReasons(r.Reasoning,
				"Compute-intensive — latest Intel c7i/c6i compute-optimised",
				"c5n/hpc7g for network/HPC workloads if applicable",
			)
		default:
			r.InstanceFamilies = []string{"m7i", "c7i", "m6i", "c6i"}
			r.Reasoning = addReasons(r.Reasoning,
				"High-performance general: latest-gen Intel m7i/c7i on-demand",
				"No Spot to eliminate interruptions for latency-sensitive services",
			)
		}

	default: // balanced
		r.CapacityTypes = []string{"spot", "on-demand"}
		r.Architectures = []string{"arm64", "amd64"}
		switch wtype {
		case kube.WorkloadGPU:
			r.InstanceFamilies = []string{"g5", "g5g", "g4dn", "p3"}
			r.Reasoning = addReasons(r.Reasoning,
				"GPU workloads — balanced mix of GPU families, spot+on-demand",
			)
		case kube.WorkloadMemory:
			r.InstanceFamilies = []string{"r7g", "r7i", "r6g", "r6i", "m7g", "m7i"}
			r.Reasoning = addReasons(r.Reasoning,
				"Memory workloads — balanced mix of memory-optimised families",
			)
		case kube.WorkloadCPU:
			r.InstanceFamilies = []string{"c7g", "c7i", "m7g", "m7i", "c6g", "c6i"}
			r.Reasoning = addReasons(r.Reasoning,
				"Compute workloads — balanced compute + general families",
			)
		default:
			r.InstanceFamilies = []string{"m7g", "m7i", "c7g", "c7i", "m6g", "m6i"}
			r.Reasoning = addReasons(r.Reasoning,
				"Balanced: mixed Graviton + Intel latest-gen, Spot + on-demand",
			)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Azure AKS
// ─────────────────────────────────────────────────────────────────────────────

func buildAzure(r *Recommendation, _ *kube.WorkloadProfile, wtype kube.WorkloadType, mode OptimizationMode) {
	// Azure SKU families: D=general, F=compute, E=memory, N=GPU
	switch mode {
	case ModeCostOptimized:
		r.CapacityTypes = []string{"spot", "on-demand"}
		r.Architectures = []string{"amd64"}
		switch wtype {
		case kube.WorkloadGPU:
			r.InstanceFamilies = []string{"NC", "ND"}
			r.Reasoning = addReasons(r.Reasoning, "GPU workloads — NC/ND series Azure GPU VMs with Spot pricing")
		case kube.WorkloadMemory:
			r.InstanceFamilies = []string{"E", "M"}
			r.Reasoning = addReasons(r.Reasoning, "Memory workloads — E-series (memory-optimised) on Spot")
		case kube.WorkloadCPU:
			r.InstanceFamilies = []string{"F", "FX"}
			r.Reasoning = addReasons(r.Reasoning, "Compute workloads — F-series (compute-optimised) on Spot")
		default:
			r.InstanceFamilies = []string{"D", "Das", "Dads"}
			r.Reasoning = addReasons(r.Reasoning, "General: Dadsv5 (AMD) / Dasv5 — best $/vCPU on Azure Spot")
		}
	case ModeHighPerformance:
		r.CapacityTypes = []string{"on-demand"}
		r.Architectures = []string{"amd64"}
		switch wtype {
		case kube.WorkloadGPU:
			r.InstanceFamilies = []string{"NC", "NCv3", "ND", "NDv2"}
			r.Reasoning = addReasons(r.Reasoning, "GPU: high-end NC/ND series (V100/A100) on-demand")
		case kube.WorkloadMemory:
			r.InstanceFamilies = []string{"E", "M", "MediumMemory"}
			r.Reasoning = addReasons(r.Reasoning, "Memory: E-series + M-series (up to 4 TiB RAM) on-demand")
		case kube.WorkloadCPU:
			r.InstanceFamilies = []string{"Fx", "FX", "Fs"}
			r.Reasoning = addReasons(r.Reasoning, "Compute: Fx-series (latest Intel Sapphire Rapids) on-demand")
		default:
			r.InstanceFamilies = []string{"D", "Ds", "Dls"}
			r.Reasoning = addReasons(r.Reasoning, "High-perf general: Dv5-series (Intel) on-demand")
		}
	default: // balanced
		r.CapacityTypes = []string{"spot", "on-demand"}
		r.Architectures = []string{"amd64"}
		r.InstanceFamilies = []string{"D", "Das", "E", "F"}
		r.Reasoning = addReasons(r.Reasoning, "Balanced: D/E/F Azure families, Spot + on-demand")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GCP GKE
// ─────────────────────────────────────────────────────────────────────────────

func buildGCP(r *Recommendation, _ *kube.WorkloadProfile, wtype kube.WorkloadType, mode OptimizationMode) {
	// GCP machine families: n2/n2d=general, c2/c2d=compute, m2/m3=memory, a2/g2=GPU
	switch mode {
	case ModeCostOptimized:
		r.CapacityTypes = []string{"spot", "on-demand"}
		r.Architectures = []string{"amd64"}
		switch wtype {
		case kube.WorkloadGPU:
			r.InstanceFamilies = []string{"a2", "g2"}
			r.Reasoning = addReasons(r.Reasoning, "GPU: a2 (A100) / g2 (L4) with Spot pricing")
		case kube.WorkloadMemory:
			r.InstanceFamilies = []string{"n2d", "m3"}
			r.Reasoning = addReasons(r.Reasoning, "Memory: n2d (AMD, cheapest) + m3 for large memory needs")
		case kube.WorkloadCPU:
			r.InstanceFamilies = []string{"c2d", "n2d"}
			r.Reasoning = addReasons(r.Reasoning, "Compute: c2d (AMD EPYC) — best $/vCPU on GCP Spot")
		default:
			r.InstanceFamilies = []string{"n2d", "n2", "t2d"}
			r.Reasoning = addReasons(r.Reasoning, "General: n2d (AMD) + t2d (Arm) — lowest cost on Spot")
		}
	case ModeHighPerformance:
		r.CapacityTypes = []string{"on-demand"}
		r.Architectures = []string{"amd64"}
		switch wtype {
		case kube.WorkloadGPU:
			r.InstanceFamilies = []string{"a3", "a2"}
			r.Reasoning = addReasons(r.Reasoning, "GPU: a3 (H100) / a2 (A100) on-demand — highest throughput")
		case kube.WorkloadMemory:
			r.InstanceFamilies = []string{"m3", "m2"}
			r.Reasoning = addReasons(r.Reasoning, "Memory: m3 (Intel Sapphire Rapids) up to 30 TiB RAM")
		case kube.WorkloadCPU:
			r.InstanceFamilies = []string{"c3", "c2"}
			r.Reasoning = addReasons(r.Reasoning, "Compute: c3 (Intel Sapphire Rapids) on-demand")
		default:
			r.InstanceFamilies = []string{"n2", "c3", "n4"}
			r.Reasoning = addReasons(r.Reasoning, "High-perf general: n2/c3 Intel on-demand")
		}
	default: // balanced
		r.CapacityTypes = []string{"spot", "on-demand"}
		r.Architectures = []string{"amd64"}
		r.InstanceFamilies = []string{"n2", "n2d", "c2d"}
		r.Reasoning = addReasons(r.Reasoning, "Balanced: n2 (Intel) + n2d (AMD) + c2d, Spot + on-demand")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Sizing helpers
// ─────────────────────────────────────────────────────────────────────────────

// minCPU returns the smallest vCPU count a node must have to fit the largest
// pod's CPU request (with 20% overhead headroom).
func minCPU(maxPodCPUm int64) int {
	if maxPodCPUm == 0 {
		return 2
	}
	needed := int((float64(maxPodCPUm) / 1000.0) * 1.2) // 20% headroom
	switch {
	case needed <= 1:
		return 2
	case needed <= 2:
		return 2
	case needed <= 4:
		return 4
	case needed <= 8:
		return 8
	default:
		return 16
	}
}

// minMemMiB returns the minimum node memory in MiB needed to fit the largest
// pod's memory request (with 25% overhead headroom).
func minMemMiB(maxPodMemMiB int64) int {
	if maxPodMemMiB == 0 {
		return 2048
	}
	needed := int(float64(maxPodMemMiB) * 1.25)
	switch {
	case needed <= 1024:
		return 2048
	case needed <= 2048:
		return 2048
	case needed <= 4096:
		return 4096
	case needed <= 8192:
		return 8192
	case needed <= 16384:
		return 16384
	default:
		return 32768
	}
}

// cpuSizes returns the list of vCPU sizes Karpenter should consider,
// starting at minCPU and going up to 64.
func cpuSizes(minCPU int) []string {
	all := []string{"2", "4", "8", "16", "32", "48", "64"}
	var out []string
	for _, s := range all {
		var n int
		fmt.Sscanf(s, "%d", &n)
		if n >= minCPU {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{"16", "32", "64"}
	}
	return out
}

func addReasons(existing []string, more ...string) []string {
	return append(existing, more...)
}
