package nodes

import (
	"fmt"
	"math"

	"github.com/kemilad/karpx/internal/kube"
)

// CostEstimate holds approximate monthly cost projections for a node recommendation.
// All prices are USD, based on AWS us-east-1 on-demand rates.
// Actual costs vary by region, Spot market conditions, and AWS pricing changes.
type CostEstimate struct {
	EstimatedNodes     int
	PrimaryType        string  // e.g. "m7i.xlarge"
	VCPUs              int     // vCPU count of primary type
	MemGiB             float64 // memory of primary type in GiB
	OnDemandPerNodeHr  float64 // on-demand $/hr per node
	OnDemandMonthlyUSD float64 // estimated total monthly cost at on-demand
	SpotPerNodeHr      float64 // typical Spot $/hr per node
	SpotMonthlyUSD     float64 // estimated total monthly cost at Spot
	SpotSavingsPct     int     // % savings vs on-demand when using Spot
	HasSpot            bool    // whether the recommendation uses Spot instances
	Note               string  // disclaimer / region note
}

// instanceSpec holds representative pricing for one instance type.
type instanceSpec struct {
	vCPU      int
	memGiB    float64
	priceHr   float64 // on-demand $/hr, approximate us-east-1
	spotRatio float64 // typical Spot price as fraction of on-demand
}

// instancePricing maps "family:size" to approximate on-demand pricing.
// Prices sourced from AWS us-east-1 public pricing; updated as of early 2025.
var instancePricing = map[string]instanceSpec{
	// General purpose - Intel (m7i)
	"m7i:large":   {2, 8, 0.1008, 0.32},
	"m7i:xlarge":  {4, 16, 0.2016, 0.32},
	"m7i:2xlarge": {8, 32, 0.4032, 0.32},
	"m7i:4xlarge": {16, 64, 0.8064, 0.32},
	"m7i:8xlarge": {32, 128, 1.6128, 0.32},

	// General purpose - Intel prev gen (m6i)
	"m6i:large":   {2, 8, 0.096, 0.30},
	"m6i:xlarge":  {4, 16, 0.192, 0.30},
	"m6i:2xlarge": {8, 32, 0.384, 0.30},
	"m6i:4xlarge": {16, 64, 0.768, 0.30},

	// General purpose - AMD (m6a)
	"m6a:large":   {2, 8, 0.0864, 0.30},
	"m6a:xlarge":  {4, 16, 0.1728, 0.30},
	"m6a:2xlarge": {8, 32, 0.3456, 0.30},

	// General purpose - Graviton (m7g)
	"m7g:large":   {2, 8, 0.0816, 0.35},
	"m7g:xlarge":  {4, 16, 0.1632, 0.35},
	"m7g:2xlarge": {8, 32, 0.3264, 0.35},
	"m7g:4xlarge": {16, 64, 0.6528, 0.35},
	"m7g:8xlarge": {32, 128, 1.3056, 0.35},

	// General purpose - Graviton prev gen (m6g)
	"m6g:large":   {2, 8, 0.077, 0.35},
	"m6g:xlarge":  {4, 16, 0.154, 0.35},
	"m6g:2xlarge": {8, 32, 0.308, 0.35},

	// Flexible - Intel (m7i-flex / c7i-flex)
	"m7i-flex:large":   {2, 8, 0.0477, 0.55},
	"m7i-flex:xlarge":  {4, 16, 0.0953, 0.55},
	"m7i-flex:2xlarge": {8, 32, 0.1907, 0.55},
	"c7i-flex:large":   {2, 4, 0.0383, 0.55},
	"c7i-flex:xlarge":  {4, 8, 0.0765, 0.55},
	"c7i-flex:2xlarge": {8, 16, 0.1530, 0.55},

	// Compute optimized - Intel (c7i)
	"c7i:large":   {2, 4, 0.0850, 0.32},
	"c7i:xlarge":  {4, 8, 0.1700, 0.32},
	"c7i:2xlarge": {8, 16, 0.3400, 0.32},
	"c7i:4xlarge": {16, 32, 0.6800, 0.32},

	// Compute optimized - Intel prev gen (c6i)
	"c6i:large":   {2, 4, 0.0850, 0.30},
	"c6i:xlarge":  {4, 8, 0.1700, 0.30},
	"c6i:2xlarge": {8, 16, 0.3400, 0.30},

	// Compute optimized - AMD (c6a)
	"c6a:large":   {2, 4, 0.0765, 0.30},
	"c6a:xlarge":  {4, 8, 0.1530, 0.30},
	"c6a:2xlarge": {8, 16, 0.3060, 0.30},

	// Compute optimized - Graviton (c7g)
	"c7g:large":   {2, 4, 0.0725, 0.35},
	"c7g:xlarge":  {4, 8, 0.1450, 0.35},
	"c7g:2xlarge": {8, 16, 0.2900, 0.35},
	"c7g:4xlarge": {16, 32, 0.5800, 0.35},

	// Compute optimized - Graviton prev gen (c6g)
	"c6g:large":   {2, 4, 0.068, 0.35},
	"c6g:xlarge":  {4, 8, 0.136, 0.35},
	"c6g:2xlarge": {8, 16, 0.272, 0.35},

	// Memory optimized - Intel (r7i)
	"r7i:large":   {2, 16, 0.1260, 0.35},
	"r7i:xlarge":  {4, 32, 0.2520, 0.35},
	"r7i:2xlarge": {8, 64, 0.5040, 0.35},
	"r7i:4xlarge": {16, 128, 1.0080, 0.35},

	// Memory optimized - Intel prev gen (r6i)
	"r6i:large":   {2, 16, 0.126, 0.32},
	"r6i:xlarge":  {4, 32, 0.252, 0.32},
	"r6i:2xlarge": {8, 64, 0.504, 0.32},

	// Memory optimized - Graviton (r7g)
	"r7g:large":   {2, 16, 0.1008, 0.35},
	"r7g:xlarge":  {4, 32, 0.2016, 0.35},
	"r7g:2xlarge": {8, 64, 0.4032, 0.35},

	// Memory optimized - Graviton prev gen (r6g)
	"r6g:large":   {2, 16, 0.1008, 0.35},
	"r6g:xlarge":  {4, 32, 0.2016, 0.35},

	// Burstable / free-tier eligible (t3 / t3a / t4g)
	"t3:micro":   {2, 1, 0.0104, 0.65},
	"t3:small":   {2, 2, 0.0208, 0.65},
	"t3:medium":  {2, 4, 0.0416, 0.65},
	"t3:large":   {2, 8, 0.0832, 0.65},
	"t3:xlarge":  {4, 16, 0.1664, 0.65},
	"t3a:medium": {2, 4, 0.0376, 0.65},
	"t3a:large":  {2, 8, 0.0752, 0.65},
	"t4g:medium": {2, 4, 0.0336, 0.65},
	"t4g:large":  {2, 8, 0.0672, 0.65},
	"t4g:xlarge": {4, 16, 0.1344, 0.65},

	// GPU (g4dn, g5, g5g, p3)
	"g4dn:xlarge":  {4, 16, 0.526, 0.30},
	"g4dn:2xlarge": {8, 32, 0.752, 0.30},
	"g5:xlarge":    {4, 16, 1.006, 0.28},
	"g5:2xlarge":   {8, 32, 1.212, 0.28},
	"g5g:xlarge":   {4, 8, 0.556, 0.28},
	"g5g:2xlarge":  {8, 16, 0.722, 0.28},
	"p3:2xlarge":   {8, 61, 3.06, 0.30},
	"p3:8xlarge":   {32, 244, 12.24, 0.30},
}

// sizeSuffix maps a vCPU count to its canonical AWS size suffix.
var sizeSuffix = map[int]string{
	2:  "large",
	4:  "xlarge",
	8:  "2xlarge",
	16: "4xlarge",
	32: "8xlarge",
}

// EstimateCost returns an approximate cost projection for an AWS recommendation.
// For Azure and GCP, the Note field explains that pricing is AWS-only for now.
// MinCPUFromProfile returns the minimum vCPU count derived from a workload profile.
// Exported so the HTTP handler can build a minimal Recommendation for cost-only calls.
func MinCPUFromProfile(profile *kube.WorkloadProfile) int {
	if profile == nil {
		return 2
	}
	return minCPU(profile.MaxPodCPUm)
}

func EstimateCost(rec Recommendation, profile *kube.WorkloadProfile) CostEstimate {
	if rec.Provider != kube.ProviderAWS {
		return CostEstimate{Note: "Cost estimation currently available for AWS only"}
	}
	if len(rec.InstanceFamilies) == 0 {
		return CostEstimate{Note: "No instance families in recommendation"}
	}

	family := rec.InstanceFamilies[0]
	minVCPU := rec.MinNodeCPU
	if minVCPU == 0 {
		minVCPU = 2
	}

	spec, typeName := pickSpec(family, minVCPU)

	// Estimate node count: ceil(totalCPU / vCPU per node) with 20% headroom.
	totalCPU := float64(profile.TotalCPUm) / 1000.0
	if totalCPU < 0.5 {
		totalCPU = 2.0 // floor so the estimate is always meaningful
	}
	estimatedNodes := int(math.Ceil(totalCPU / float64(spec.vCPU) * 1.2))
	if estimatedNodes < 1 {
		estimatedNodes = 1
	}

	const hoursPerMonth = 730.0

	odPerNode := spec.priceHr
	odMonthly := odPerNode * float64(estimatedNodes) * hoursPerMonth
	spotPerNode := spec.priceHr * spec.spotRatio
	spotMonthly := spotPerNode * float64(estimatedNodes) * hoursPerMonth
	savingsPct := int(math.Round((1 - spec.spotRatio) * 100))

	hasSpot := false
	for _, ct := range rec.CapacityTypes {
		if ct == "spot" {
			hasSpot = true
			break
		}
	}

	return CostEstimate{
		EstimatedNodes:     estimatedNodes,
		PrimaryType:        typeName,
		VCPUs:              spec.vCPU,
		MemGiB:             spec.memGiB,
		OnDemandPerNodeHr:  odPerNode,
		OnDemandMonthlyUSD: odMonthly,
		SpotPerNodeHr:      spotPerNode,
		SpotMonthlyUSD:     spotMonthly,
		SpotSavingsPct:     savingsPct,
		HasSpot:            hasSpot,
		Note:               "Approximate us-east-1 on-demand pricing. Actual costs vary by region and Spot availability.",
	}
}

// pickSpec selects the best matching instanceSpec for the given family and min vCPU.
func pickSpec(family string, minVCPU int) (instanceSpec, string) {
	// Round up to next standard vCPU tier.
	targetVCPU := 2
	for _, s := range []int{2, 4, 8, 16, 32} {
		if s >= minVCPU {
			targetVCPU = s
			break
		}
	}

	suffix, ok := sizeSuffix[targetVCPU]
	if !ok {
		suffix = "xlarge"
		targetVCPU = 4
	}

	key := fmt.Sprintf("%s:%s", family, suffix)
	if spec, found := instancePricing[key]; found {
		return spec, fmt.Sprintf("%s.%s", family, suffix)
	}

	// Fallback: use m7i as a reference price for unknown families.
	fallbackKey := fmt.Sprintf("m7i:%s", suffix)
	if spec, found := instancePricing[fallbackKey]; found {
		return spec, fmt.Sprintf("%s.%s", family, suffix)
	}

	// Last resort: generic estimate based on vCPU count.
	priceHr := 0.05 * float64(targetVCPU/2)
	return instanceSpec{targetVCPU, 8, priceHr, 0.35}, fmt.Sprintf("%s.%s", family, suffix)
}
