package plan

import (
	"testing"
)

// TestEstimatePerformanceWithPrefixCaching verifies that a non-zero
// PrefixCachingFactor raises the reported prefill throughput without affecting
// the decode rate (P7). Cache hits skip KV recompute at prefill, so the
// effective prefill throughput scales by 1/(1-factor).
func TestEstimatePerformanceWithPrefixCaching(t *testing.T) {
	model, _ := dpTestModel(32, 10)
	const bottleneck = 0.01 // 0.01 s/token
	const headroom = 8.0

	noCaching := Node{RAMAvailableGB: 16, UnifiedMemory: true, PrefixCachingFactor: 0}
	withCaching := Node{RAMAvailableGB: 16, UnifiedMemory: true, PrefixCachingFactor: 0.5}

	est0 := estimatePerformance(bottleneck, headroom, model, noCaching)
	est1 := estimatePerformance(bottleneck, headroom, model, withCaching)

	// Prefill throughput must be strictly higher with caching.
	if est1.PrefillTokSMin <= est0.PrefillTokSMin {
		t.Errorf("prefix_caching_factor=0.5 must increase prefill throughput: "+
			"got %.4f tok/s, want > %.4f tok/s", est1.PrefillTokSMin, est0.PrefillTokSMin)
	}

	// Decode throughput must be unaffected by prefix caching — it processes
	// one new token at a time regardless of prefix cache state.
	if est1.DecodeTokSMin != est0.DecodeTokSMin {
		t.Errorf("prefix caching must not change decode rate: "+
			"%.6f != %.6f", est1.DecodeTokSMin, est0.DecodeTokSMin)
	}

	// factor=0.5 → 1/(1-0.5) = 2.0× speedup on prefill. Check within 1% tolerance.
	const factor = 0.5
	wantRatio := 1.0 / (1 - factor) // 2.0
	gotRatio := est1.PrefillTokSMin / est0.PrefillTokSMin
	if gotRatio < wantRatio*0.99 || gotRatio > wantRatio*1.01 {
		t.Errorf("prefill throughput ratio = %.4f, want %.4f (1/(1-%.1f))",
			gotRatio, wantRatio, factor)
	}
}

// TestEstimatePerformanceWithKVSSD verifies that kv_ssd_offload=true with
// non-zero DiskFreeGB increases HeadroomGB in the PerfEstimate (P7). The
// contribution is capped at kvSsdOffloadMaxMultiplier × VRAM.
func TestEstimatePerformanceWithKVSSD(t *testing.T) {
	model, _ := dpTestModel(32, 10)
	const bottleneck = 0.01
	const headroom = 4.0
	const vram = 24.0

	base := Node{RAMAvailableGB: 32, VRAMGB: vram, MemBandwidthGBs: 400}
	withSSD := Node{
		RAMAvailableGB:  32,
		VRAMGB:          vram,
		MemBandwidthGBs: 400,
		KVSSDOffload:    true,
		DiskFreeGB:      200, // large SSD — will hit the cap
	}

	est0 := estimatePerformance(bottleneck, headroom, model, base)
	est1 := estimatePerformance(bottleneck, headroom, model, withSSD)

	// KV SSD offload must increase reported headroom (effectiveMemGB).
	if est1.HeadroomGB <= est0.HeadroomGB {
		t.Errorf("kv_ssd_offload=true must increase HeadroomGB: got %.2f, want > %.2f",
			est1.HeadroomGB, est0.HeadroomGB)
	}

	// The cap is kvSsdOffloadMaxMultiplier × VRAM = 2 × 24 = 48 GB of SSD contrib.
	// With diskFreeGB=200, raw contrib = kvSsdOffloadFactor×200 = 0.5×200 = 100 GB
	// but capped at 48. effectiveHeadroom = headroom(4) + 48 = 52 GB.
	maxAllowedHeadroom := headroom + kvSsdOffloadMaxMultiplier*vram
	if est1.HeadroomGB > maxAllowedHeadroom+0.01 {
		t.Errorf("KV SSD offload exceeded the cap: HeadroomGB=%.2f > max %.2f "+
			"(kvSsdOffloadMaxMultiplier=%.1f × VRAM=%.1f + base headroom=%.1f)",
			est1.HeadroomGB, maxAllowedHeadroom, kvSsdOffloadMaxMultiplier, vram, headroom)
	}

	// Verify decode/prefill rates are unaffected (KV SSD only changes memory).
	if est1.DecodeTokSMin != est0.DecodeTokSMin {
		t.Errorf("KV SSD offload must not change decode rate: %.6f != %.6f",
			est1.DecodeTokSMin, est0.DecodeTokSMin)
	}

	// A node with KVSSDOffload=false but same DiskFreeGB must get no headroom boost.
	noOffload := Node{RAMAvailableGB: 32, VRAMGB: vram, MemBandwidthGBs: 400,
		KVSSDOffload: false, DiskFreeGB: 200}
	estNo := estimatePerformance(bottleneck, headroom, model, noOffload)
	if estNo.HeadroomGB != est0.HeadroomGB {
		t.Errorf("KVSSDOffload=false must not change headroom: %.2f != %.2f",
			estNo.HeadroomGB, est0.HeadroomGB)
	}
}

// TestOrderingThresholdFromEnv verifies that PURSER_PLANNER_ORDERING_THRESHOLD
// is read by getEnvInt and that orderNodes handles fleets at and above the new
// threshold without crashing (N3).
func TestOrderingThresholdFromEnv(t *testing.T) {
	const newThreshold = 5
	t.Setenv("PURSER_PLANNER_ORDERING_THRESHOLD", "5")

	// Simulate a re-init: directly re-read the env var into the package var.
	prev := orderingThreshold
	orderingThreshold = getEnvInt("PURSER_PLANNER_ORDERING_THRESHOLD", HeldKarpMaxNodes)
	defer func() { orderingThreshold = prev }()

	if orderingThreshold != newThreshold {
		t.Fatalf("PURSER_PLANNER_ORDERING_THRESHOLD=5 → orderingThreshold=%d, want %d",
			orderingThreshold, newThreshold)
	}

	model, _ := dpTestModel(8, 32)

	// A fleet of exactly threshold=5 nodes: orderNodes must use Held-Karp (n <= threshold)
	// and return a valid permutation with the most-capable node first.
	nodes5 := []Node{
		node("A", 40, 300), node("B", 30, 100), node("C", 35, 150),
		node("D", 25, 200), node("E", 40, 180),
	}
	links5 := []Link{
		{From: "A", To: "B", RTTms: 2, BandwidthGBs: 10},
		{From: "B", To: "C", RTTms: 3, BandwidthGBs: 10},
		{From: "C", To: "D", RTTms: 1, BandwidthGBs: 10},
		{From: "D", To: "E", RTTms: 4, BandwidthGBs: 10},
		{From: "A", To: "C", RTTms: 5, BandwidthGBs: 10},
	}
	got5 := orderNodes(nodes5, model, links5, Constraints{})
	if !sameSet(got5, nodes5) {
		t.Fatalf("5-node orderNodes returned invalid permutation: %v", orderIDs(got5))
	}
	// A (usefulCapacity = 40×300 = 12000) is the most capable — must be host.
	if got5[0].ID != "A" {
		t.Errorf("5-node fleet host = %q, want A (most capable)", got5[0].ID)
	}

	// A fleet of threshold+1=6 nodes: orderNodes must switch to the heuristic
	// (n > threshold) and still return a valid permutation.
	nodes6 := append(nodes5, node("F", 38, 200))
	got6 := orderNodes(nodes6, model, links5, Constraints{})
	if !sameSet(got6, nodes6) {
		t.Fatalf("6-node orderNodes returned invalid permutation: %v", orderIDs(got6))
	}

	// Verify the threshold is still the value we set (not mutated by orderNodes).
	if orderingThreshold != newThreshold {
		t.Errorf("orderingThreshold must not be mutated by orderNodes: got %d, want %d",
			orderingThreshold, newThreshold)
	}
}
