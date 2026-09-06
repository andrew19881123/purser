package plan

import (
	"context"
	"fmt"
)

// This file implements phase F of the planner (design 08 §9): per-node failover
// alternatives.
//
// For every node the primary plan actually uses, we pre-compute an alternative
// deployment over the fleet MINUS that node, so the control plane can react
// instantly when a box dies instead of replanning under pressure. The result is
// stored in DeploymentPlan.FailoverAlt, keyed by the dead node's ID.
//
// ANTI-RECURSION GUARD (design 08 §9): the alternatives are produced by
// planInternal(..., computeFailover=false), so a failover plan never spawns its
// own failover plans. The recursion is therefore exactly one level deep and
// always terminates. Every failover alternative carries an empty FailoverAlt.
//
// DEGRADE/NOTIFY CONVENTION: if removing a node makes the model infeasible on
// what remains, FailoverAlt[nodeID] is set to nil (a sentinel meaning "no
// standby plan — the fleet cannot absorb this loss") and a human-readable
// "degrade/notify" note is appended to the primary plan's Explanation. Callers
// distinguish "no failover computed" from "failover impossible" by key presence:
// a present key with a nil value means degrade/notify; a present key with a
// non-nil value is a ready standby plan.

// computeFailoverPlans fills plan.FailoverAlt with, for each node used by the
// plan, an alternative computed over `nodes` minus that node (design 08 §9).
// `nodes` is the already-filtered fleet planInternal worked with, so a spare
// node not used by the primary plan can be recruited into a failover plan.
//
// It must be called only from the top-level (computeFailover) path; the
// alternatives are planned non-recursively so they never re-enter this function.
//
// G15: All N failover plans are computed in parallel (one goroutine per node),
// reducing wall-clock time from O(N × plan_cost) to O(plan_cost). Results are
// drained before returning so no goroutine is leaked. They are then applied in
// original pipeline order to keep plan.Explanation deterministic.
func computeFailoverPlans(ctx context.Context, plan *DeploymentPlan, nodes []Node, links []Link, model ModelSpec, c Constraints) {
	if plan == nil {
		return
	}
	if plan.FailoverAlt == nil {
		plan.FailoverAlt = make(map[string]*DeploymentPlan, len(plan.Assignments))
	}

	// Collect distinct node IDs in pipeline (assignment) order, skipping any
	// that are already present in the map (guards against duplicate assignments).
	var order []string
	for _, a := range plan.Assignments {
		if _, done := plan.FailoverAlt[a.NodeID]; !done {
			// Use a sentinel to mark the slot as claimed so the second pass
			// below can populate it without a separate seen set.
			plan.FailoverAlt[a.NodeID] = nil
			order = append(order, a.NodeID)
		}
	}
	if len(order) == 0 {
		return
	}

	type result struct {
		nodeID string
		plan   *DeploymentPlan
		err    error
	}

	// Launch one goroutine per node — all N plans run concurrently.
	// Buffer == len(order) so goroutines never block on send even if the
	// caller's context is cancelled before we drain the channel.
	results := make(chan result, len(order))
	for _, dead := range order {
		dead := dead // capture loop variable
		go func() {
			remaining := nodesExcluding(nodes, dead)
			// Non-recursive: computeFailover == false, so alt.FailoverAlt stays empty.
			alt, err := planInternal(ctx, remaining, links, model, c, false)
			results <- result{nodeID: dead, plan: alt, err: err}
		}()
	}

	// Drain ALL results before returning — this is required to avoid goroutine
	// leaks even when the context has been cancelled (planInternal returns
	// quickly in that case, so the drain is cheap).
	collected := make(map[string]result, len(order))
	for range order {
		r := <-results
		collected[r.nodeID] = r
	}

	// Apply results in original pipeline order so plan.Explanation is deterministic.
	for _, dead := range order {
		r := collected[dead]
		if r.err != nil {
			// Degrade/notify: the fleet cannot absorb losing this node.
			// plan.FailoverAlt[dead] is already nil (sentinel set above).
			plan.Explanation = append(plan.Explanation, fmt.Sprintf(
				"phase F: losing node %q leaves no feasible plan — degrade/notify operator", dead))
		} else {
			plan.FailoverAlt[dead] = r.plan
		}
	}
}

// nodesExcluding returns a copy of nodes with the node whose ID == id removed.
func nodesExcluding(nodes []Node, id string) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.ID != id {
			out = append(out, n)
		}
	}
	return out
}
