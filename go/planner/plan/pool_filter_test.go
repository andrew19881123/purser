package plan

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestPlan_AllowedNodeIDs_FiltersFleet verifies that when AllowedNodeIDs is
// set, Plan() only assigns layers to nodes in the allowed list.
func TestPlan_AllowedNodeIDs_FiltersFleet(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(10)
	model := bench7BModel()

	// Allow only the first 3 nodes.
	allowed := make([]string, 3)
	for i := 0; i < 3; i++ {
		allowed[i] = nodes[i].ID
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = true
	}

	dp, err := Plan(ctx, nodes, links, model, Constraints{AllowedNodeIDs: allowed})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}

	// All assignments must be within allowed nodes.
	for _, a := range dp.Assignments {
		if !allowedSet[a.NodeID] {
			t.Errorf("plan must only use nodes from AllowedNodeIDs, got %s", a.NodeID)
		}
	}
}

// TestPlan_AllowedNodeIDs_EmptySlice_NoRestriction verifies that an empty
// AllowedNodeIDs slice imposes no restriction (all nodes are available).
func TestPlan_AllowedNodeIDs_EmptySlice_NoRestriction(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(10)
	model := bench7BModel()

	// Empty AllowedNodeIDs = no restriction.
	dp, err := Plan(ctx, nodes, links, model, Constraints{AllowedNodeIDs: nil})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if len(dp.Assignments) == 0 {
		t.Fatal("expected at least one assignment")
	}
}

// TestPlan_AllowedNodeIDs_NoneInPool_ReturnsError verifies that AllowedNodeIDs
// containing node IDs that don't exist in the fleet returns a PlanError.
func TestPlan_AllowedNodeIDs_NoneInPool_ReturnsError(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(5)
	model := bench7BModel()

	// AllowedNodeIDs that don't exist in the fleet.
	_, err := Plan(ctx, nodes, links, model, Constraints{
		AllowedNodeIDs: []string{"nonexistent-node-1", "nonexistent-node-2"},
	})
	if err == nil {
		t.Fatal("expected a PlanError, got nil")
	}
	var pe *PlanError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PlanError, got %T: %v", err, err)
	}
	if !strings.Contains(pe.Reason, "no nodes available in team pool") {
		t.Errorf("expected error reason to contain 'no nodes available in team pool', got: %q", pe.Reason)
	}
}

// TestPlan_AllowedNodeIDs_ForceHostNotInPool_ReturnsError verifies that
// ForceHost pointing to a node outside the allowed pool returns a PlanError.
func TestPlan_AllowedNodeIDs_ForceHostNotInPool_ReturnsError(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(5)
	model := bench7BModel()

	allowed := []string{nodes[0].ID}
	forbiddenHost := nodes[1].ID // not in allowed pool

	_, err := Plan(ctx, nodes, links, model, Constraints{
		AllowedNodeIDs: allowed,
		ForceHost:      &forbiddenHost,
	})
	if err == nil {
		t.Fatal("expected a PlanError when ForceHost is outside the allowed pool, got nil")
	}
	var pe *PlanError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PlanError, got %T: %v", err, err)
	}
	if !strings.Contains(pe.Reason, "not in the team's allowed node pool") {
		t.Errorf("expected error reason to mention the pool constraint, got: %q", pe.Reason)
	}
}

// TestPlanReplicaSet_AllowedNodeIDs_Respected verifies that PlanReplicaSet
// honours AllowedNodeIDs: no replica may use a node outside the allowed pool.
func TestPlanReplicaSet_AllowedNodeIDs_Respected(t *testing.T) {
	ctx := context.Background()
	nodes, links := buildFleet(20)
	model := bench7BModel()

	// Pool of 12 nodes, 2 replicas.
	allowed := make([]string, 12)
	for i := 0; i < 12; i++ {
		allowed[i] = nodes[i].ID
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = true
	}

	rs, err := PlanReplicaSet(ctx, nodes, links, model, Constraints{AllowedNodeIDs: allowed}, 2)
	if err != nil {
		t.Fatalf("PlanReplicaSet: unexpected error: %v", err)
	}
	if len(rs.Replicas) != 2 {
		t.Fatalf("expected 2 replicas, got %d", len(rs.Replicas))
	}

	// Both replicas must only use nodes from the allowed pool.
	for i, r := range rs.Replicas {
		for _, a := range r.Assignments {
			if !allowedSet[a.NodeID] {
				t.Errorf("replica %d uses node %q which is not in AllowedNodeIDs", i, a.NodeID)
			}
		}
	}
}
