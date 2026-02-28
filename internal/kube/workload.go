package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// WorkloadProfile summarises the resource demands of all running workloads.
type WorkloadProfile struct {
	TotalPods     int
	TotalCPUm     int64   // aggregate CPU requests in millicores
	TotalMemMiB   int64   // aggregate memory requests in MiB
	MaxPodCPUm    int64   // largest single-pod CPU request (millicores)
	MaxPodMemMiB  int64   // largest single-pod memory request (MiB)
	HasGPU        bool    // any container requests nvidia/amd/google GPU resources
	HasBatchJobs  bool    // Jobs or CronJobs detected in the cluster
	MemPerCPUGiB  float64 // average GiB of memory per CPU core across all pods
	Namespaces    int     // number of distinct namespaces that have running pods
	NoRequests    bool    // true when no resource requests are set (nothing to analyse)
}

// AnalyzeWorkloads connects to the cluster and returns a WorkloadProfile built
// from all currently running pods, Jobs, and CronJobs.
//
// If the cluster is unreachable or RBAC denies access, an error is returned
// and the caller should fall back to asking the user manually.
func AnalyzeWorkloads(kubeCtx string) (*WorkloadProfile, error) {
	overrides := &clientcmd.ConfigOverrides{}
	if kubeCtx != "" {
		overrides.CurrentContext = kubeCtx
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), overrides,
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}

	p := &WorkloadProfile{}
	nsSet := map[string]struct{}{}

	// ── Running pods ───────────────────────────────────────────────────────
	pods, err := cs.CoreV1().Pods().List(context.TODO(), metav1.ListOptions{
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	for _, pod := range pods.Items {
		nsSet[pod.Namespace] = struct{}{}
		p.TotalPods++

		var podCPUm, podMemMiB int64
		for _, c := range pod.Spec.Containers {
			if cpu := c.Resources.Requests.Cpu(); cpu != nil {
				podCPUm += cpu.MilliValue()
			}
			if mem := c.Resources.Requests.Memory(); mem != nil {
				podMemMiB += mem.Value() / (1024 * 1024)
			}
			for rname := range c.Resources.Requests {
				switch string(rname) {
				case "nvidia.com/gpu", "amd.com/gpu", "accelerator.google.com/gpu":
					p.HasGPU = true
				}
			}
		}

		p.TotalCPUm += podCPUm
		p.TotalMemMiB += podMemMiB
		if podCPUm > p.MaxPodCPUm {
			p.MaxPodCPUm = podCPUm
		}
		if podMemMiB > p.MaxPodMemMiB {
			p.MaxPodMemMiB = podMemMiB
		}
	}
	p.Namespaces = len(nsSet)

	// ── Batch jobs ─────────────────────────────────────────────────────────
	if jobs, err := cs.BatchV1().Jobs().List(context.TODO(), metav1.ListOptions{}); err == nil && len(jobs.Items) > 0 {
		p.HasBatchJobs = true
	}
	if crons, err := cs.BatchV1().CronJobs().List(context.TODO(), metav1.ListOptions{}); err == nil && len(crons.Items) > 0 {
		p.HasBatchJobs = true
	}

	// ── Derived ratios ─────────────────────────────────────────────────────
	if p.TotalCPUm > 0 {
		p.MemPerCPUGiB = (float64(p.TotalMemMiB) / 1024.0) / (float64(p.TotalCPUm) / 1000.0)
	}
	if p.TotalCPUm == 0 && p.TotalMemMiB == 0 {
		p.NoRequests = true
	}

	return p, nil
}

// WorkloadType classifies the dominant workload pattern inferred from a profile.
type WorkloadType string

const (
	WorkloadGeneral WorkloadType = "general"     // balanced CPU / memory
	WorkloadMemory  WorkloadType = "memory"      // memory-heavy (ratio > 4 GiB/core)
	WorkloadCPU     WorkloadType = "cpu"         // compute-heavy (ratio < 2 GiB/core)
	WorkloadGPU     WorkloadType = "gpu"         // GPU jobs detected
	WorkloadBatch   WorkloadType = "batch"       // batch / CronJob patterns
	WorkloadUnknown WorkloadType = "unknown"     // no requests set; cannot classify
)

// ClassifyWorkload infers a WorkloadType from a WorkloadProfile.
func ClassifyWorkload(p *WorkloadProfile) WorkloadType {
	if p.HasGPU {
		return WorkloadGPU
	}
	if p.NoRequests || p.TotalPods == 0 {
		return WorkloadUnknown
	}
	if p.MemPerCPUGiB > 4.0 {
		return WorkloadMemory
	}
	if p.MemPerCPUGiB < 2.0 && p.MemPerCPUGiB > 0 {
		return WorkloadCPU
	}
	if p.HasBatchJobs {
		return WorkloadBatch
	}
	return WorkloadGeneral
}
