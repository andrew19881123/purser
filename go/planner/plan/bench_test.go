package plan

import (
	"context"
	"fmt"
	"testing"
)

// Micro-benchmark suite for Plan() across fleet sizes and model classes.
//
// Node spec used throughout (A6000-class GPU node):
//   - VRAM:          24 GB  (discrete GPU — usefulMemory = min(RAM, VRAM) = 24 GB)
//   - RAM available: 56 GB  (64 GB total − 8 GB OS reservation)
//   - Mem bandwidth: 200 GB/s
//
// Model sizing aligns with CALIBRATABLE constants in plan.go:
//   - The memory-bandwidth-utilisation factor (memBandwidthUtilFraction=0.70) and
//     per-node overhead (overheadOSRuntimeGB=2.0) are the dominant knobs for
//     the timing estimates; the benchmarks exercise those code paths.
//
// Phase-B candidateSubsets derivation for the three fit scenarios
// (usefulMemory per node = 24 GB, overhead = 2 GB/node):
//
//	Small  (bench7BModel  q4= 7 GB + KV≈0.3 GB): ramNeeded=7.3  → k_min=1 (fits on 1 node)
//	Medium (bench100BModel q4=50 GB + KV≈2.7 GB): ramNeeded=52.7 → k_min=3
//	  k=2: 2×24=48 < 52.7+4=56.7 ✗; k=3: 3×24=72 ≥ 52.7+6=58.7 ✓
//	Large  (bench80BModel  q8=160 GB + KV≈5.4 GB): ramNeeded=165.4 → k_min=8
//	  k=7: 7×24=168 < 165.4+14=179.4 ✗; k=8: 8×24=192 ≥ 165.4+16=181.4 ✓

// ---- fixture constructors -----------------------------------------------

// benchNode returns a realistic discrete-GPU node (A6000-class).
func benchNode(id string) Node {
	return Node{
		ID:              id,
		RAMTotalGB:      64,
		RAMAvailableGB:  56,
		VRAMGB:          24,
		MemBandwidthGBs: 200,
	}
}

// buildFleet constructs N benchmark nodes with sequential IDs and a chain
// of bidirectional links (25 GB/s, 2 ms RTT — a typical NVLink/InfiniBand hop).
func buildFleet(n int) ([]Node, []Link) {
	nodes := make([]Node, n)
	ids := make([]string, n)
	for i := range nodes {
		ids[i] = fmt.Sprintf("node-%03d", i)
		nodes[i] = benchNode(ids[i])
	}
	links := make([]Link, 0, (n-1)*2)
	for i := 0; i+1 < n; i++ {
		links = append(links,
			Link{From: ids[i], To: ids[i+1], RTTms: 2.0, BandwidthGBs: 25.0},
			Link{From: ids[i+1], To: ids[i], RTTms: 2.0, BandwidthGBs: 25.0},
		)
	}
	return nodes, links
}

// bench7BModel: Llama-7B class. q4=7 GB fits on a single 24 GB VRAM node.
func bench7BModel() ModelSpec {
	return ModelSpec{
		ID:            "bench/llama-7b",
		Layers:        32,
		ParamsTotalB:  7,
		ParamsActiveB: 7,
		HiddenSize:    4096,
		NKVHeads:      8,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    8192,
		Quantizations: []Quantization{
			{Name: "q8", SizeGB: 14, Quality: 0.99},
			{Name: "q4", SizeGB: 7, Quality: 0.90},
		},
	}
}

// bench100BModel: ~100 B class model. q4=50 GB needs k_min=3 on 24 GB VRAM nodes.
func bench100BModel() ModelSpec {
	return ModelSpec{
		ID:            "bench/100b",
		Layers:        80,
		ParamsTotalB:  100,
		ParamsActiveB: 100,
		HiddenSize:    8192,
		NKVHeads:      8,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    8192,
		Quantizations: []Quantization{
			{Name: "q8", SizeGB: 100, Quality: 0.99},
			{Name: "q4", SizeGB: 50, Quality: 0.90},
		},
	}
}

// bench80BModel: 80 B model. q8=160 GB needs k_min=8 on 24 GB VRAM nodes.
func bench80BModel() ModelSpec {
	return ModelSpec{
		ID:            "bench/80b",
		Layers:        80,
		ParamsTotalB:  80,
		ParamsActiveB: 80,
		HiddenSize:    8192,
		NKVHeads:      16,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    8192,
		Quantizations: []Quantization{
			{Name: "q8", SizeGB: 160, Quality: 0.99},
			{Name: "q4", SizeGB: 80, Quality: 0.90},
		},
	}
}

// benchCatalog returns 10 models spanning a range of sizes and architectures
// for the FitAll scenario.
func benchCatalog() []ModelSpec {
	return []ModelSpec{
		// 3B tiny dense
		{
			ID: "bench/3b", Layers: 24, ParamsTotalB: 3, ParamsActiveB: 3,
			HiddenSize: 2048, NKVHeads: 8, HeadDim: 128,
			AttentionType: AttentionGQA, ContextMax: 8192,
			Quantizations: []Quantization{
				{Name: "q8", SizeGB: 6, Quality: 0.99},
				{Name: "q4", SizeGB: 3, Quality: 0.90},
			},
		},
		bench7BModel(),
		// 13B dense
		{
			ID: "bench/13b", Layers: 40, ParamsTotalB: 13, ParamsActiveB: 13,
			HiddenSize: 5120, NKVHeads: 8, HeadDim: 128,
			AttentionType: AttentionGQA, ContextMax: 8192,
			Quantizations: []Quantization{
				{Name: "q8", SizeGB: 26, Quality: 0.99},
				{Name: "q4", SizeGB: 13, Quality: 0.90},
			},
		},
		// 30B dense
		{
			ID: "bench/30b", Layers: 60, ParamsTotalB: 30, ParamsActiveB: 30,
			HiddenSize: 6144, NKVHeads: 8, HeadDim: 128,
			AttentionType: AttentionGQA, ContextMax: 8192,
			Quantizations: []Quantization{
				{Name: "q8", SizeGB: 60, Quality: 0.99},
				{Name: "q4", SizeGB: 30, Quality: 0.90},
			},
		},
		// 34B MoE (small active fraction → cheap KV)
		{
			ID: "bench/34b-moe", Layers: 48, ParamsTotalB: 34, ParamsActiveB: 4,
			IsMoE: true, HiddenSize: 4096, NKVHeads: 8, HeadDim: 128,
			AttentionType: AttentionGQA, ContextMax: 8192,
			Quantizations: []Quantization{
				{Name: "q8", SizeGB: 68, Quality: 0.99},
				{Name: "q4", SizeGB: 34, Quality: 0.90},
			},
		},
		// 70B with speculative draft
		{
			ID: "bench/70b-draft", Layers: 80, ParamsTotalB: 70, ParamsActiveB: 70,
			HiddenSize: 8192, NKVHeads: 8, HeadDim: 128,
			AttentionType: AttentionGQA, ContextMax: 8192,
			Draft: DraftInfo{Available: true, Type: "mtp", TailLayers: 4},
			Quantizations: []Quantization{
				{Name: "q8", SizeGB: 140, Quality: 0.99},
				{Name: "q4", SizeGB: 70, Quality: 0.90},
			},
		},
		bench100BModel(),
		// 120B MLA (latent KV compression ×10)
		{
			ID: "bench/120b-mla", Layers: 96, ParamsTotalB: 120, ParamsActiveB: 120,
			HiddenSize: 8192, NKVHeads: 16, HeadDim: 128,
			AttentionType: AttentionMLA, ContextMax: 16384,
			Quantizations: []Quantization{
				{Name: "q8", SizeGB: 240, Quality: 0.99},
				{Name: "q4", SizeGB: 120, Quality: 0.90},
			},
		},
		bench80BModel(),
		// 70B linear attention (near-constant KV state)
		{
			ID: "bench/70b-linear", Layers: 80, ParamsTotalB: 70, ParamsActiveB: 70,
			HiddenSize: 8192, NKVHeads: 8, HeadDim: 128,
			AttentionType: AttentionLinear, ContextMax: 32768,
			Quantizations: []Quantization{
				{Name: "q8", SizeGB: 140, Quality: 0.99},
				{Name: "q4", SizeGB: 70, Quality: 0.90},
			},
		},
	}
}

// ---- benchmarks ---------------------------------------------------------

// BenchmarkPlanSmallFleet: 4 nodes, 7B model fits on 1 (phase B rule G3).
func BenchmarkPlanSmallFleet(b *testing.B) {
	nodes, links := buildFleet(4)
	model := bench7BModel()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Plan(context.Background(), nodes, links, model, Constraints{})
	}
}

// BenchmarkPlanMediumFleet: 20 nodes, 100B model q4 splits across 3 (k_min=3).
func BenchmarkPlanMediumFleet(b *testing.B) {
	nodes, links := buildFleet(20)
	model := bench100BModel()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Plan(context.Background(), nodes, links, model, Constraints{})
	}
}

// BenchmarkPlanLargeFleet: 100 nodes, 80B model q8 splits across 8 (k_min=8).
func BenchmarkPlanLargeFleet(b *testing.B) {
	nodes, links := buildFleet(100)
	model := bench80BModel()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Plan(context.Background(), nodes, links, model, Constraints{})
	}
}

// BenchmarkPlanNoFit: model (q4=250 GB) too large for the 4-node fleet.
// Exercises the early-exit path in phase A / candidateSubsets.
func BenchmarkPlanNoFit(b *testing.B) {
	nodes, links := buildFleet(4)
	// 4 nodes × 56 GB RAM = 224 GB aggregate RAM; 250 GB + KV + overhead > 224 GB.
	model := ModelSpec{
		ID:            "bench/unfit-500b",
		Layers:        126,
		ParamsTotalB:  500,
		ParamsActiveB: 500,
		HiddenSize:    16384,
		NKVHeads:      16,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    8192,
		Quantizations: []Quantization{
			{Name: "q4", SizeGB: 250, Quality: 0.90},
		},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Plan(context.Background(), nodes, links, model, Constraints{})
	}
}

// BenchmarkPlanForceNodeCount: ForceNodeCount=1, 4-node fleet, 7B model.
// Exercises the operator-constraint path + validatePlanMemory backstop.
func BenchmarkPlanForceNodeCount(b *testing.B) {
	nodes, links := buildFleet(4)
	model := bench7BModel()
	one := 1
	c := Constraints{ForceNodeCount: &one}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Plan(context.Background(), nodes, links, model, c)
	}
}

// BenchmarkFitAll: Plan() for each of 10 catalog models against a 20-node fleet.
// Exercises the full phase A–F pipeline across a spread of model sizes and
// architecture types (dense, MoE, MLA, linear attention, draft).
func BenchmarkFitAll(b *testing.B) {
	nodes, links := buildFleet(20)
	catalog := benchCatalog()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, m := range catalog {
			_, _ = Plan(context.Background(), nodes, links, m, Constraints{})
		}
	}
}

// ---- smoke test ---------------------------------------------------------

// TestBenchmarkScenariosAreRealistic calls Plan() directly with the same
// fixtures as the benchmarks and asserts that feasible scenarios return a
// valid, non-nil plan. This makes the benchmark fixtures double as regression
// tests and ensures the node/model specs are internally consistent.
func TestBenchmarkScenariosAreRealistic(t *testing.T) {
	t.Run("SmallFleet/fits-on-1", func(t *testing.T) {
		nodes, links := buildFleet(4)
		dp, err := Plan(context.Background(), nodes, links, bench7BModel(), Constraints{})
		if err != nil {
			t.Fatalf("small fleet: unexpected error: %v", err)
		}
		if len(dp.Assignments) != 1 {
			t.Errorf("small fleet: expected single-node plan, got %d assignments", len(dp.Assignments))
		}
		if dp.Estimated.DecodeTokSMin <= 0 {
			t.Errorf("small fleet: decode estimate must be positive, got %.3f", dp.Estimated.DecodeTokSMin)
		}
	})

	t.Run("MediumFleet/splits-across-3", func(t *testing.T) {
		nodes, links := buildFleet(20)
		dp, err := Plan(context.Background(), nodes, links, bench100BModel(), Constraints{})
		if err != nil {
			t.Fatalf("medium fleet: unexpected error: %v", err)
		}
		if len(dp.Assignments) < 3 {
			t.Errorf("medium fleet: expected >= 3-node plan, got %d assignments", len(dp.Assignments))
		}
		checkContiguousCover(t, dp.Assignments, bench100BModel().Layers)
	})

	t.Run("LargeFleet/splits-across-8", func(t *testing.T) {
		nodes, links := buildFleet(100)
		dp, err := Plan(context.Background(), nodes, links, bench80BModel(), Constraints{})
		if err != nil {
			t.Fatalf("large fleet: unexpected error: %v", err)
		}
		if len(dp.Assignments) < 8 {
			t.Errorf("large fleet: expected >= 8-node plan, got %d assignments", len(dp.Assignments))
		}
		checkContiguousCover(t, dp.Assignments, bench80BModel().Layers)
	})

	t.Run("NoFit/returns-PlanError", func(t *testing.T) {
		nodes, links := buildFleet(4)
		model := ModelSpec{
			ID:            "bench/unfit-500b",
			Layers:        126,
			ParamsTotalB:  500,
			ParamsActiveB: 500,
			HiddenSize:    16384,
			NKVHeads:      16,
			HeadDim:       128,
			AttentionType: AttentionGQA,
			ContextMax:    8192,
			Quantizations: []Quantization{
				{Name: "q4", SizeGB: 250, Quality: 0.90},
			},
		}
		dp, err := Plan(context.Background(), nodes, links, model, Constraints{})
		if dp != nil {
			t.Fatalf("no-fit: expected nil plan, got %+v", dp)
		}
		if err == nil {
			t.Fatal("no-fit: expected a *PlanError, got nil")
		}
		pe, ok := err.(*PlanError)
		if !ok {
			t.Fatalf("no-fit: expected *PlanError, got %T: %v", err, err)
		}
		if pe.DeficitGB <= 0 {
			t.Errorf("no-fit: expected positive deficit, got %.2f", pe.DeficitGB)
		}
	})

	t.Run("ForceNodeCount/1-valid", func(t *testing.T) {
		nodes, links := buildFleet(4)
		one := 1
		dp, err := Plan(context.Background(), nodes, links, bench7BModel(), Constraints{ForceNodeCount: &one})
		if err != nil {
			t.Fatalf("force-count=1: unexpected error: %v", err)
		}
		if len(dp.Assignments) != 1 {
			t.Errorf("force-count=1: expected 1 assignment, got %d", len(dp.Assignments))
		}
	})

	t.Run("FitAll/catalog-runs-cleanly", func(t *testing.T) {
		nodes, links := buildFleet(20)
		catalog := benchCatalog()
		feasible := 0
		for _, m := range catalog {
			dp, err := Plan(context.Background(), nodes, links, m, Constraints{})
			if err == nil {
				feasible++
				if dp == nil {
					t.Errorf("model %q: Plan returned nil plan with nil error", m.ID)
				}
			} else {
				if _, ok := err.(*PlanError); !ok {
					t.Errorf("model %q: non-plan error: %v", m.ID, err)
				}
			}
		}
		// At least half the catalog must be feasible on the 20-node fleet.
		if feasible < len(catalog)/2 {
			t.Errorf("FitAll: only %d/%d catalog models produced a plan; check fleet/model sizing", feasible, len(catalog))
		}
	})
}
