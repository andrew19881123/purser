package plan

import "fmt"

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
func computeFailoverPlans(plan *DeploymentPlan, nodes []Node, links []Link, model ModelSpec, c Constraints) {
	if plan == nil {
		return
	}
	if plan.FailoverAlt == nil {
		plan.FailoverAlt = make(map[string]*DeploymentPlan, len(plan.Assignments))
	}

	// Iterate over the nodes the plan uses, in assignment (pipeline) order for a
	// deterministic explanation. Each node appears once in a contiguous cover;
	// the presence check guards against any accidental duplicate.
	for _, a := range plan.Assignments {
		dead := a.NodeID
		if _, done := plan.FailoverAlt[dead]; done {
			continue
		}

		remaining := nodesExcluding(nodes, dead)
		// Non-recursive: computeFailover == false, so alt.FailoverAlt stays empty.
		alt, err := planInternal(remaining, links, model, c, false)
		if err != nil {
			// Degrade/notify: the fleet cannot absorb losing this node.
			plan.FailoverAlt[dead] = nil
			plan.Explanation = append(plan.Explanation, fmt.Sprintf(
				"phase F: losing node %q leaves no feasible plan — degrade/notify operator", dead))
			continue
		}
		plan.FailoverAlt[dead] = alt
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
