package plan

import (
	"context"
	"fmt"
	"sort"
)

// ReplicaSet describes N independent pipeline deployments serving the same model.
// Each replica is a complete DeploymentPlan on a disjoint subset of the fleet.
// The gateway distributes incoming requests across replicas according to the
// Routing policy.
type ReplicaSet struct {
	// ModelID is the model served by all replicas.
	ModelID string
	// Replicas holds the N independent deployment plans, one per fleet partition.
	// They are ordered by partition index (0 = top-capacity partition).
	Replicas []*DeploymentPlan
	// Routing determines how the gateway distributes requests across replicas.
	Routing RoutingPolicy
}

// RoutingPolicy controls how requests are distributed across replicas.
type RoutingPolicy int

const (
	// RoutingRoundRobin distributes requests evenly across all healthy replicas.
	RoutingRoundRobin RoutingPolicy = iota
	// RoutingLeastLoaded routes each request to the replica with the fewest
	// active in-flight requests. Requires live metrics feedback from the gateway.
	RoutingLeastLoaded
)

// PlanReplicaSet produces a ReplicaSet of replicaCount non-overlapping
// DeploymentPlans from the fleet.
//
// Strategy: greedy partition — sort nodes by usefulMemoryForFit descending,
// divide into replicaCount equal-ish groups, plan each group independently
// in parallel. Each group's plan call is isolated to its own fleet partition,
// so the resulting assignments are disjoint by construction.
//
// If replicaCount == 1, equivalent to Plan() (no partitioning overhead).
// Returns error if:
//   - replicaCount < 1
//   - the model does not fit any node subset
//   - the fleet has fewer than k_min×replicaCount nodes, where k_min is the
//     minimum pipeline depth derived from a trial plan on the top-half of nodes.
func PlanReplicaSet(
	ctx context.Context,
	nodes []Node, links []Link,
	model ModelSpec, c Constraints,
	replicaCount int,
) (*ReplicaSet, error) {
	if replicaCount <= 0 {
		return nil, fmt.Errorf("planner: replicaCount must be >= 1, got %d", replicaCount)
	}

	// Fast path: single replica is just a regular Plan().
	if replicaCount == 1 {
		dp, err := Plan(ctx, nodes, links, model, c)
		if err != nil {
			return nil, err
		}
		return &ReplicaSet{
			ModelID:  model.ID,
			Replicas: []*DeploymentPlan{dp},
			Routing:  RoutingRoundRobin,
		}, nil
	}

	if len(nodes) == 0 {
		return nil, &PlanError{
			Reason:      "no nodes available",
			Suggestions: []string{"register at least one node"},
		}
	}

	// Sort nodes by useful memory (for fit checks) descending so the top
	// partition always gets the most capable nodes.
	ranked := make([]Node, len(nodes))
	copy(ranked, nodes)
	sort.Slice(ranked, func(i, j int) bool {
		return usefulMemoryForFit(ranked[i]) > usefulMemoryForFit(ranked[j])
	})

	// Estimate k_min (minimum nodes per replica) by running a trial Plan on the
	// top half of the fleet. Using the top half avoids including marginal nodes
	// in the fit estimate while still covering the common case where k_min << N.
	trialSize := len(ranked)/2 + 1
	trialDP, err := Plan(ctx, ranked[:trialSize], links, model, c)
	if err != nil {
		// Top-half failed: try the full fleet to confirm feasibility.
		trialDP, err = Plan(ctx, ranked, links, model, c)
		if err != nil {
			return nil, fmt.Errorf("model does not fit fleet: %w", err)
		}
	}
	kMin := len(trialDP.Assignments)

	if len(nodes) < kMin*replicaCount {
		return nil, &PlanError{
			Reason: fmt.Sprintf(
				"fleet has %d nodes but %d replica(s) × %d nodes/replica required (k_min=%d)",
				len(nodes), replicaCount, kMin, kMin),
			Suggestions: []string{
				fmt.Sprintf("add at least %d more nodes", kMin*replicaCount-len(nodes)),
				"reduce replicaCount",
				"use a smaller quantization to lower k_min",
			},
		}
	}

	// Partition fleet into replicaCount equal-ish groups (last group absorbs
	// any remainder nodes). Each group is a contiguous slice of ranked[], so
	// groups are disjoint by construction.
	groupSize := len(ranked) / replicaCount

	// Plan each group in parallel.
	type replicaResult struct {
		idx  int
		plan *DeploymentPlan
		err  error
	}
	resCh := make(chan replicaResult, replicaCount)

	for i := 0; i < replicaCount; i++ {
		i := i // capture
		start := i * groupSize
		end := start + groupSize
		if i == replicaCount-1 {
			end = len(ranked) // last group absorbs the remainder
		}
		group := ranked[start:end]
		go func() {
			dp, err := Plan(ctx, group, links, model, c)
			resCh <- replicaResult{idx: i, plan: dp, err: err}
		}()
	}

	// Collect all results (drain fully to avoid goroutine leaks).
	plans := make([]*DeploymentPlan, replicaCount)
	errs := make([]error, replicaCount)
	for range replicaCount {
		r := <-resCh
		plans[r.idx] = r.plan
		errs[r.idx] = r.err
	}

	// Fail if any partition could not be planned.
	for i, e := range errs {
		if e != nil {
			return nil, fmt.Errorf("replica %d: %w", i, e)
		}
	}

	return &ReplicaSet{
		ModelID:  model.ID,
		Replicas: plans,
		Routing:  RoutingRoundRobin,
	}, nil
}
