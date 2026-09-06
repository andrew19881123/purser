package plan

import (
	"context"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// This file is the correctness gate for phase C (design 08 §6): it proves that
// the throughput-aware DP (dpPartition) finds the SAME minimal bottleneck as an
// exhaustive brute-force reference over all contiguous partitions, and checks
// the multi-node invariants of the assembled Plan().
//
// The brute force below is an INDEPENDENT enumeration (all subsets of internal
// cut positions), but it scores each candidate with the exact same stageTimeAt
// the DP uses (task requirement), so the comparison isolates the DP's search
// logic — recurrence + reconstruction + the memory-pruning `break` — from the
// stage-time model itself.

const dpEps = 1e-9

// bruteForcePartition is the reference partitioner. For the fixed node `order`
// it enumerates EVERY way to cut L layers into m contiguous, non-empty segments
// assigned to the first m nodes (a prefix), for every m in 1..len(order). It
// scores each partition's bottleneck as max over stages of stageTimeAt (the
// same function the DP calls) and returns the minimum feasible bottleneck, or
// +Inf if no partition is feasible under the memory/headroom constraint.
//
// Enumeration is over the 2^(L-1) subsets of internal cut positions {1..L-1};
// a subset of size m-1 defines a partition into m segments. With L <= 16 this
// is at most 32768 masks — cheap and exhaustive.
func bruteForcePartition(order []Node, links []Link, model ModelSpec, quant Quantization, context int) float64 {
	L := model.Layers
	M := len(order)
	best := math.Inf(1)
	if L <= 0 || M <= 0 {
		return best
	}

	for mask := 0; mask < (1 << uint(L-1)); mask++ {
		// Build segment boundaries 0 = b[0] < b[1] < ... < b[m] = L.
		bounds := []int{0}
		for p := 1; p <= L-1; p++ {
			if mask&(1<<uint(p-1)) != 0 {
				bounds = append(bounds, p)
			}
		}
		bounds = append(bounds, L)

		m := len(bounds) - 1 // number of segments == number of prefix nodes used
		if m > M {
			continue // not enough nodes for this many segments
		}

		bottleneck := 0.0
		feasible := true
		for k := 1; k <= m; k++ {
			i, j := bounds[k-1], bounds[k]
			st := stageTimeAt(order, k, i, j, links, model, quant, context)
			if math.IsInf(st, 1) {
				feasible = false
				break
			}
			if st > bottleneck {
				bottleneck = st
			}
		}
		if feasible && bottleneck < best {
			best = bottleneck
		}
	}
	return best
}

// dpTestModel builds a dense test model with a single quantization. Per-layer
// weight = sizeGB/layers; KV cache is deliberately tiny (small context) so the
// memory constraint is dominated by the weights, which makes the feasibility
// boundary easy to reason about in the fleet comments below.
func dpTestModel(layers int, sizeGB float64) (ModelSpec, Quantization) {
	q := Quantization{Name: "q", SizeGB: sizeGB, Quality: 0.9}
	m := ModelSpec{
		ID:            "dp/test",
		Layers:        layers,
		ParamsTotalB:  10,
		ParamsActiveB: 10,
		HiddenSize:    4096,
		NKVHeads:      8,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    2048,
		Quantizations: []Quantization{q},
	}
	return m, q
}

func node(id string, ramGB, bwGBs float64) Node {
	return Node{ID: id, RAMAvailableGB: ramGB, UnifiedMemory: true, MemBandwidthGBs: bwGBs}
}

// TestDPPartition_MatchesBruteForce is the gate: on a spread of small, fully
// deterministic heterogeneous fleets — including memory-constrained ones and a
// wholly-infeasible one — the DP's bottleneck must equal the brute-force
// optimum, and the two must agree on (in)feasibility.
func TestDPPartition_MatchesBruteForce(t *testing.T) {
	type fleet struct {
		name           string
		order          []Node
		links          []Link
		layers         int
		sizeGB         float64
		wantFeasible   bool
		wantUsedAtMost int // 0 = don't check; else DP must use <= this many nodes
	}

	fleets := []fleet{
		{
			// 2 equal nodes, no memory pressure, but a very expensive hop
			// (RTT 160 ms). Single-node compute is 0.16 s/token; the cheapest
			// 2-way split still pays the 0.16 s hop on stage 2, so it can only
			// tie-or-lose. The DP must therefore choose the 1-node prefix
			// (min over node-count), exercising the "use fewer nodes" path.
			name:           "2-node-costly-hop-prefers-single",
			order:          []Node{node("A", 40, 100), node("B", 40, 100)},
			links:          []Link{{From: "A", To: "B", RTTms: 160, BandwidthGBs: 1000}},
			layers:         8,
			sizeGB:         16,
			wantFeasible:   true,
			wantUsedAtMost: 1,
		},
		{
			// 2 heterogeneous nodes, no memory pressure, comm on the hop. Both
			// slow-ish; a balanced split can beat the single node. Pure min-max.
			name:         "2-node-balanced-split",
			order:        []Node{node("A", 40, 90), node("B", 40, 110)},
			links:        []Link{{From: "A", To: "B", RTTms: 2, BandwidthGBs: 20}},
			layers:       10,
			sizeGB:       50,
			wantFeasible: true,
		},
		{
			// Tight memory: node A holds <= 6 layers, node B <= 3 (weights
			// 5 GB/layer, headroom 10%). 8 layers => only A in {5,6} feasible.
			name:         "2-node-tight-memory",
			order:        []Node{node("A", 40, 150), node("B", 20, 150)},
			links:        []Link{{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12}},
			layers:       8,
			sizeGB:       40,
			wantFeasible: true,
		},
		{
			// Total infeasibility: two nodes cap at 3 layers each (6 < 8).
			name:         "2-node-infeasible",
			order:        []Node{node("A", 20, 150), node("B", 20, 150)},
			links:        []Link{{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12}},
			layers:       8,
			sizeGB:       40,
			wantFeasible: false,
		},
		{
			// 3 heterogeneous nodes, moderate memory, comm on both hops.
			name: "3-node-heterogeneous",
			order: []Node{
				node("A", 24, 200), node("B", 20, 120), node("C", 18, 80),
			},
			links: []Link{
				{From: "A", To: "B", RTTms: 4, BandwidthGBs: 10},
				{From: "B", To: "C", RTTms: 6, BandwidthGBs: 8},
			},
			layers:       9,
			sizeGB:       30,
			wantFeasible: true,
		},
		{
			// 4 nodes, staggered memory caps (6/5/4/2 layers). Many partitions
			// are memory-infeasible; the DP must find the best feasible one.
			name: "4-node-staggered-caps",
			order: []Node{
				node("A", 30, 180), node("B", 25, 140),
				node("C", 20, 100), node("D", 15, 60),
			},
			links: []Link{
				{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12},
				{From: "B", To: "C", RTTms: 4, BandwidthGBs: 10},
				{From: "C", To: "D", RTTms: 5, BandwidthGBs: 8},
			},
			layers:       12,
			sizeGB:       48,
			wantFeasible: true,
		},
		{
			// 5 nodes, L=16, generous memory, varied bandwidth + RTT. Largest
			// cross-check (2^15 brute-force masks).
			name: "5-node-large-16-layers",
			order: []Node{
				node("A", 30, 250), node("B", 30, 130), node("C", 30, 200),
				node("D", 30, 90), node("E", 30, 160),
			},
			links: []Link{
				{From: "A", To: "B", RTTms: 2, BandwidthGBs: 25},
				{From: "B", To: "C", RTTms: 3, BandwidthGBs: 20},
				{From: "C", To: "D", RTTms: 7, BandwidthGBs: 6},
				{From: "D", To: "E", RTTms: 1, BandwidthGBs: 30},
			},
			layers:       16,
			sizeGB:       32,
			wantFeasible: true,
		},
		{
			// No links at all: commTime is zero everywhere, pure compute min-max.
			name:         "3-node-no-links",
			order:        []Node{node("A", 30, 200), node("B", 30, 100), node("C", 30, 150)},
			links:        nil,
			layers:       7,
			sizeGB:       21,
			wantFeasible: true,
		},
		{
			// Single node, exact fit boundary (weights 3 GB/layer, 5 layers,
			// useful 20 => memNeed 17 <= 18). Degenerate M=1 partition.
			name:           "single-node-exact",
			order:          []Node{node("A", 20, 120)},
			links:          nil,
			layers:         5,
			sizeGB:         15,
			wantFeasible:   true,
			wantUsedAtMost: 1,
		},
	}

	for _, f := range fleets {
		f := f
		t.Run(f.name, func(t *testing.T) {
			model, quant := dpTestModel(f.layers, f.sizeGB)
			ctx := model.ContextMax

			assignments, dpBottleneck := dpPartition(f.order, f.links, model, quant, ctx)
			bf := bruteForcePartition(f.order, f.links, model, quant, ctx)

			if !f.wantFeasible {
				if !math.IsInf(bf, 1) {
					t.Fatalf("fleet says infeasible but brute-force found bottleneck %.6f", bf)
				}
				if assignments != nil {
					t.Fatalf("DP returned a partition %+v but brute-force proves infeasibility", assignments)
				}
				if !math.IsInf(dpBottleneck, 1) {
					t.Fatalf("DP bottleneck = %.6f, want +Inf on an infeasible fleet", dpBottleneck)
				}
				return
			}

			if math.IsInf(bf, 1) {
				t.Fatalf("fleet expected feasible but brute-force found no feasible partition")
			}
			if assignments == nil {
				t.Fatalf("DP returned INFEASIBLE (nil) but brute-force found bottleneck %.6f", bf)
			}
			if math.Abs(dpBottleneck-bf) > dpEps {
				t.Fatalf("DP bottleneck %.9f != brute-force optimum %.9f (delta %.3g)",
					dpBottleneck, bf, dpBottleneck-bf)
			}

			// The reconstructed assignments must themselves realise dpBottleneck
			// and be a valid contiguous cover — guards reconstructCuts, not just
			// the dp[][] scalar.
			checkContiguousCover(t, assignments, f.layers)
			if got := recomputeBottleneck(f.order, assignments, f.links, model, quant, ctx); math.Abs(got-dpBottleneck) > dpEps {
				t.Fatalf("reconstructed partition bottleneck %.9f != reported %.9f", got, dpBottleneck)
			}
			if f.wantUsedAtMost > 0 && len(assignments) > f.wantUsedAtMost {
				t.Fatalf("DP used %d nodes, want <= %d", len(assignments), f.wantUsedAtMost)
			}
		})
	}
}

// TestDPPartition_MatchesBruteForce_Randomized broadens the gate: a fixed-seed
// sweep of hundreds of small heterogeneous fleets (random layer counts, node
// memory, bandwidth, and links, including memory-infeasible ones). For every
// one, the DP's bottleneck must equal the brute-force optimum and agree on
// feasibility. The seed is fixed, so the run is fully deterministic.
func TestDPPartition_MatchesBruteForce_Randomized(t *testing.T) {
	rng := rand.New(rand.NewSource(0x9E3779B9))
	const iterations = 500

	feasibleSeen, infeasibleSeen := 0, 0
	for it := 0; it < iterations; it++ {
		layers := 1 + rng.Intn(12) // 1..12
		m := 1 + rng.Intn(5)       // 1..5 nodes

		order := make([]Node, m)
		for k := 0; k < m; k++ {
			// RAM 6..40 GB (sometimes tight vs weights), bandwidth 40..300 GB/s.
			ram := 6.0 + float64(rng.Intn(35))
			bw := 40.0 + float64(rng.Intn(261))
			order[k] = node(string(rune('A'+k)), ram, bw)
		}

		var links []Link
		for k := 0; k+1 < m; k++ {
			// ~30% of hops carry an explicit (sometimes costly) link.
			if rng.Intn(10) < 3 {
				links = append(links, Link{
					From:         order[k].ID,
					To:           order[k+1].ID,
					RTTms:        float64(rng.Intn(200)),
					BandwidthGBs: 1.0 + float64(rng.Intn(50)),
				})
			}
		}

		sizeGB := 8.0 + float64(rng.Intn(73)) // 8..80 GB of weights
		model, quant := dpTestModel(layers, sizeGB)
		ctx := model.ContextMax

		assignments, dpBottleneck := dpPartition(order, links, model, quant, ctx)
		bf := bruteForcePartition(order, links, model, quant, ctx)

		if math.IsInf(bf, 1) {
			infeasibleSeen++
			if assignments != nil {
				t.Fatalf("iter %d: DP found %+v but brute-force proves infeasibility (L=%d, size=%.0f, order=%+v)",
					it, assignments, layers, sizeGB, order)
			}
			if !math.IsInf(dpBottleneck, 1) {
				t.Fatalf("iter %d: DP bottleneck=%.6f, want +Inf (infeasible)", it, dpBottleneck)
			}
			continue
		}

		feasibleSeen++
		if assignments == nil {
			t.Fatalf("iter %d: DP returned INFEASIBLE but brute-force optimum=%.6f (L=%d, size=%.0f, order=%+v)",
				it, bf, layers, sizeGB, order)
		}
		if math.Abs(dpBottleneck-bf) > dpEps {
			t.Fatalf("iter %d: DP=%.9f != brute-force=%.9f (delta %.3g; L=%d, size=%.0f, order=%+v, links=%+v)",
				it, dpBottleneck, bf, dpBottleneck-bf, layers, sizeGB, order, links)
		}
		checkContiguousCover(t, assignments, layers)
		if got := recomputeBottleneck(order, assignments, links, model, quant, ctx); math.Abs(got-dpBottleneck) > dpEps {
			t.Fatalf("iter %d: reconstructed bottleneck %.9f != reported %.9f", it, got, dpBottleneck)
		}
	}

	// Sanity: the sweep must actually exercise both branches, or it proves
	// nothing about the memory-pruning / infeasibility path.
	if feasibleSeen == 0 || infeasibleSeen == 0 {
		t.Fatalf("degenerate sweep: feasible=%d infeasible=%d (want both > 0)", feasibleSeen, infeasibleSeen)
	}
	t.Logf("randomized sweep: %d feasible, %d infeasible fleets all matched brute-force", feasibleSeen, infeasibleSeen)
}

// recomputeBottleneck scores an assignment list exactly as the pipeline sees
// it: stage k receives its activation from stage k-1 along `order`. Assignments
// from the DP follow `order`, so index k in the slice is stage k.
func recomputeBottleneck(order []Node, assignments []Assignment, links []Link, model ModelSpec, quant Quantization, context int) float64 {
	max := 0.0
	for idx, a := range assignments {
		st := stageTimeAt(order, idx+1, a.LayerStart, a.LayerEnd+1, links, model, quant, context)
		if st > max {
			max = st
		}
	}
	return max
}

// checkContiguousCover asserts the assignments are contiguous, non-overlapping,
// gap-free shards covering exactly [0, layers).
func checkContiguousCover(t *testing.T, assignments []Assignment, layers int) {
	t.Helper()
	if len(assignments) == 0 {
		t.Fatal("no assignments")
	}
	sorted := append([]Assignment(nil), assignments...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LayerStart < sorted[j].LayerStart })

	if sorted[0].LayerStart != 0 {
		t.Errorf("first shard starts at %d, want 0", sorted[0].LayerStart)
	}
	if last := sorted[len(sorted)-1].LayerEnd; last != layers-1 {
		t.Errorf("last shard ends at %d, want %d", last, layers-1)
	}
	for i, a := range sorted {
		if a.LayerEnd < a.LayerStart {
			t.Errorf("shard %d is empty/inverted: [%d,%d]", i, a.LayerStart, a.LayerEnd)
		}
		if i > 0 {
			prevEnd := sorted[i-1].LayerEnd
			if a.LayerStart != prevEnd+1 {
				t.Errorf("gap/overlap between shard %d (ends %d) and %d (starts %d)",
					i-1, prevEnd, i, a.LayerStart)
			}
		}
	}
}

// TestPlan_MultiNodeInvariants checks the assembled multi-node plan invariants
// (task 4) on fleets that force k_min >= 2: memory respected with HEADROOM on
// every node, contiguous shards covering [0, Layers), PipelineOrder length ==
// #assignments, and exactly one HOST. (Ordering optimality is phase D — not
// tested here.)
func TestPlan_MultiNodeInvariants(t *testing.T) {
	type fleet struct {
		name    string
		nodes   []Node
		links   []Link
		layers  int
		sizeGB  float64
		wantMin int // plan must span at least this many nodes
	}

	fleets := []fleet{
		{
			// 40 GB weights over two 30 GB nodes: cannot fit on one (k_min = 2).
			name:    "two-node-split",
			nodes:   []Node{node("A", 30, 150), node("B", 30, 120)},
			links:   []Link{{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12}},
			layers:  8,
			sizeGB:  40,
			wantMin: 2,
		},
		{
			// 54 GB weights over three 25 GB nodes: needs all three (k_min = 3).
			name: "three-node-split",
			nodes: []Node{
				node("A", 25, 200), node("B", 25, 130), node("C", 25, 160),
			},
			links: []Link{
				{From: "A", To: "B", RTTms: 4, BandwidthGBs: 10},
				{From: "B", To: "C", RTTms: 5, BandwidthGBs: 9},
			},
			layers:  12,
			sizeGB:  54,
			wantMin: 3,
		},
	}

	for _, f := range fleets {
		f := f
		t.Run(f.name, func(t *testing.T) {
			model, quant := dpTestModel(f.layers, f.sizeGB)

			dp, err := Plan(context.Background(), f.nodes, f.links, model, Constraints{})
			if err != nil {
				t.Fatalf("expected a plan, got error: %v", err)
			}
			if len(dp.Assignments) < f.wantMin {
				t.Fatalf("expected >= %d assignments (multi-node), got %d", f.wantMin, len(dp.Assignments))
			}

			nodeByID := make(map[string]Node, len(f.nodes))
			for _, n := range f.nodes {
				nodeByID[n.ID] = n
			}

			// (a) memory respected with HEADROOM on every node.
			for _, a := range dp.Assignments {
				n, ok := nodeByID[a.NodeID]
				if !ok {
					t.Fatalf("assignment references unknown node %q", a.NodeID)
				}
				if !stageFits(n, model, quant, a.LayerStart, a.LayerEnd+1, model.ContextMax) {
					need := stageMemNeed(model, quant, a.LayerStart, a.LayerEnd+1, model.ContextMax)
					t.Errorf("node %q shard [%d,%d] needs %.2f GB, exceeds %.2f GB usable-with-headroom",
						a.NodeID, a.LayerStart, a.LayerEnd, need, usefulMemory(n)*(1-defaultHeadroomFraction))
				}
			}

			// (b) contiguous shards covering [0, Layers), no gaps/overlaps.
			checkContiguousCover(t, dp.Assignments, f.layers)

			// (c) PipelineOrder length == #assignments.
			if len(dp.PipelineOrder) != len(dp.Assignments) {
				t.Errorf("PipelineOrder len %d != assignments %d", len(dp.PipelineOrder), len(dp.Assignments))
			}

			// (d) exactly one HOST.
			hosts := 0
			for _, a := range dp.Assignments {
				if a.Role == RoleHost {
					hosts++
				}
			}
			if hosts != 1 {
				t.Errorf("expected exactly 1 HOST, got %d", hosts)
			}

			// The reported headroom must be strictly positive (HEADROOM enforced).
			if dp.Estimated.HeadroomGB <= 0 {
				t.Errorf("expected positive tightest headroom, got %.3f", dp.Estimated.HeadroomGB)
			}
		})
	}
}
