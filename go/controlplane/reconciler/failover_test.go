package reconciler_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// mockFailoverPlanner is a test double for reconciler.FailoverPlanner.
// Set err to simulate a planning failure (no capacity); leave nil for success.
type mockFailoverPlanner struct {
	planID string
	err    error
}

func (m *mockFailoverPlanner) Plan(_ context.Context, modelID string) (*purserv1.DeploymentPlan, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &purserv1.DeploymentPlan{
		PlanId:  m.planID,
		ModelId: modelID,
		Assignments: []*purserv1.Assignment{
			{NodeId: "spare-node", Role: purserv1.Role_ROLE_HOST},
		},
	}, nil
}

// failoverDetail decodes the orchestrator.DeploymentDetail from a registry
// deployment. Used to inspect FailoverPlanID after reconcile.
func failoverDetail(t *testing.T, dep *registry.Deployment) orchestrator.DeploymentDetail {
	t.Helper()
	var d orchestrator.DeploymentDetail
	if err := json.Unmarshal(dep.Detail, &d); err != nil {
		t.Fatalf("decode deployment detail: %v", err)
	}
	return d
}

// TestNodeDown_WithSpareNode verifies that when a node fails and the planner
// finds an alternate plan the reconciler:
//   - persists the new plan in the registry,
//   - stamps the deployment's FailoverPlanID,
//   - calls the Actuator's Failover.
func TestNodeDown_WithSpareNode(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed model + active deployment on the failing node.
	depID := seedActiveDeployment(t, reg, "m1", "node-down", true)

	// The failing node is UNREACHABLE (planner will not include it).
	_ = reg.CreateNode(ctx, &registry.Node{
		ID: "node-down", State: "NODE_STATE_UNREACHABLE", LastSeen: now,
	})

	cfg := reconciler.DefaultConfig()
	cfg.Hysteresis = 0
	cfg.FailureThreshold = 1
	cfg.Levels[reconciler.EventNodeDown] = reconciler.AutomationAuto

	act := &mockActuator{}
	planner := &mockFailoverPlanner{planID: "plan-fo-base"}

	rc := reconciler.New(reg, act, cfg)
	rc.SetPlanner(planner)
	rc.SetClock(func() time.Time { return now })

	rep, err := rc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rep.Acted) != 1 {
		t.Errorf("expected 1 acted event, got %+v", rep.Acted)
	}

	// Actuator.Failover must have been called for the deployment.
	if len(act.failovers) != 1 || act.failovers[0] != depID {
		t.Fatalf("expected Failover(%s), got %v", depID, act.failovers)
	}

	// The deployment detail must carry a non-empty FailoverPlanID.
	dep, err := reg.GetDeployment(ctx, depID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	detail := failoverDetail(t, dep)
	if detail.FailoverPlanID == "" {
		t.Error("FailoverPlanID should be set in deployment detail after failover planning")
	}

	// The plan must have been persisted in the registry.
	if _, err := reg.GetPlan(ctx, detail.FailoverPlanID); err != nil {
		t.Errorf("plan %q not found in registry: %v", detail.FailoverPlanID, err)
	}
}

// TestNodeDown_NoCapacity verifies that when the planner cannot produce a
// plan (fleet exhausted / no spare nodes) the reconciler emits a
// reconciler.failover.no_capacity audit event and does NOT call the Actuator.
func TestNodeDown_NoCapacity(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedActiveDeployment(t, reg, "m1", "node-down", true)
	_ = reg.CreateNode(ctx, &registry.Node{
		ID: "node-down", State: "NODE_STATE_UNREACHABLE", LastSeen: now,
	})

	cfg := reconciler.DefaultConfig()
	cfg.Hysteresis = 0
	cfg.FailureThreshold = 1
	cfg.Levels[reconciler.EventNodeDown] = reconciler.AutomationAuto

	act := &mockActuator{}
	planner := &mockFailoverPlanner{err: errors.New("fleet: no nodes available")}

	rc := reconciler.New(reg, act, cfg)
	rc.SetPlanner(planner)
	rc.SetClock(func() time.Time { return now })

	rep, err := rc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// node_down must still be detected.
	if n := countType(rep.Detected, reconciler.EventNodeDown); n != 1 {
		t.Errorf("expected 1 node_down detected, got %d", n)
	}
	// No autonomous action must have been taken.
	if len(act.failovers) != 0 {
		t.Errorf("Failover must not be called when capacity is unavailable, got %v", act.failovers)
	}
	if len(rep.Acted) != 0 {
		t.Errorf("no acted events expected, got %+v", rep.Acted)
	}

	// A no_capacity audit event must have been emitted.
	auditRows, aerr := reg.ListAudit(ctx, 10)
	if aerr != nil {
		t.Fatalf("ListAudit: %v", aerr)
	}
	var found bool
	for _, row := range auditRows {
		if row.Action == "reconciler.failover.no_capacity" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected reconciler.failover.no_capacity audit event, none found")
	}
}
