package plan

import (
	"math"
	"testing"
)

// This file is the correctness gate for phase D (design 08 §7): the pipeline
// ORDERING. It checks the exact Held-Karp path on small fleets, the
// nearest-neighbour + 2-opt heuristic above the threshold, HOST placement
// (ForceHost + capacity default), and the order↔partition co-optimisation.

// orderIDs projects a node slice to its IDs (for readable failure messages).
func orderIDs(nodes []Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

// brutePathCost is the independent reference for the min-cost Hamiltonian path:
// it fixes the host at position 0 and enumerates every permutation of the rest,
// scoring each with the same edgeCost the solver uses. Returns the minimum.
func brutePathCost(nodes []Node, hostIdx int, model ModelSpec, links []Link) float64 {
	order := make([]int, 0, len(nodes))
	order = append(order, hostIdx)
	for i := range nodes {
		if i != hostIdx {
			order = append(order, i)
		}
	}
	best := math.Inf(1)
	var perm func(k int)
	perm = func(k int) {
		if k == len(order) {
			c := 0.0
			for i := 0; i+1 < len(order); i++ {
				c += edgeCost(nodes[order[i]].ID, nodes[order[i+1]].ID, model, links)
			}
			if c < best {
				best = c
			}
			return
		}
		for i := k; i < len(order); i++ {
			order[k], order[i] = order[i], order[k]
			perm(k + 1)
			order[k], order[i] = order[i], order[k]
		}
	}
	perm(1) // keep the host pinned at position 0
	return best
}

// sameSet reports whether two node slices contain the same IDs (a permutation).
func sameSet(a, b []Node) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, n := range a {
		seen[n.ID]++
	}
	for _, n := range b {
		seen[n.ID]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

// TestEdgeCost_MissingLinkFinite checks edgeCost's units and the missing-link
// handling: a measured hop costs RTT + a tiny transfer term; an unmeasured hop
// costs the high-but-finite penalty (never +Inf).
func TestEdgeCost_MissingLinkFinite(t *testing.T) {
	model, _ := dpTestModel(4, 8) // HiddenSize 4096 → activationBytes 8192 B
	links := []Link{{From: "A", To: "B", RTTms: 5, BandwidthGBs: 10}}

	ab := edgeCost("A", "B", model, links)
	// 5 ms RTT + 8192 / (10*1e6 bytes/ms) = 5 + ~0.00082 ms.
	if ab < 5.0 || ab > 5.01 {
		t.Fatalf("edgeCost(A,B) = %.6f, want ~5.0008 ms", ab)
	}
	missing := edgeCost("A", "Z", model, links)
	if missing != missingLinkPenaltyMs {
		t.Fatalf("missing-link cost = %.1f, want %.1f (high but finite)", missing, missingLinkPenaltyMs)
	}
	if math.IsInf(missing, 0) {
		t.Fatal("missing-link cost must be finite")
	}
}

// TestOrderNodes_HeldKarp checks the exact solver: (1) a trivial 3-node case has
// the expected optimum A→B→C, and (2) on a 4-node asymmetric fleet the path cost
// equals the brute-force minimum — and never exceeds an arbitrary order.
func TestOrderNodes_HeldKarp(t *testing.T) {
	model, _ := dpTestModel(8, 16)

	t.Run("trivial-expected-optimum", func(t *testing.T) {
		nodes := []Node{node("A", 40, 100), node("B", 40, 100), node("C", 40, 100)}
		links := []Link{
			{From: "A", To: "B", RTTms: 1, BandwidthGBs: 1000},
			{From: "B", To: "C", RTTms: 1, BandwidthGBs: 1000},
			{From: "A", To: "C", RTTms: 100, BandwidthGBs: 1000},
			{From: "C", To: "B", RTTms: 100, BandwidthGBs: 1000},
			{From: "B", To: "A", RTTms: 100, BandwidthGBs: 1000},
			{From: "C", To: "A", RTTms: 100, BandwidthGBs: 1000},
		}
		host := "A"
		got := orderNodes(nodes, model, links, Constraints{ForceHost: &host})
		want := []string{"A", "B", "C"}
		for i := range want {
			if got[i].ID != want[i] {
				t.Fatalf("order = %v, want %v", orderIDs(got), want)
			}
		}
		// And it must not exceed an arbitrary order from the same host.
		arbitrary := []Node{nodes[0], nodes[2], nodes[1]} // A, C, B
		if gc, ac := orderCost(got, model, links), orderCost(arbitrary, model, links); gc > ac+dpEps {
			t.Fatalf("Held-Karp cost %.6f > arbitrary %.6f", gc, ac)
		}
	})

	t.Run("matches-brute-force-optimum", func(t *testing.T) {
		// Distinct capacities so the capacity-chosen host is deterministic (A).
		nodes := []Node{node("A", 40, 300), node("B", 30, 100), node("C", 35, 150), node("D", 25, 200)}
		links := []Link{
			{From: "A", To: "B", RTTms: 9, BandwidthGBs: 20}, {From: "A", To: "C", RTTms: 3, BandwidthGBs: 20},
			{From: "A", To: "D", RTTms: 7, BandwidthGBs: 20}, {From: "B", To: "C", RTTms: 8, BandwidthGBs: 20},
			{From: "B", To: "D", RTTms: 2, BandwidthGBs: 20}, {From: "C", To: "D", RTTms: 6, BandwidthGBs: 20},
			{From: "C", To: "B", RTTms: 4, BandwidthGBs: 20}, {From: "D", To: "B", RTTms: 5, BandwidthGBs: 20},
			{From: "D", To: "C", RTTms: 1, BandwidthGBs: 20}, {From: "B", To: "A", RTTms: 9, BandwidthGBs: 20},
			{From: "C", To: "A", RTTms: 3, BandwidthGBs: 20}, {From: "D", To: "A", RTTms: 7, BandwidthGBs: 20},
		}
		got := orderNodes(nodes, model, links, Constraints{})
		if got[0].ID != "A" {
			t.Fatalf("host = %q, want A (most capable)", got[0].ID)
		}
		if !sameSet(got, nodes) {
			t.Fatalf("order %v is not a permutation of the input", orderIDs(got))
		}
		// hostIdx of A within `nodes` is 0.
		want := brutePathCost(nodes, 0, model, links)
		if gc := orderCost(got, model, links); math.Abs(gc-want) > dpEps {
			t.Fatalf("Held-Karp cost %.9f != brute-force optimum %.9f (order %v)", gc, want, orderIDs(got))
		}
	})
}

// TestOrderNodes_TwoOpt exercises the heuristic branch (> HeldKarpMaxNodes): it
// must not crash and must return a valid permutation with the host first. A
// second, crafted case proves 2-opt STRICTLY improves a nearest-neighbour path
// that walks into an expensive final hop.
func TestOrderNodes_TwoOpt(t *testing.T) {
	model, _ := dpTestModel(16, 32)

	t.Run("heuristic-branch-no-crash", func(t *testing.T) {
		// 12 nodes (> HeldKarpMaxNodes) forces the NN + 2-opt path.
		const nNodes = 12
		nodes := make([]Node, nNodes)
		var links []Link
		for i := 0; i < nNodes; i++ {
			nodes[i] = node(string(rune('A'+i)), 40, 100)
		}
		for i := 0; i < nNodes; i++ {
			from := string(rune('A' + i))
			to := string(rune('A' + (i+1)%nNodes))
			links = append(links, Link{From: from, To: to, RTTms: 1 + float64(i), BandwidthGBs: 50})
		}

		got := orderNodes(nodes, model, links, Constraints{})
		if len(got) != nNodes || !sameSet(got, nodes) {
			t.Fatalf("orderNodes returned %v, want a permutation of A..L", orderIDs(got))
		}

		// 2-opt never worsens the nearest-neighbour path it starts from.
		nn := nearestNeighborOrder(nodes, model, links)
		opt := twoOptImprove(nn, model, links)
		if oc, nc := orderCost(opt, model, links), orderCost(nn, model, links); oc > nc+dpEps {
			t.Fatalf("2-opt cost %.6f worse than nearest-neighbour %.6f", oc, nc)
		}
	})

	t.Run("strictly-improves-a-greedy-mistake", func(t *testing.T) {
		// Directed costs where NN greedily builds H→A→B→C and is trapped into the
		// expensive B→C(10); reversing the tail to H→A→C→B (A→C=2, C→B=2) is far
		// cheaper. Missing hops default to the finite penalty and are never chosen.
		nodes := []Node{node("H", 40, 100), node("A", 40, 100), node("B", 40, 100), node("C", 40, 100)}
		links := []Link{
			{From: "H", To: "A", RTTms: 1, BandwidthGBs: 1e6}, {From: "H", To: "B", RTTms: 10, BandwidthGBs: 1e6},
			{From: "H", To: "C", RTTms: 10, BandwidthGBs: 1e6}, {From: "A", To: "B", RTTms: 1, BandwidthGBs: 1e6},
			{From: "A", To: "C", RTTms: 2, BandwidthGBs: 1e6}, {From: "B", To: "C", RTTms: 10, BandwidthGBs: 1e6},
			{From: "C", To: "B", RTTms: 2, BandwidthGBs: 1e6},
		}
		nn := nearestNeighborOrder(nodes, model, links)
		if got := orderIDs(nn); got[0] != "H" || got[1] != "A" || got[2] != "B" || got[3] != "C" {
			t.Fatalf("nearest-neighbour route = %v, want [H A B C]", got)
		}
		opt := twoOptImprove(nn, model, links)
		nc, oc := orderCost(nn, model, links), orderCost(opt, model, links)
		if oc >= nc-dpEps {
			t.Fatalf("2-opt did not strictly improve: nn=%.4f opt=%.4f (route %v)", nc, oc, orderIDs(opt))
		}
		if !sameSet(opt, nn) {
			t.Fatalf("2-opt result %v is not a permutation of %v", orderIDs(opt), orderIDs(nn))
		}
	})
}

// TestOrderNodes_ForceHost checks that a forced host lands at the pipeline head,
// both from orderNodes directly and end-to-end through Plan's PipelineOrder.
func TestOrderNodes_ForceHost(t *testing.T) {
	model, _ := dpTestModel(12, 54)
	nodes := []Node{node("A", 25, 200), node("B", 25, 130), node("C", 25, 160)}
	links := []Link{
		{From: "A", To: "B", RTTms: 4, BandwidthGBs: 10},
		{From: "B", To: "C", RTTms: 5, BandwidthGBs: 9},
		{From: "C", To: "A", RTTms: 6, BandwidthGBs: 8},
	}

	// A is the most capable (cap 50 vs 40/32.5); force the LEAST-obvious host.
	forced := "B"
	got := orderNodes(nodes, model, links, Constraints{ForceHost: &forced})
	if got[0].ID != "B" {
		t.Fatalf("orderNodes head = %q, want forced B (order %v)", got[0].ID, orderIDs(got))
	}

	// End-to-end: Plan must put B at PipelineOrder[0] and mark it HOST.
	dp, err := Plan(nodes, links, model, Constraints{ForceHost: &forced})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if dp.PipelineOrder[0] != "B" {
		t.Fatalf("PipelineOrder head = %q, want B (order %v)", dp.PipelineOrder[0], dp.PipelineOrder)
	}
	if dp.Assignments[0].NodeID != "B" || dp.Assignments[0].Role != RoleHost {
		t.Fatalf("first assignment = %+v, want B/HOST", dp.Assignments[0])
	}
	hosts := 0
	for _, a := range dp.Assignments {
		if a.Role == RoleHost {
			hosts++
		}
	}
	if hosts != 1 {
		t.Fatalf("expected exactly 1 HOST, got %d", hosts)
	}
}

// TestMultiNodePlan_CoOptimization verifies the order↔partition co-optimisation:
// orderNodes is idempotent (so the loop reaches its fixed point within
// coOptMaxRounds — on a LAN, in a single round), and the assembled plan stays
// valid (contiguous shards covering all layers, exactly one HOST).
func TestMultiNodePlan_CoOptimization(t *testing.T) {
	model, _ := dpTestModel(12, 54)
	nodes := []Node{node("A", 25, 200), node("B", 25, 130), node("C", 25, 160)}
	links := []Link{
		{From: "A", To: "B", RTTms: 4, BandwidthGBs: 10},
		{From: "B", To: "C", RTTms: 5, BandwidthGBs: 9},
		{From: "C", To: "A", RTTms: 6, BandwidthGBs: 8},
	}

	// Idempotency is the mechanism behind fixed-point convergence: re-ordering an
	// already-ordered fleet returns the same sequence, so the co-opt loop's
	// sameOrder check fires immediately (round 1) and never exceeds coOptMaxRounds.
	o1 := orderNodes(nodes, model, links, Constraints{})
	o2 := orderNodes(o1, model, links, Constraints{})
	if !sameOrder(o1, o2) {
		t.Fatalf("orderNodes not idempotent: %v then %v", orderIDs(o1), orderIDs(o2))
	}

	dp, err := Plan(nodes, links, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if len(dp.Assignments) < 2 {
		t.Fatalf("expected a multi-node plan, got %d assignment(s)", len(dp.Assignments))
	}
	// Valid partition: contiguous shards covering [0, Layers), one HOST.
	checkContiguousCover(t, dp.Assignments, model.Layers)
	if len(dp.PipelineOrder) != len(dp.Assignments) {
		t.Fatalf("PipelineOrder len %d != assignments %d", len(dp.PipelineOrder), len(dp.Assignments))
	}
	hosts := 0
	for _, a := range dp.Assignments {
		if a.Role == RoleHost {
			hosts++
		}
	}
	if hosts != 1 {
		t.Fatalf("expected exactly 1 HOST, got %d", hosts)
	}
}
