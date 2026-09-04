package reconciler_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
)

// --- mock actuator ---------------------------------------------------------

type mockActuator struct {
	restarts  [][2]string
	failovers []string
	cleanups  []string
	err       error
}

func (m *mockActuator) RestartEngine(_ context.Context, dep, node string) error {
	m.restarts = append(m.restarts, [2]string{dep, node})
	return m.err
}
func (m *mockActuator) Failover(_ context.Context, dep string) error {
	m.failovers = append(m.failovers, dep)
	return m.err
}
func (m *mockActuator) Cleanup(_ context.Context, dep string) error {
	m.cleanups = append(m.cleanups, dep)
	return m.err
}

// --- helpers ---------------------------------------------------------------

func openReg(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "reg.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

func seedActiveDeployment(t *testing.T, reg registry.Registry, modelID, nodeID string, createModel bool) string {
	t.Helper()
	ctx := context.Background()
	if createModel {
		if err := reg.CreateModel(ctx, &registry.Model{ID: modelID}); err != nil {
			t.Fatalf("create model: %v", err)
		}
	}
	detail := orchestrator.DeploymentDetail{
		ModelID: modelID,
		Engines: []orchestrator.EngineRef{{NodeID: nodeID, Role: "host", AgentAddr: "1.2.3.4:9443", Handle: "h"}},
	}
	b, _ := json.Marshal(detail)
	dep := &registry.Deployment{ID: "dep-1", ModelID: modelID, PlanID: "plan-1", State: orchestrator.StateActive, Detail: b}
	if err := reg.CreateDeployment(ctx, dep); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return dep.ID
}

func countType(evs []reconciler.Event, typ reconciler.EventType) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// --- tests -----------------------------------------------------------------

func TestReconcile_HealthyConverges(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedActiveDeployment(t, reg, "m1", "n1", true)
	_ = reg.CreateNode(ctx, &registry.Node{ID: "n1", Hostname: "h1", State: "NODE_STATE_RUNNING", LastSeen: now})

	act := &mockActuator{}
	rc := reconciler.New(reg, act, reconciler.DefaultConfig())
	rc.SetClock(func() time.Time { return now })

	rep, err := rc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rep.Detected) != 0 {
		t.Errorf("healthy cluster should have no discrepancies, got %+v", rep.Detected)
	}
	if len(act.restarts)+len(act.failovers)+len(act.cleanups) != 0 {
		t.Errorf("no actions expected on healthy cluster")
	}
}

func TestReconcile_EngineDownHysteresisThenAutoRestart(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	base := time.Now().UTC()
	depID := seedActiveDeployment(t, reg, "m1", "n1", true)
	// Node is alive (fresh heartbeat) but its engine is DEGRADED.
	_ = reg.CreateNode(ctx, &registry.Node{ID: "n1", Hostname: "h1", State: "NODE_STATE_DEGRADED", LastSeen: base})

	cfg := reconciler.DefaultConfig()
	cfg.Hysteresis = 30 * time.Second
	cfg.FailureThreshold = 3
	cfg.NodeTimeout = 45 * time.Second
	// engine_down defaults to Auto.

	clock := base
	act := &mockActuator{}
	rc := reconciler.New(reg, act, cfg)
	rc.SetClock(func() time.Time { return clock })

	// Pass 1 (t0): detected but within hysteresis → no action.
	rep, _ := rc.Reconcile(ctx)
	if countType(rep.Detected, reconciler.EventEngineDown) != 1 {
		t.Fatalf("pass1: expected engine_down detected, got %+v", rep.Detected)
	}
	if len(act.restarts) != 0 {
		t.Fatalf("pass1: must not act within hysteresis")
	}

	// Pass 2 (t0+5s): still within count/time thresholds.
	clock = base.Add(5 * time.Second)
	_, _ = rc.Reconcile(ctx)
	if len(act.restarts) != 0 {
		t.Fatalf("pass2: must not act yet, restarts=%v", act.restarts)
	}

	// Pass 3 (t0+35s): count>=3 and dwell>=30s → Auto restart.
	clock = base.Add(35 * time.Second)
	rep, _ = rc.Reconcile(ctx)
	if len(act.restarts) != 1 || act.restarts[0] != [2]string{depID, "n1"} {
		t.Fatalf("pass3: expected one restart of (%s,n1), got %v", depID, act.restarts)
	}
}

func TestReconcile_NodeDownRequiresApproval(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedActiveDeployment(t, reg, "m1", "n1", true)
	// Stale heartbeat → node considered down.
	_ = reg.CreateNode(ctx, &registry.Node{ID: "n1", Hostname: "h1", State: "NODE_STATE_RUNNING", LastSeen: now.Add(-10 * time.Minute)})

	cfg := reconciler.DefaultConfig()
	cfg.Hysteresis = 0
	cfg.FailureThreshold = 1
	// node_down defaults to ApprovalRequired.

	act := &mockActuator{}
	rc := reconciler.New(reg, act, cfg)
	rc.SetClock(func() time.Time { return now })

	rep, _ := rc.Reconcile(ctx)
	if countType(rep.Detected, reconciler.EventNodeDown) != 1 {
		t.Fatalf("expected node_down detected, got %+v", rep.Detected)
	}
	if len(act.failovers) != 0 {
		t.Errorf("node_down is ApprovalRequired: failover must NOT run automatically, got %v", act.failovers)
	}
	if len(rep.Acted) != 0 {
		t.Errorf("no autonomous action expected, got %+v", rep.Acted)
	}
}

func TestReconcile_NodeDownAutoFailoverWhenPolicyAllows(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	now := time.Now().UTC()
	depID := seedActiveDeployment(t, reg, "m1", "n1", true)
	_ = reg.CreateNode(ctx, &registry.Node{ID: "n1", Hostname: "h1", State: "NODE_STATE_UNREACHABLE", LastSeen: now})

	cfg := reconciler.DefaultConfig()
	cfg.Hysteresis = 0
	cfg.FailureThreshold = 1
	cfg.Levels[reconciler.EventNodeDown] = reconciler.AutomationAuto

	act := &mockActuator{}
	rc := reconciler.New(reg, act, cfg)
	rc.SetClock(func() time.Time { return now })

	rep, _ := rc.Reconcile(ctx)
	if len(act.failovers) != 1 || act.failovers[0] != depID {
		t.Fatalf("expected auto failover of %s, got %v", depID, act.failovers)
	}
	if len(rep.Acted) != 1 {
		t.Errorf("expected 1 acted event, got %+v", rep.Acted)
	}
}

func TestReconcile_OrphanDeploymentDetected(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// Deployment references a model that does not exist.
	seedActiveDeployment(t, reg, "gone", "n1", false)
	_ = reg.CreateNode(ctx, &registry.Node{ID: "n1", State: "NODE_STATE_RUNNING", LastSeen: now})

	cfg := reconciler.DefaultConfig()
	cfg.Hysteresis = 0
	cfg.FailureThreshold = 1
	cfg.Levels[reconciler.EventOrphanDeployment] = reconciler.AutomationAuto

	act := &mockActuator{}
	rc := reconciler.New(reg, act, cfg)
	rc.SetClock(func() time.Time { return now })

	rep, _ := rc.Reconcile(ctx)
	if countType(rep.Detected, reconciler.EventOrphanDeployment) != 1 {
		t.Fatalf("expected orphan_deployment detected, got %+v", rep.Detected)
	}
	if len(act.cleanups) != 1 {
		t.Errorf("expected cleanup of orphan, got %v", act.cleanups)
	}
}

func TestReconcile_HealedDiscrepancyResetsCounter(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	base := time.Now().UTC()
	seedActiveDeployment(t, reg, "m1", "n1", true)
	node := &registry.Node{ID: "n1", Hostname: "h1", State: "NODE_STATE_DEGRADED", LastSeen: base}
	_ = reg.CreateNode(ctx, node)

	cfg := reconciler.DefaultConfig()
	cfg.Hysteresis = 10 * time.Second
	cfg.FailureThreshold = 3

	clock := base
	act := &mockActuator{}
	rc := reconciler.New(reg, act, cfg)
	rc.SetClock(func() time.Time { return clock })

	// Pass 1: engine_down observed (count=1).
	_, _ = rc.Reconcile(ctx)

	// Heal: node becomes healthy; pass 2 observes nothing → counter resets.
	node.State = "NODE_STATE_RUNNING"
	node.LastSeen = base.Add(20 * time.Second)
	_ = reg.UpdateNode(ctx, node)
	clock = base.Add(20 * time.Second)
	_, _ = rc.Reconcile(ctx)

	// Regress: DEGRADED again; pass 3 must start counting from 1, not act.
	node.State = "NODE_STATE_DEGRADED"
	node.LastSeen = base.Add(40 * time.Second)
	_ = reg.UpdateNode(ctx, node)
	clock = base.Add(40 * time.Second)
	_, _ = rc.Reconcile(ctx)

	if len(act.restarts) != 0 {
		t.Errorf("counter should have reset on heal; no restart expected, got %v", act.restarts)
	}
}
