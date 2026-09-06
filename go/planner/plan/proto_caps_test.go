package plan

// Tests for I4 (prefix_caching_factor) and I6 (kv_ssd_offload): proto → Node
// wiring and the downstream effect on estimatePerformance.

import (
	"testing"

	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// ---------------------------------------------------------------------------
// Proto ↔ Node wiring (NodeFromHardwareProfile)
// ---------------------------------------------------------------------------

// TestPrefixCachingFactorFromProto verifies that a HardwareProfile with
// prefix_caching_factor = 0.5 is faithfully converted into Node.PrefixCachingFactor.
func TestPrefixCachingFactorFromProto(t *testing.T) {
	profile := &purserv1.HardwareProfile{
		NodeId:              "n1",
		RamTotalGb:          64,
		RamAvailableGb:      48,
		PrefixCachingFactor: 0.5,
	}
	n := NodeFromHardwareProfile(profile)
	if n.PrefixCachingFactor != 0.5 {
		t.Errorf("PrefixCachingFactor = %v, want 0.5", n.PrefixCachingFactor)
	}
}

// TestKVSSDOffloadFromProto verifies that a HardwareProfile with
// kv_ssd_offload = true is faithfully converted into Node.KVSSDOffload.
func TestKVSSDOffloadFromProto(t *testing.T) {
	profile := &purserv1.HardwareProfile{
		NodeId:         "n1",
		RamTotalGb:     64,
		RamAvailableGb: 48,
		KvSsdOffload:   true,
	}
	n := NodeFromHardwareProfile(profile)
	if !n.KVSSDOffload {
		t.Errorf("KVSSDOffload = false, want true")
	}
}

// TestPrefixCachingFactorZeroDefault verifies the zero value is preserved
// (no factor assumed when the agent does not report one).
func TestPrefixCachingFactorZeroDefault(t *testing.T) {
	profile := &purserv1.HardwareProfile{
		NodeId:         "n1",
		RamAvailableGb: 48,
		// PrefixCachingFactor intentionally absent → 0
	}
	n := NodeFromHardwareProfile(profile)
	if n.PrefixCachingFactor != 0 {
		t.Errorf("expected zero PrefixCachingFactor by default, got %v", n.PrefixCachingFactor)
	}
}

// ---------------------------------------------------------------------------
// estimatePerformance: capability effect
// ---------------------------------------------------------------------------

// baseNode returns a minimal node for performance estimation tests.
func baseNode() Node {
	return Node{
		ID:              "bench-node",
		RAMTotalGB:      64,
		RAMAvailableGB:  48,
		VRAMGB:          24,
		MemBandwidthGBs: 400,
	}
}

// baseModel returns a minimal model spec for performance estimation tests.
func baseModel() ModelSpec {
	return ModelSpec{
		ID:            "acme/perf-test",
		Layers:        4,
		ParamsTotalB:  7,
		ParamsActiveB: 7,
		HiddenSize:    4096,
		NKVHeads:      8,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    8192,
		Quantizations: []Quantization{
			{Name: "q4", SizeGB: 4, Quality: 0.90},
		},
	}
}

// TestEstimateWithPrefixCaching verifies that a node reporting PrefixCachingFactor 0.5
// shows a strictly higher effective prefill throughput than an identical node
// with factor 0 (no prefix caching). Decode rate must be unchanged — prefix
// caching affects prefill only.
func TestEstimateWithPrefixCaching(t *testing.T) {
	model := baseModel()
	quant := model.Quantizations[0]
	headroom := 4.0

	// Compute a bottleneck that gives a non-zero rate.
	bottleneck := computeTime(baseNode(), model, quant, 0, model.Layers)
	if bottleneck <= 0 {
		t.Fatal("bottleneck must be positive for this test to be meaningful")
	}

	noCaching := baseNode() // PrefixCachingFactor defaults to 0
	withCaching := baseNode()
	withCaching.PrefixCachingFactor = 0.5

	estNo := estimatePerformance(bottleneck, headroom, model, noCaching)
	estWith := estimatePerformance(bottleneck, headroom, model, withCaching)

	if estWith.PrefillTokSMin <= estNo.PrefillTokSMin {
		t.Errorf(
			"prefix caching should raise PrefillTokSMin: with=%.2f no=%.2f",
			estWith.PrefillTokSMin, estNo.PrefillTokSMin,
		)
	}
	if estWith.PrefillTokSMax <= estNo.PrefillTokSMax {
		t.Errorf(
			"prefix caching should raise PrefillTokSMax: with=%.2f no=%.2f",
			estWith.PrefillTokSMax, estNo.PrefillTokSMax,
		)
	}
	// Decode rate must be identical — prefix caching does not affect decode.
	if estWith.DecodeTokSMin != estNo.DecodeTokSMin || estWith.DecodeTokSMax != estNo.DecodeTokSMax {
		t.Errorf(
			"prefix caching must not change decode range: with=[%.2f,%.2f] no=[%.2f,%.2f]",
			estWith.DecodeTokSMin, estWith.DecodeTokSMax,
			estNo.DecodeTokSMin, estNo.DecodeTokSMax,
		)
	}
}

// TestEstimateWithKVSSD verifies that a node with KVSSDOffload=true and
// non-zero DiskFreeGB shows a strictly higher HeadroomGB than the same node
// without SSD offload.
func TestEstimateWithKVSSD(t *testing.T) {
	model := baseModel()
	quant := model.Quantizations[0]
	headroom := 4.0

	bottleneck := computeTime(baseNode(), model, quant, 0, model.Layers)

	noSSD := baseNode()
	withSSD := baseNode()
	withSSD.KVSSDOffload = true
	withSSD.DiskFreeGB = 100 // 100 GB free SSD → kvSsdOffloadFactor × 100 added

	estNo := estimatePerformance(bottleneck, headroom, model, noSSD)
	estWith := estimatePerformance(bottleneck, headroom, model, withSSD)

	if estWith.HeadroomGB <= estNo.HeadroomGB {
		t.Errorf(
			"KV SSD offload should raise HeadroomGB: with=%.2f no=%.2f",
			estWith.HeadroomGB, estNo.HeadroomGB,
		)
	}
}

// TestEstimateWithKVSSD_NoDisk verifies that KVSSDOffload=true with zero
// DiskFreeGB does NOT change the headroom (no disk, no contribution).
func TestEstimateWithKVSSD_NoDisk(t *testing.T) {
	model := baseModel()
	quant := model.Quantizations[0]
	headroom := 4.0
	bottleneck := computeTime(baseNode(), model, quant, 0, model.Layers)

	noSSD := baseNode() // KVSSDOffload=false, DiskFreeGB=0
	withSSDNoDisk := baseNode()
	withSSDNoDisk.KVSSDOffload = true
	withSSDNoDisk.DiskFreeGB = 0 // offload enabled but no disk

	estNo := estimatePerformance(bottleneck, headroom, model, noSSD)
	estWith := estimatePerformance(bottleneck, headroom, model, withSSDNoDisk)

	if estWith.HeadroomGB != estNo.HeadroomGB {
		t.Errorf(
			"KV SSD offload with zero disk should not change headroom: with=%.2f no=%.2f",
			estWith.HeadroomGB, estNo.HeadroomGB,
		)
	}
}
