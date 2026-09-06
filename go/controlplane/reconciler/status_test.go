package reconciler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
)

// TestStatus_EmptyTracker verifies that Status() returns sensible defaults
// when the reconciler has never run a pass (no discrepancies tracked).
func TestStatus_EmptyTracker(t *testing.T) {
	reg := openReg(t)
	rc := reconciler.New(reg, nil, reconciler.DefaultConfig())

	st := rc.Status()

	// Config must reflect DefaultConfig values.
	cfg := reconciler.DefaultConfig()
	if st.Config.IntervalS != int(cfg.Interval.Seconds()) {
		t.Errorf("IntervalS = %d, want %d", st.Config.IntervalS, int(cfg.Interval.Seconds()))
	}
	if st.Config.NodeTimeoutS != int(cfg.NodeTimeout.Seconds()) {
		t.Errorf("NodeTimeoutS = %d, want %d", st.Config.NodeTimeoutS, int(cfg.NodeTimeout.Seconds()))
	}
	if st.Config.HysteresisS != int(cfg.Hysteresis.Seconds()) {
		t.Errorf("HysteresisS = %d, want %d", st.Config.HysteresisS, int(cfg.Hysteresis.Seconds()))
	}
	if st.Config.ActionCooldownS != int(cfg.ActionCooldown.Seconds()) {
		t.Errorf("ActionCooldownS = %d, want %d", st.Config.ActionCooldownS, int(cfg.ActionCooldown.Seconds()))
	}

	// Tracker must be empty (no discrepancies observed yet).
	if len(st.Tracker) != 0 {
		t.Errorf("Tracker len = %d, want 0; got %v", len(st.Tracker), st.Tracker)
	}
}

// TestStatus_WithTrackedDiscrepancy verifies that after a reconcile pass
// detects a discrepancy, Status() reports it in the Tracker with the correct
// event type and a non-zero tracked count.
func TestStatus_WithTrackedDiscrepancy(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()

	// Seed: an active deployment on a node that has timed out (NodeDown).
	nodeID := "node-status-test"
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:       nodeID,
		Hostname: "h1",
		State:    "NODE_STATE_RUNNING",
		// LastSeen intentionally zero → treated as never seen → times out
		// after NodeTimeout from CreatedAt.
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	detail := orchestrator.DeploymentDetail{
		ModelID: "model-status",
		Engines: []orchestrator.EngineRef{{NodeID: nodeID, Role: "host"}},
	}
	b, _ := json.Marshal(detail)
	if err := reg.CreateModel(ctx, &registry.Model{ID: "model-status"}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := reg.CreateDeployment(ctx, &registry.Deployment{
		ID:      "dep-status",
		ModelID: "model-status",
		PlanID:  "plan-s",
		State:   orchestrator.StateActive,
		Detail:  b,
	}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Use a very short timeout so the node is immediately considered down, and a
	// very long hysteresis so no action fires (we only want the tracker to fill).
	cfg := reconciler.Config{
		Interval:         time.Second,
		NodeTimeout:      time.Millisecond, // node created at t=0; any non-zero elapsed > 1ms
		Hysteresis:       24 * time.Hour,   // never act
		FailureThreshold: 999,
		ActionCooldown:   24 * time.Hour,
		Levels:           reconciler.DefaultLevels(),
	}
	rc := reconciler.New(reg, nil, cfg)
	// Advance time well past the node timeout.
	rc.SetClock(func() time.Time { return time.Now().Add(10 * time.Minute) })

	if _, err := rc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	st := rc.Status()

	// There must be at least one entry in the tracker (node_down).
	if len(st.Tracker) == 0 {
		t.Fatal("Tracker must be non-empty after reconcile detects a discrepancy")
	}
	ndSum, ok := st.Tracker[string(reconciler.EventNodeDown)]
	if !ok {
		t.Fatalf("expected node_down in Tracker; got %v", st.Tracker)
	}
	if ndSum.Tracked < 1 {
		t.Errorf("Tracker[node_down].Tracked = %d, want >= 1", ndSum.Tracked)
	}
	if ndSum.OldestAgeS < 0 {
		t.Errorf("Tracker[node_down].OldestAgeS = %f, want >= 0", ndSum.OldestAgeS)
	}
}

// TestStatus_JSONRoundTrip verifies that ReconcilerStatus is JSON-serialisable
// (no unexported fields, zero-value maps marshal to {} not null).
func TestStatus_JSONRoundTrip(t *testing.T) {
	reg := openReg(t)
	rc := reconciler.New(reg, nil, reconciler.DefaultConfig())

	st := rc.Status()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["config"]; !ok {
		t.Error("JSON must contain 'config'")
	}
	if _, ok := m["tracker"]; !ok {
		t.Error("JSON must contain 'tracker'")
	}
}
