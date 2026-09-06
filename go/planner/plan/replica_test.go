package plan

import (
	"context"
	"strings"
	"testing"
)

// TestPlanReplicaSet_SingleReplica_EquivalentToPlan verifies that replicaCount=1
// is a thin wrapper around Plan(): same number of assignments, no overhead.
func TestPlanReplicaSet_SingleReplica_EquivalentToPlan(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(10)
	model := bench7BModel()

	rs, err := PlanReplicaSet(ctx, nodes, links, model, Constraints{}, 1)
	if err != nil {
		t.Fatalf("PlanReplicaSet(1): unexpected error: %v", err)
	}
	if len(rs.Replicas) != 1 {
		t.Fatalf("expected 1 replica, got %d", len(rs.Replicas))
	}
	if rs.ModelID != model.ID {
		t.Errorf("ModelID = %q, want %q", rs.ModelID, model.ID)
	}
	if rs.Routing != RoutingRoundRobin {
		t.Errorf("Routing = %v, want RoutingRoundRobin", rs.Routing)
	}

	// Must produce the same number of assignments as a direct Plan call.
	dp, err := Plan(ctx, nodes, links, model, Constraints{})
	if err != nil {
		t.Fatalf("Plan: unexpected error: %v", err)
	}
	if len(rs.Replicas[0].Assignments) != len(dp.Assignments) {
		t.Errorf("single-replica assignments = %d, want %d (same as Plan)",
			len(rs.Replicas[0].Assignments), len(dp.Assignments))
	}
}

// TestPlanReplicaSet_TwoReplicas_DisjointNodes checks that with a 20-node fleet
// and 2 replicas, each replica receives a disjoint set of nodes.
func TestPlanReplicaSet_TwoReplicas_DisjointNodes(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(20)
	model := bench7BModel() // fits on 1 node, so k_min=1 and 20 >= 1×2

	rs, err := PlanReplicaSet(ctx, nodes, links, model, Constraints{}, 2)
	if err != nil {
		t.Fatalf("PlanReplicaSet(2): unexpected error: %v", err)
	}
	if len(rs.Replicas) != 2 {
		t.Fatalf("expected 2 replicas, got %d", len(rs.Replicas))
	}

	// Collect node IDs used by replica 0.
	nodesInR0 := make(map[string]bool, len(rs.Replicas[0].Assignments))
	for _, a := range rs.Replicas[0].Assignments {
		nodesInR0[a.NodeID] = true
	}

	// Replica 1 must not reuse any node from replica 0.
	for _, a := range rs.Replicas[1].Assignments {
		if nodesInR0[a.NodeID] {
			t.Errorf("replicas must use disjoint nodes: node %q appears in both replicas", a.NodeID)
		}
	}
}

// TestPlanReplicaSet_InsufficientFleet_ReturnsError verifies the k_min guard:
// a 3-node fleet serving bench100BModel (k_min=3) cannot support 2 replicas
// (would need 6 nodes).
func TestPlanReplicaSet_InsufficientFleet_ReturnsError(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(3)
	model := bench100BModel() // k_min=3 on 24 GB VRAM nodes (q4=50 GB)

	_, err := PlanReplicaSet(ctx, nodes, links, model, Constraints{}, 2)
	if err == nil {
		t.Fatal("expected error for insufficient fleet, got nil")
	}
	if !strings.Contains(err.Error(), "fleet") {
		t.Errorf("expected error to mention 'fleet', got: %v", err)
	}
}

// TestPlanReplicaSet_ZeroReplicas_ReturnsError validates the input guard.
func TestPlanReplicaSet_ZeroReplicas_ReturnsError(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(4)
	model := bench7BModel()

	_, err := PlanReplicaSet(ctx, nodes, links, model, Constraints{}, 0)
	if err == nil {
		t.Fatal("expected error for replicaCount=0, got nil")
	}
}

// TestPlanReplicaSet_NegativeReplicas_ReturnsError validates the input guard
// for negative values.
func TestPlanReplicaSet_NegativeReplicas_ReturnsError(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(4)
	model := bench7BModel()

	_, err := PlanReplicaSet(ctx, nodes, links, model, Constraints{}, -1)
	if err == nil {
		t.Fatal("expected error for replicaCount=-1, got nil")
	}
}

// TestPlanReplicaSet_AllReplicasValid checks that every replica in a multi-replica
// set is a valid, non-nil plan with a contiguous layer cover.
func TestPlanReplicaSet_AllReplicasValid(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(20)
	model := bench7BModel()

	rs, err := PlanReplicaSet(ctx, nodes, links, model, Constraints{}, 4)
	if err != nil {
		t.Fatalf("PlanReplicaSet(4): unexpected error: %v", err)
	}
	if len(rs.Replicas) != 4 {
		t.Fatalf("expected 4 replicas, got %d", len(rs.Replicas))
	}

	for i, r := range rs.Replicas {
		if r == nil {
			t.Fatalf("replica %d is nil", i)
		}
		if len(r.Assignments) == 0 {
			t.Errorf("replica %d has no assignments", i)
		}
		checkContiguousCover(t, r.Assignments, model.Layers)
	}
}

// BenchmarkComputeFailoverPlans_Parallel measures the new parallel phase F
// against the large-fleet scenario to confirm sub-linear wall-clock growth.
func BenchmarkComputeFailoverPlans_Parallel(b *testing.B) {
	nodes, links := buildFleet(20)
	model := bench100BModel()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Plan() invokes computeFailoverPlans (phase F) internally.
		_, _ = Plan(context.Background(), nodes, links, model, Constraints{})
	}
}
