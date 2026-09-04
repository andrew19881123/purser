package plan

import (
	"fmt"
	"math"
)

// This file assembles a multi-node DeploymentPlan for one candidate subset and
// scores it with the cost function (design 08 §10), so the phase-B selection
// loop can keep the cheapest candidate. It also drives the phase-D
// co-optimisation: it alternates the pipeline ORDERING (orderNodes, ordering.go)
// with the phase-C partition DP (dpPartition) to a fixed point.

// multiNodePlan builds and scores a DeploymentPlan for one candidate node
// `subset` (design 08 §5 selection loop). It co-optimises the pipeline order
// (phase D) with the partition (phase C), honours pinned ranges, and assembles
// the scored plan. nodeByID must resolve every node in the subset. Returns nil
// if no feasible contiguous partition exists on this subset — the caller (phase
// B loop) then tries the next candidate.
func multiNodePlan(
	subset []Node,
	nodeByID map[string]Node,
	model ModelSpec,
	quant Quantization,
	links []Link,
	c Constraints,
) *DeploymentPlan {
	// Phase D co-optimisation (design 08 §7): alternate the ordering with the
	// partition DP up to coOptMaxRounds times, stopping at the first fixed point
	// (order unchanged). On a LAN the coupling is WEAK — the activation size is
	// partition-independent, so orderNodes is idempotent and the loop converges
	// on the first re-order; the extra rounds only matter if a future,
	// partition-aware ordering is added.
	order := orderNodes(subset, model, links, c)
	assignments, bottleneck := dpPartition(order, links, model, quant, model.ContextMax)
	rounds := 1
	for rounds < coOptMaxRounds {
		reordered := orderNodes(order, model, links, c)
		if sameOrder(order, reordered) {
			break // fixed point: ordering stable → co-optimisation converged
		}
		order = reordered
		assignments, bottleneck = dpPartition(order, links, model, quant, model.ContextMax)
		rounds++
	}
	if len(assignments) == 0 {
		return nil // INFEASIBLE on this subset — phase B tries the next
	}

	pinNotes := applyPinnedRanges(assignments, order, c.Pinned)

	pipelineOrder := make([]string, 0, len(assignments))
	for _, a := range assignments {
		pipelineOrder = append(pipelineOrder, a.NodeID)
	}

	stageTimes := stageTimesForPlan(assignments, nodeByID, model, quant, links)
	cost := costFunction(pipelineOrder, stageTimes, quant, links, model, c)
	headroom := minStageHeadroom(assignments, nodeByID, model, quant)

	orderMethod := "exact (Held-Karp)"
	if len(order) > HeldKarpMaxNodes {
		orderMethod = "heuristic (nearest-neighbour + 2-opt)"
	}

	explanation := []string{
		fmt.Sprintf("selected quantization %q (quality %.2f)", quant.Name, quant.Quality),
		fmt.Sprintf("model does not fit on a single node: split across %d nodes (phase C throughput-aware DP)", len(assignments)),
		fmt.Sprintf("bottleneck stage time %.4f s/token (min-max partition); tightest headroom %.1f GB", bottleneck, headroom),
	}
	for _, a := range assignments {
		n := nodeByID[a.NodeID]
		explanation = append(explanation, fmt.Sprintf(
			"node %q (%s): layers [%d,%d] — need %.1f GB of %.1f GB useful",
			a.NodeID, a.Role, a.LayerStart, a.LayerEnd,
			stageMemNeed(model, quant, a.LayerStart, a.LayerEnd+1, model.ContextMax), usefulMemory(n)))
	}
	if quant.EmulatedFP4 {
		explanation = append(explanation, "quantization requires FP4 but no node is FP4-native: running emulated (penalised)")
	}
	explanation = append(explanation, pinNotes...)
	// Phase D: the order is the min-cost Hamiltonian path over activation
	// transfer, co-optimised with the partition (design 08 §7).
	explanation = append(explanation, fmt.Sprintf(
		"phase D ordering: min-cost Hamiltonian path over activation transfer, %s; host %q; co-optimised with partition (converged in %d round(s))",
		orderMethod, pipelineOrder[0], rounds))
	if c.ForceHost != nil {
		if *c.ForceHost == pipelineOrder[0] {
			explanation = append(explanation, fmt.Sprintf("force_host=%q honoured: it is the pipeline head", *c.ForceHost))
		} else {
			explanation = append(explanation, fmt.Sprintf(
				"note: force_host=%q not in the selected subset — host chosen by capacity (%q)", *c.ForceHost, pipelineOrder[0]))
		}
	}

	return &DeploymentPlan{
		PlanID:        newPlanID(model.ID, pipelineOrder[0]),
		ModelID:       model.ID,
		Quantization:  quant.Name,
		Assignments:   assignments,
		PipelineOrder: pipelineOrder,
		Estimated:     estimateMultiNode(bottleneck, headroom, model),
		Cost:          cost,
		Explanation:   explanation,
		// Phase E (placeDraft) and phase F (computeFailoverPlans) are applied by
		// planInternal after the winning plan is selected; FailoverAlt starts
		// empty so failover alternatives carry no nested plans.
		FailoverAlt: map[string]*DeploymentPlan{},
	}
}

// stageTimesForPlan recomputes the per-stage time for each assignment along the
// pipeline (used for imbalance and the perf estimate). Stage m receives its
// activation from stage m-1.
func stageTimesForPlan(assignments []Assignment, nodeByID map[string]Node, model ModelSpec, quant Quantization, links []Link) []float64 {
	times := make([]float64, len(assignments))
	for idx, a := range assignments {
		n := nodeByID[a.NodeID]
		ct := 0.0
		if idx >= 1 {
			ct = commTime(model, lookupLink(links, assignments[idx-1].NodeID, a.NodeID))
		}
		times[idx] = computeTime(n, model, quant, a.LayerStart, a.LayerEnd+1) + ct
	}
	return times
}

// minStageHeadroom returns the smallest per-node headroom (useful memory minus
// what its shard needs) across the pipeline — the tightest fit, reported to the
// UI. It is > 0 for any DP-feasible partition (the DP enforces the HEADROOM
// margin).
func minStageHeadroom(assignments []Assignment, nodeByID map[string]Node, model ModelSpec, quant Quantization) float64 {
	min := math.Inf(1)
	for _, a := range assignments {
		n := nodeByID[a.NodeID]
		h := usefulMemory(n) - stageMemNeed(model, quant, a.LayerStart, a.LayerEnd+1, model.ContextMax)
		if h < min {
			min = h
		}
	}
	if math.IsInf(min, 1) {
		return 0
	}
	return min
}

// costFunction scores a plan (design 08 §10). Lower is better. The phase-B loop
// picks the minimum-cost candidate. QualityWeightBias shifts W4 (design 08 §14).
//
// CALIBRATABLE: the weights are placeholders (design 08 §10, "da calibrare").
// hops/linkCost use the PROVISIONAL order; they will change once phase D
// (TODO fase8b) computes the optimal ordering.
func costFunction(order []string, stageTimes []float64, quant Quantization, links []Link, model ModelSpec, c Constraints) float64 {
	hops := len(order) - 1
	if hops < 0 {
		hops = 0
	}

	linkCost := 0.0
	for i := 0; i+1 < len(order); i++ {
		linkCost += commTime(model, lookupLink(links, order[i], order[i+1]))
	}

	imbalance := variance(stageTimes)
	qualityPenalty := 1 - quant.Quality
	fp4Penalty := 0.0
	if quant.EmulatedFP4 {
		fp4Penalty = 1
	}

	w4 := costW4Quality + c.QualityWeightBias
	return costW1Hops*float64(hops) +
		costW2LinkCost*linkCost +
		costW3Imbalance*imbalance +
		w4*qualityPenalty +
		costW5FP4*fp4Penalty
}

// variance is the population variance of xs (0 for < 2 elements).
func variance(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	mean := 0.0
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	sum := 0.0
	for _, x := range xs {
		d := x - mean
		sum += d * d
	}
	return sum / float64(len(xs))
}

// estimateMultiNode turns the pipeline bottleneck stage time into a
// decode/prefill RANGE (design 08 §11). The bottleneck already includes
// inter-stage communication, so it is the pipeline's per-token pace; the shared
// estimatePerformance converts it into a min/max band and applies the
// speculative-decoding factor when the model ships a draft.
func estimateMultiNode(bottleneck, headroom float64, model ModelSpec) PerfEstimate {
	return estimatePerformance(bottleneck, headroom, model)
}
