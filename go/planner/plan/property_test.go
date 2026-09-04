package plan

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"testing/quick"
)

// This file is the property-based gate for the whole planner. Using the stdlib
// testing/quick (no new dependency), it generates thousands of random but VALID
// fleets/models and asserts the structural INVARIANTS of Plan() on each one:
//
//	(1) every assignment fits its node's useful memory (multi-node stages fit
//	    WITH the HEADROOM margin the DP enforces) and reported headroom >= 0;
//	(2) the plan uses no more than k_min+DELTA nodes;
//	(3) ForceHost, when set, lands the forced node at the pipeline head;
//	(4) the shards are a contiguous cover of [0, Layers);
//	(5) there is exactly one HOST;
//	(6) with a draft model, exactly one assignment carries the draft and it is
//	    the pipeline tail (phase E).
//
// If a random input is INFEASIBLE, Plan must return a *PlanError (checked
// instead of the plan invariants).

// planScenario is a single generated planner input. It implements
// quick.Generator so testing/quick can synthesise valid fleets/models.
type planScenario struct {
	nodes []Node
	links []Link
	model ModelSpec
	c     Constraints
}

// Generate builds a random, structurally-valid planner input. Sizes are chosen
// so the sweep hits BOTH feasible and infeasible fleets (asserted non-degenerate
// by the caller).
func (planScenario) Generate(rng *rand.Rand, _ int) reflect.Value {
	nNodes := 1 + rng.Intn(5) // 1..5 nodes
	nodes := make([]Node, nNodes)
	for i := range nodes {
		nodes[i] = Node{
			ID:              fmt.Sprintf("n%d", i),
			RAMAvailableGB:  8.0 + float64(rng.Intn(56)), // 8..63 GB
			UnifiedMemory:   true,
			MemBandwidthGBs: 50.0 + float64(rng.Intn(400)), // 50..449 GB/s
		}
	}

	// Sparse random directed links (some hops unmeasured on purpose).
	var links []Link
	for i := 0; i < nNodes; i++ {
		for j := 0; j < nNodes; j++ {
			if i != j && rng.Intn(3) == 0 {
				links = append(links, Link{
					From:         nodes[i].ID,
					To:           nodes[j].ID,
					RTTms:        float64(rng.Intn(60)),
					BandwidthGBs: 1.0 + float64(rng.Intn(40)),
				})
			}
		}
	}

	layers := 1 + rng.Intn(16) // 1..16 layers
	big := 8.0 + float64(rng.Intn(120))
	quants := []Quantization{
		{Name: "q8", SizeGB: big, Quality: 0.99},
		{Name: "q4", SizeGB: big / 2, Quality: 0.90},
		{Name: "q2", SizeGB: big / 4, Quality: 0.75},
	}
	model := ModelSpec{
		ID:            "prop/model",
		Layers:        layers,
		ParamsTotalB:  10,
		ParamsActiveB: 10,
		HiddenSize:    4096,
		NKVHeads:      8,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    2048,
		Quantizations: quants,
	}
	if rng.Intn(2) == 0 { // half the time an MoE with a small active fraction
		model.IsMoE = true
		model.ParamsActiveB = 2
	}
	if rng.Intn(3) == 0 { // sometimes ship a speculative draft
		model.Draft = DraftInfo{Available: true, Type: "mtp", TailLayers: 1 + rng.Intn(layers)}
	}

	var c Constraints
	if rng.Intn(3) == 0 {
		// Force the most-capable node as host: it is always ranked first, so it
		// is present in every candidate subset — the forced-host invariant is
		// then well-defined for both single- and multi-node outcomes.
		host := bestNodeByCapacity(nodes).ID
		c.ForceHost = &host
	}

	return reflect.ValueOf(planScenario{nodes: nodes, links: links, model: model, c: c})
}

// TestPlan_PropertyInvariants runs the randomized invariant sweep.
func TestPlan_PropertyInvariants(t *testing.T) {
	var feasible, infeasible int
	f := func(s planScenario) bool {
		dp, err := Plan(s.nodes, s.links, s.model, s.c)
		if err != nil {
			infeasible++
			// Infeasible input → must be a sensible *PlanError with a reason.
			var pe *PlanError
			if !errors.As(err, &pe) || pe.Reason == "" {
				return false
			}
			return true
		}
		feasible++
		return checkPlanInvariants(t, s, dp)
	}

	cfg := &quick.Config{MaxCount: 2000, Rand: rand.New(rand.NewSource(0xC0FFEE))}
	if err := quick.Check(f, cfg); err != nil {
		t.Fatal(err)
	}
	// The sweep must actually exercise both branches or it proves little.
	if feasible == 0 || infeasible == 0 {
		t.Fatalf("degenerate property sweep: feasible=%d infeasible=%d (want both > 0)", feasible, infeasible)
	}
	t.Logf("property sweep: %d feasible, %d infeasible scenarios all held the invariants", feasible, infeasible)
}

// checkPlanInvariants asserts invariants (1)–(6) on a produced plan. It logs the
// first violated invariant (with the scenario) and returns false so quick.Check
// reports the shrinking input too.
func checkPlanInvariants(t *testing.T, s planScenario, dp *DeploymentPlan) bool {
	t.Helper()
	filtered := applyNodeFilter(s.nodes, s.c)
	nodeByID := make(map[string]Node, len(filtered))
	for _, n := range filtered {
		nodeByID[n.ID] = n
	}
	ctx := s.model.ContextMax

	q, ok := lookupQuant(s.model.Quantizations, dp.Quantization)
	if !ok {
		t.Logf("invariant: plan quant %q not among model quantizations", dp.Quantization)
		return false
	}

	// (5) exactly one HOST.
	hosts := 0
	for _, a := range dp.Assignments {
		if a.Role == RoleHost {
			hosts++
		}
	}
	if hosts != 1 {
		t.Logf("invariant (5): expected exactly one HOST, got %d (%+v)", hosts, dp.Assignments)
		return false
	}

	// (4) contiguous cover of [0, Layers).
	if !isContiguousCover(dp.Assignments, s.model.Layers) {
		t.Logf("invariant (4): not a contiguous cover of [0,%d): %+v", s.model.Layers, dp.Assignments)
		return false
	}

	// (1) memory fit (+ headroom for multi-node), and reported headroom >= 0.
	multi := len(dp.Assignments) > 1
	for _, a := range dp.Assignments {
		n, ok := nodeByID[a.NodeID]
		if !ok {
			t.Logf("invariant (1): assignment references unknown node %q", a.NodeID)
			return false
		}
		need := stageMemNeed(s.model, q, a.LayerStart, a.LayerEnd+1, ctx)
		if need > usefulMemory(n) {
			t.Logf("invariant (1): node %q shard [%d,%d] needs %.2f GB > %.2f GB useful",
				a.NodeID, a.LayerStart, a.LayerEnd, need, usefulMemory(n))
			return false
		}
		if multi && !stageFits(n, s.model, q, a.LayerStart, a.LayerEnd+1, ctx) {
			t.Logf("invariant (1): multi-node stage %q [%d,%d] violates HEADROOM margin",
				a.NodeID, a.LayerStart, a.LayerEnd)
			return false
		}
	}
	if dp.Estimated.HeadroomGB < 0 {
		t.Logf("invariant (1): reported headroom is negative: %.3f", dp.Estimated.HeadroomGB)
		return false
	}

	// (2) uses no more than k_min+DELTA nodes.
	kv := estimateKVCache(s.model, ctx)
	subsets := candidateSubsets(filtered, s.model, q, kv, s.c)
	maxK := len(filtered)
	if len(subsets) > 0 {
		maxK = len(subsets[len(subsets)-1])
	}
	if len(dp.Assignments) > maxK {
		t.Logf("invariant (2): plan uses %d nodes > k_min+DELTA bound %d", len(dp.Assignments), maxK)
		return false
	}

	// (3) ForceHost lands at the pipeline head.
	if s.c.ForceHost != nil {
		if len(dp.PipelineOrder) == 0 || dp.PipelineOrder[0] != *s.c.ForceHost {
			t.Logf("invariant (3): ForceHost %q not at head (order %v)", *s.c.ForceHost, dp.PipelineOrder)
			return false
		}
	}

	// (6) draft placement (phase E): exactly one carrier, on the tail.
	if s.model.Draft.Available {
		n, idx := countDraft(dp.Assignments)
		if n != 1 {
			t.Logf("invariant (6): expected exactly one draft carrier, got %d", n)
			return false
		}
		if dp.Assignments[idx].LayerEnd != s.model.Layers-1 {
			t.Logf("invariant (6): draft carrier ends at %d, want tail %d", dp.Assignments[idx].LayerEnd, s.model.Layers-1)
			return false
		}
	}

	return true
}

// isContiguousCover is the boolean sibling of checkContiguousCover: it reports
// whether the assignments are contiguous, non-overlapping, gap-free shards
// covering exactly [0, layers).
func isContiguousCover(assignments []Assignment, layers int) bool {
	if len(assignments) == 0 {
		return false
	}
	sorted := append([]Assignment(nil), assignments...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LayerStart < sorted[j].LayerStart })
	if sorted[0].LayerStart != 0 || sorted[len(sorted)-1].LayerEnd != layers-1 {
		return false
	}
	for i, a := range sorted {
		if a.LayerEnd < a.LayerStart {
			return false
		}
		if i > 0 && a.LayerStart != sorted[i-1].LayerEnd+1 {
			return false
		}
	}
	return true
}

// TestPlan_PinnedRespected is the deterministic gate for the Pinned part of
// invariant (3): the shard covering a pinned layer is assigned to the pinned
// node (best-effort pinning, design 08 §6/§14).
func TestPlan_PinnedRespected(t *testing.T) {
	// 40 GB weights over two 30 GB nodes: both nodes are in the subset (k_min=2).
	nodes := []Node{node("A", 30, 150), node("B", 30, 120)}
	links := []Link{{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12}}
	model, _ := dpTestModel(8, 40)

	// Pin a tail-ish layer to A (the host); the shard containing it must land on A.
	pinned := map[LayerRange]NodeID{{Start: 6, End: 6}: "A"}
	dp, err := Plan(nodes, links, model, Constraints{Pinned: pinned})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}

	found := false
	for _, a := range dp.Assignments {
		if a.LayerStart <= 6 && 6 <= a.LayerEnd {
			found = true
			if a.NodeID != "A" {
				t.Fatalf("pinned layer 6 served by %q, want A (order %v)", a.NodeID, dp.PipelineOrder)
			}
		}
	}
	if !found {
		t.Fatalf("no shard covers pinned layer 6: %+v", dp.Assignments)
	}
}
