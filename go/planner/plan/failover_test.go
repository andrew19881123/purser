package plan

import (
	"context"
	"strings"
	"testing"
)

// This file is the correctness gate for phase F (design 08 §9): per-node
// failover alternatives. It checks that FailoverAlt has an entry for every node
// the plan uses, that the recursion is one level deep (alternatives carry no
// nested failover), and that an unabsorbable node loss degrades to a nil
// sentinel plus a "degrade/notify" note.

// planExplains reports whether any explanation line contains sub.
func planExplains(dp *DeploymentPlan, sub string) bool {
	for _, e := range dp.Explanation {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// TestFailover_EntryPerNodeAndNoRecursion: a 3-node fleet where the model needs
// only 2 nodes. Every node used by the primary plan gets a FailoverAlt entry;
// each feasible alternative is itself a valid plan with an EMPTY FailoverAlt
// (the anti-recursion guard — failover plans do not spawn their own failover).
func TestFailover_EntryPerNodeAndNoRecursion(t *testing.T) {
	// 40 GB weights; each node holds ~5 of 8 layers, so any 2 of the 3 suffice.
	nodes := []Node{node("A", 30, 200), node("B", 30, 130), node("C", 30, 160)}
	links := []Link{
		{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12},
		{From: "B", To: "C", RTTms: 4, BandwidthGBs: 10},
		{From: "A", To: "C", RTTms: 5, BandwidthGBs: 9},
		{From: "B", To: "A", RTTms: 3, BandwidthGBs: 12},
		{From: "C", To: "B", RTTms: 4, BandwidthGBs: 10},
		{From: "C", To: "A", RTTms: 5, BandwidthGBs: 9},
	}
	model, _ := dpTestModel(8, 40)

	dp, err := Plan(context.Background(), nodes, links, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}

	// One FailoverAlt entry per distinct node used by the plan.
	used := map[string]bool{}
	for _, a := range dp.Assignments {
		used[a.NodeID] = true
	}
	if len(dp.FailoverAlt) != len(used) {
		t.Fatalf("FailoverAlt has %d entries, want one per used node (%d)", len(dp.FailoverAlt), len(used))
	}
	for nid := range used {
		alt, ok := dp.FailoverAlt[nid]
		if !ok {
			t.Fatalf("missing FailoverAlt entry for used node %q", nid)
		}
		// With a spare node available, losing any used node stays feasible.
		if alt == nil {
			t.Fatalf("expected a feasible failover for %q (a spare node exists), got degrade/notify", nid)
		}
		// Anti-recursion guard: failover plans carry NO nested failover.
		if len(alt.FailoverAlt) != 0 {
			t.Fatalf("failover plan for %q has a non-empty FailoverAlt (%d) — recursion not bounded",
				nid, len(alt.FailoverAlt))
		}
		// And the alternative must not use the dead node.
		for _, a := range alt.Assignments {
			if a.NodeID == nid {
				t.Fatalf("failover plan for %q still uses the dead node", nid)
			}
		}
		// The alternative is itself a valid contiguous cover.
		checkContiguousCover(t, alt.Assignments, model.Layers)
	}
}

// TestFailover_SingleNodeDegrade: on a single-node deployment, losing the only
// node cannot be absorbed — FailoverAlt[node] is the nil sentinel and the plan
// carries a degrade/notify note.
func TestFailover_SingleNodeDegrade(t *testing.T) {
	nodes := []Node{node("solo", 64, 400)}
	model, _ := dpTestModel(6, 12) // fits comfortably on one node

	dp, err := Plan(context.Background(), nodes, nil, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if len(dp.FailoverAlt) != 1 {
		t.Fatalf("expected one FailoverAlt entry, got %d", len(dp.FailoverAlt))
	}
	alt, ok := dp.FailoverAlt["solo"]
	if !ok {
		t.Fatal("missing FailoverAlt entry for the sole node")
	}
	if alt != nil {
		t.Fatalf("expected nil (degrade/notify) failover for the sole node, got %+v", alt)
	}
	if !planExplains(dp, "degrade/notify") {
		t.Fatalf("expected a degrade/notify explanation, got %v", dp.Explanation)
	}
}

// TestFailover_TwoNodeBothRequiredDegrade: when the model needs BOTH nodes,
// losing either leaves an infeasible single node → both entries are the nil
// sentinel and two degrade/notify notes are recorded.
func TestFailover_TwoNodeBothRequiredDegrade(t *testing.T) {
	// 40 GB weights over two 30 GB nodes: neither node alone can hold it.
	nodes := []Node{node("A", 30, 150), node("B", 30, 120)}
	links := []Link{{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12}}
	model, _ := dpTestModel(8, 40)

	dp, err := Plan(context.Background(), nodes, links, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if len(dp.Assignments) != 2 {
		t.Fatalf("expected a two-node plan, got %d assignments", len(dp.Assignments))
	}
	for _, nid := range []string{"A", "B"} {
		alt, ok := dp.FailoverAlt[nid]
		if !ok {
			t.Fatalf("missing FailoverAlt entry for %q", nid)
		}
		if alt != nil {
			t.Fatalf("expected nil (degrade/notify) failover for %q, got a plan", nid)
		}
	}
	// Both losses must be reported as degrade/notify.
	notes := 0
	for _, e := range dp.Explanation {
		if strings.Contains(e, "degrade/notify") {
			notes++
		}
	}
	if notes != 2 {
		t.Fatalf("expected 2 degrade/notify notes, got %d (%v)", notes, dp.Explanation)
	}
}

// TestFailover_RecruitsSpareNode: a spare node NOT used by the primary plan can
// be recruited into a failover plan when a used node dies.
func TestFailover_RecruitsSpareNode(t *testing.T) {
	// Primary plan fits on 2 nodes; a big spare "C" sits idle. Losing a used
	// node pulls C in. Make C the LEAST capable so the primary prefers A,B.
	nodes := []Node{node("A", 40, 300), node("B", 40, 260), node("C", 40, 60)}
	links := []Link{
		{From: "A", To: "B", RTTms: 2, BandwidthGBs: 20},
		{From: "A", To: "C", RTTms: 2, BandwidthGBs: 20},
		{From: "B", To: "C", RTTms: 2, BandwidthGBs: 20},
		{From: "B", To: "A", RTTms: 2, BandwidthGBs: 20},
		{From: "C", To: "A", RTTms: 2, BandwidthGBs: 20},
		{From: "C", To: "B", RTTms: 2, BandwidthGBs: 20},
	}
	model, _ := dpTestModel(8, 44)

	dp, err := Plan(context.Background(), nodes, links, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	// Every used node should have a feasible failover (the spare absorbs the loss).
	for _, a := range dp.Assignments {
		alt := dp.FailoverAlt[a.NodeID]
		if alt == nil {
			t.Fatalf("losing used node %q should be absorbable via the spare, got degrade/notify", a.NodeID)
		}
		checkContiguousCover(t, alt.Assignments, model.Layers)
	}
}
