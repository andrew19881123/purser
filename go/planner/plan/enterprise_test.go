package plan

import (
	"math"
	"testing"
)

// This file is the correctness gate for the four algorithm fixes introduced in
// v0.3 (G1–G4). Each test targets exactly one fix so a regression is
// immediately attributable to the right change.

// TestLinkIndexLookupIsO1 verifies G4 (buildLinkIndex / O(1) lookup):
//   - Every link present in the fleet is found by idx.lookup.
//   - A non-existent pair returns nil (no false positives).
//
// It uses the buildFleet fixture from bench_test.go (same package) which
// produces a bidirectional chain of NVLink-class links.
func TestLinkIndexLookupIsO1(t *testing.T) {
	nodes, links := buildFleet(10)
	idx := buildLinkIndex(links)

	// Every link present in the slice must be findable via O(1) lookup.
	for _, l := range links {
		found := idx.lookup(l.From, l.To)
		if found == nil {
			t.Fatalf("idx.lookup(%q, %q) = nil, want non-nil link", l.From, l.To)
		}
		if found.BandwidthGBs != l.BandwidthGBs {
			t.Errorf("idx.lookup(%q, %q).BandwidthGBs = %.1f, want %.1f",
				l.From, l.To, found.BandwidthGBs, l.BandwidthGBs)
		}
		if found.RTTms != l.RTTms {
			t.Errorf("idx.lookup(%q, %q).RTTms = %.1f, want %.1f",
				l.From, l.To, found.RTTms, l.RTTms)
		}
	}

	// A non-existent directed pair must return nil.
	if got := idx.lookup("non-existent-1", "non-existent-2"); got != nil {
		t.Errorf("idx.lookup(non-existent pair) = %+v, want nil", got)
	}

	// Spot-check: the reverse direction of a one-way link also resolves if it
	// was explicitly registered (buildFleet adds bidirectional links).
	if len(nodes) >= 2 {
		a, b := nodes[0].ID, nodes[1].ID
		if idx.lookup(a, b) == nil || idx.lookup(b, a) == nil {
			t.Errorf("bidirectional links between %q and %q: one direction missing", a, b)
		}
	}
}

// TestEffectiveBandwidthWithSSDOffload verifies G3 (SSD bandwidth penalty):
//
//	A node with 10 GB VRAM and SSD offload, serving a 12 GB weight + 5 GB KV
//	shard (total 17 GB > 10 GB VRAM), must report a lower effective bandwidth
//	than an equivalent node with 20 GB VRAM (no overflow to SSD).
//
// The effective bandwidth must lie between the NVMe floor (kvSsdReadBandwidthGBs)
// and the HBM ceiling (2000 GB/s).
func TestEffectiveBandwidthWithSSDOffload(t *testing.T) {
	nodeWithSSD := Node{
		ID:              "ssd-node",
		VRAMGB:          10,
		KVSSDOffload:    true,
		MemBandwidthGBs: 2000, // HBM-class bandwidth
	}
	nodeNoSSD := Node{
		ID:              "vram-node",
		VRAMGB:          20, // 12+5 = 17 GB fits fully in VRAM
		KVSSDOffload:    false,
		MemBandwidthGBs: 2000,
	}

	// 12 GB weights + 5 GB KV: 7 GB overflow → 5 GB spilled to SSD (cap: shardKVBytes).
	bwSSD := effectiveBandwidth(nodeWithSSD, 12e9, 5e9)
	bwNoSSD := effectiveBandwidth(nodeNoSSD, 12e9, 5e9)

	if bwSSD >= bwNoSSD {
		t.Errorf("SSD offload node bw (%.2f GB/s) must be < pure-VRAM node bw (%.2f GB/s)",
			bwSSD, bwNoSSD)
	}
	if bwSSD <= kvSsdReadBandwidthGBs-0.1 {
		t.Errorf("effective bw (%.2f GB/s) must be > NVMe floor (%.2f GB/s)",
			bwSSD, kvSsdReadBandwidthGBs)
	}
	if bwSSD >= 2000.0 {
		t.Errorf("effective bw (%.2f GB/s) must be < VRAM ceiling (2000 GB/s)", bwSSD)
	}

	// When the shard fits fully in VRAM (no overflow), bandwidth should be unpenalised.
	bwFits := effectiveBandwidth(nodeWithSSD, 4e9, 3e9) // 7 GB total < 10 GB VRAM
	if bwFits != 2000.0 {
		t.Errorf("shard that fits in VRAM: effective bw = %.2f, want 2000 (no penalty)", bwFits)
	}

	// SSD offload disabled → no penalty regardless of footprint.
	nodeNoSSDLargeFootprint := Node{
		ID: "nossd-node", VRAMGB: 5, KVSSDOffload: false, MemBandwidthGBs: 2000,
	}
	bwNoOffload := effectiveBandwidth(nodeNoSSDLargeFootprint, 12e9, 5e9)
	if bwNoOffload != 2000.0 {
		t.Errorf("KVSSDOffload=false: effective bw = %.2f, want 2000 (no penalty)", bwNoOffload)
	}
}

// TestKVCacheMLACompressionRatio verifies G1 (KVCompressionRatio field):
//
//	A DeepSeek-V3-class model with KVCompressionRatio=0.0625 must yield a
//	smaller KV cache estimate than the same model relying on the AttentionMLA
//	default of 0.10. The custom ratio is ~0.0625 / 0.10 = 62.5% of the default,
//	so the custom estimate must be < 80% of the default.
func TestKVCacheMLACompressionRatio(t *testing.T) {
	// DeepSeek-V3 geometry: 61 layers, 128 KV heads, head_dim=128.
	modelCustomRatio := ModelSpec{
		Layers:             61,
		NKVHeads:           128,
		HeadDim:            128,
		AttentionType:      AttentionMLA,
		KVCompressionRatio: 0.0625, // 1024 / (128×128) ≈ 0.0625
		ContextMax:         32768,
	}
	modelDefaultRatio := modelCustomRatio
	modelDefaultRatio.KVCompressionRatio = 0 // use AttentionMLA default (0.10)

	kvCustom := estimateKVCache(modelCustomRatio, 32768)
	kvDefault := estimateKVCache(modelDefaultRatio, 32768)

	if kvCustom <= 0 {
		t.Fatalf("custom ratio KV estimate must be positive, got %.6f", kvCustom)
	}
	if kvDefault <= 0 {
		t.Fatalf("default ratio KV estimate must be positive, got %.6f", kvDefault)
	}
	if kvCustom >= kvDefault*0.8 {
		t.Errorf("custom MLA ratio 0.0625 must produce KV < 80%% of default 0.10: custom=%.4f default=%.4f",
			kvCustom, kvDefault)
	}

	// Ratio of estimates should match ratio of factors (0.0625 / 0.10 = 0.625).
	expectedRatio := 0.0625 / 0.10
	gotRatio := kvCustom / kvDefault
	if math.Abs(gotRatio-expectedRatio) > 1e-6 {
		t.Errorf("kv ratio = %.6f, want %.6f (custom/default factor ratio)", gotRatio, expectedRatio)
	}

	// A zero KVCompressionRatio must produce the same result as the default.
	modelZeroRatio := modelCustomRatio
	modelZeroRatio.KVCompressionRatio = 0
	kvZero := estimateKVCache(modelZeroRatio, 32768)
	if math.Abs(kvZero-kvDefault) > 1e-10 {
		t.Errorf("KVCompressionRatio=0 must use AttentionType default: got %.6f, want %.6f", kvZero, kvDefault)
	}

	// An out-of-range ratio (> 1) must fall back to the AttentionType default.
	modelOutOfRange := modelCustomRatio
	modelOutOfRange.KVCompressionRatio = 1.5
	kvOOR := estimateKVCache(modelOutOfRange, 32768)
	if math.Abs(kvOOR-kvDefault) > 1e-10 {
		t.Errorf("KVCompressionRatio=1.5 (> 1) must fall back to default: got %.6f, want %.6f", kvOOR, kvDefault)
	}
}

// TestKVCacheInMemoryWithPrefixCaching verifies G2 (prefix caching fit check):
//
//	kvCacheInMemory with a 50%% prefix caching factor must return ≈ 50%% of the
//	full KV estimate (within 5%% tolerance). With factor=0 (no caching) the
//	result must equal the full estimate exactly.
func TestKVCacheInMemoryWithPrefixCaching(t *testing.T) {
	model := bench7BModel()
	fullKV := estimateKVCache(model, model.ContextMax)

	if fullKV <= 0 {
		t.Fatalf("full KV estimate must be positive, got %.6f", fullKV)
	}

	// 50%% prefix caching: only half the tokens need KV materialisation.
	kvHalf := kvCacheInMemory(model, model.ContextMax, 0.5)
	wantHalf := fullKV * 0.5
	if math.Abs(kvHalf-wantHalf) > fullKV*0.05 {
		t.Errorf("50%%%% prefix caching: in-memory KV = %.6f, want %.6f ± 5%%%%", kvHalf, wantHalf)
	}

	// No prefix caching (factor = 0): result must equal the full estimate exactly.
	kvFull := kvCacheInMemory(model, model.ContextMax, 0.0)
	if math.Abs(kvFull-fullKV) > 1e-10 {
		t.Errorf("prefix caching factor 0: in-memory KV = %.6f, want %.6f (full)", kvFull, fullKV)
	}

	// Factor >= 1 is clamped: return the full estimate (guard against sign inversion).
	kvClamped := kvCacheInMemory(model, model.ContextMax, 1.0)
	if math.Abs(kvClamped-fullKV) > 1e-10 {
		t.Errorf("prefix caching factor 1.0: in-memory KV = %.6f, want %.6f (full)", kvClamped, fullKV)
	}

	// Factor 0.9: only 10%% of KV resident.
	kvNinety := kvCacheInMemory(model, model.ContextMax, 0.9)
	wantTen := fullKV * 0.1
	if math.Abs(kvNinety-wantTen) > fullKV*0.05 {
		t.Errorf("90%%%% prefix caching: in-memory KV = %.6f, want %.6f ± 5%%%%", kvNinety, wantTen)
	}
}
