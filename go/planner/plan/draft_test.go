package plan

import "testing"

// This file is the correctness gate for phase E (design 08 §8): speculative
// draft placement. With a draft available, EXACTLY ONE assignment must carry the
// draft flag, and it must be the pipeline TAIL — the shard holding the model's
// last layer.

// countDraft returns how many assignments carry the draft flag and the index of
// the (first) one that does (-1 if none).
func countDraft(assignments []Assignment) (n, idx int) {
	idx = -1
	for i, a := range assignments {
		if a.Draft {
			n++
			if idx == -1 {
				idx = i
			}
		}
	}
	return n, idx
}

// TestPlaceDraft_SingleNode: on a single-node plan the sole assignment (which is
// both host and tail) carries the draft.
func TestPlaceDraft_SingleNode(t *testing.T) {
	nodes := []Node{node("solo", 64, 400)}
	model, _ := dpTestModel(6, 12)
	model.Draft = DraftInfo{Available: true, Type: "mtp", TailLayers: 2}

	dp, err := Plan(nodes, nil, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if len(dp.Assignments) != 1 {
		t.Fatalf("expected a single-node plan, got %d assignments", len(dp.Assignments))
	}
	n, idx := countDraft(dp.Assignments)
	if n != 1 || idx != 0 {
		t.Fatalf("expected exactly one draft on the single node, got n=%d idx=%d", n, idx)
	}
	if dp.Assignments[0].LayerEnd != model.Layers-1 {
		t.Fatalf("draft node must hold the tail layer %d, got LayerEnd=%d", model.Layers-1, dp.Assignments[0].LayerEnd)
	}
}

// TestPlaceDraft_MultiNodeTail: on a multi-node plan exactly one assignment
// carries the draft and it is the TAIL — max LayerEnd (== Layers-1), the last
// stage in the pipeline order.
func TestPlaceDraft_MultiNodeTail(t *testing.T) {
	// 40 GB weights over two 30 GB nodes: forces a 2-way split (k_min = 2).
	nodes := []Node{node("A", 30, 150), node("B", 30, 120)}
	links := []Link{{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12}}
	model, _ := dpTestModel(8, 40)
	model.Draft = DraftInfo{Available: true, Type: "eagle", TailLayers: 3}

	dp, err := Plan(nodes, links, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if len(dp.Assignments) < 2 {
		t.Fatalf("expected a multi-node plan, got %d assignment(s)", len(dp.Assignments))
	}

	n, idx := countDraft(dp.Assignments)
	if n != 1 {
		t.Fatalf("expected exactly one draft assignment, got %d", n)
	}
	// The draft carrier must hold the model's last layer...
	if dp.Assignments[idx].LayerEnd != model.Layers-1 {
		t.Fatalf("draft on shard [%d,%d]; want the tail ending at %d",
			dp.Assignments[idx].LayerStart, dp.Assignments[idx].LayerEnd, model.Layers-1)
	}
	// ...and be the max-LayerEnd shard among all assignments (the true tail).
	for i, a := range dp.Assignments {
		if a.LayerEnd > dp.Assignments[idx].LayerEnd {
			t.Fatalf("assignment %d has a later tail (LayerEnd %d) than the draft carrier (%d)",
				i, a.LayerEnd, dp.Assignments[idx].LayerEnd)
		}
	}
	// The tail carrier is the last stage of the pipeline order (no extra hop).
	if dp.PipelineOrder[len(dp.PipelineOrder)-1] != dp.Assignments[idx].NodeID {
		t.Fatalf("draft carrier %q is not the pipeline tail %q",
			dp.Assignments[idx].NodeID, dp.PipelineOrder[len(dp.PipelineOrder)-1])
	}
}

// TestPlaceDraft_NoDraft: with no draft available, no assignment is flagged.
func TestPlaceDraft_NoDraft(t *testing.T) {
	nodes := []Node{node("A", 30, 150), node("B", 30, 120)}
	links := []Link{{From: "A", To: "B", RTTms: 3, BandwidthGBs: 12}}
	model, _ := dpTestModel(8, 40) // Draft zero-value: not available

	dp, err := Plan(nodes, links, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if n, _ := countDraft(dp.Assignments); n != 0 {
		t.Fatalf("expected no draft assignments without a draft model, got %d", n)
	}
}

// TestPlaceDraft_Idempotent: calling placeDraft twice keeps exactly one carrier
// (guards the "clear then set" invariant behind repeated phase-E application).
func TestPlaceDraft_Idempotent(t *testing.T) {
	model, _ := dpTestModel(8, 40)
	model.Draft = DraftInfo{Available: true, Type: "mtp", TailLayers: 2}
	plan := &DeploymentPlan{
		Assignments: []Assignment{
			{NodeID: "A", Role: RoleHost, LayerStart: 0, LayerEnd: 3},
			{NodeID: "B", Role: RoleWorker, LayerStart: 4, LayerEnd: 7},
		},
	}
	placeDraft(plan, model)
	placeDraft(plan, model)
	n, idx := countDraft(plan.Assignments)
	if n != 1 || idx != 1 {
		t.Fatalf("expected exactly one draft on the tail (idx 1), got n=%d idx=%d", n, idx)
	}
}
