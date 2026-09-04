package plan

import "fmt"

// This file implements phase E of the planner (design 08 §8): speculative-draft
// placement.
//
// When a model ships a speculative draft (MTP / EAGLE / a small draft model),
// the draft heads must run on the node that holds the LAST layers of the target
// model — the pipeline TAIL. The final hidden state produced by the tail shard
// is exactly the input the draft heads consume, so co-locating them there adds
// NO extra pipeline hop: the draft proposes its tokens on the same box that just
// finished the forward pass. Placing the draft anywhere else would ship the tail
// activation back across the network every step.
//
// The RAM the draft needs is already accounted for in phase A (it rides inside
// the per-node overhead / weight budget), so phase E is purely a PLACEMENT
// decision: it flips exactly one Assignment.Draft flag and records the choice in
// the explanation. It is idempotent and safe to call on any assembled plan.

// placeDraft marks the pipeline tail node — the assignment whose shard holds the
// model's last layer (max LayerEnd) — as the speculative-draft carrier. It is a
// no-op when the model has no draft or the plan has no assignments. Exactly one
// assignment ends up with Draft == true (any pre-existing flags are cleared
// first, so the invariant holds even if called more than once).
func placeDraft(plan *DeploymentPlan, model ModelSpec) {
	if plan == nil || !model.Draft.Available || len(plan.Assignments) == 0 {
		return
	}

	// Find the tail: the shard with the largest LayerEnd. For a valid contiguous
	// cover this is unique (LayerEnd == model.Layers-1). Ties (which should not
	// occur) break to the earliest assignment for determinism.
	tail := 0
	for i := 1; i < len(plan.Assignments); i++ {
		if plan.Assignments[i].LayerEnd > plan.Assignments[tail].LayerEnd {
			tail = i
		}
	}

	// Exactly one carrier: clear any stale flags, then set the tail.
	for i := range plan.Assignments {
		plan.Assignments[i].Draft = false
	}
	plan.Assignments[tail].Draft = true

	tailNode := plan.Assignments[tail].NodeID
	if len(plan.Assignments) == 1 {
		plan.Explanation = append(plan.Explanation, fmt.Sprintf(
			"phase E: speculative draft (%s, %d tail layers) co-located on host %q (no extra hop)",
			model.Draft.Type, model.Draft.TailLayers, tailNode))
		return
	}
	plan.Explanation = append(plan.Explanation, fmt.Sprintf(
		"phase E: speculative draft (%s, %d tail layers) placed on pipeline tail %q (holds layers [%d,%d]); no extra hop",
		model.Draft.Type, model.Draft.TailLayers, tailNode,
		plan.Assignments[tail].LayerStart, plan.Assignments[tail].LayerEnd))
}
