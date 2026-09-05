package reconciler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go.opentelemetry.io/otel"
)

// newTestMeterProvider sets up an in-memory MeterProvider and returns it
// together with a function that reads the collected metrics.
func newTestMeterProvider(t *testing.T) (*sdkmetric.MeterProvider, func() []metricdata.Metrics) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	collect := func() []metricdata.Metrics {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("reader.Collect: %v", err)
		}
		var out []metricdata.Metrics
		for _, sm := range rm.ScopeMetrics {
			out = append(out, sm.Metrics...)
		}
		return out
	}
	return mp, collect
}

// findMetric looks for a metric by name in the slice returned by collect().
func findMetric(metrics []metricdata.Metrics, name string) (metricdata.Metrics, bool) {
	for _, m := range metrics {
		if m.Name == name {
			return m, true
		}
	}
	return metricdata.Metrics{}, false
}

// TestDispatchIncrementsEventsDetected verifies that dispatch() increments
// purser.reconciler.events_detected regardless of the automation level.
func TestDispatchIncrementsEventsDetected(t *testing.T) {
	mp, collect := newTestMeterProvider(t)
	oldMP := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(oldMP) })

	reg := openReg(t)
	ctx := context.Background()

	act := &mockActuator{}
	cfg := reconciler.DefaultConfig()
	cfg.Interval = time.Hour // prevent automatic ticking
	cfg.FailureThreshold = 1
	cfg.Hysteresis = 0 // fire immediately
	cfg.NodeTimeout = time.Millisecond

	rc := reconciler.New(reg, act, cfg)
	rc.SetClock(func() time.Time { return time.Now() })

	// Seed an active deployment referencing a node that is down.
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:       "n1",
		Hostname: "h1",
		State:    "NODE_STATE_RUNNING",
		LastSeen: time.Now().Add(-24 * time.Hour), // stale → node_down
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	detail, _ := json.Marshal(orchestrator.DeploymentDetail{
		ModelID: "m1",
		Engines: []orchestrator.EngineRef{{NodeID: "n1", Role: "host"}},
	})
	if err := reg.CreateModel(ctx, &registry.Model{ID: "m1"}); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if err := reg.CreateDeployment(ctx, &registry.Deployment{
		ID:      "dep-1",
		ModelID: "m1",
		State:   orchestrator.StateActive,
		Detail:  detail,
	}); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	if _, err := rc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	metrics := collect()
	m, ok := findMetric(metrics, "purser.reconciler.events_detected")
	if !ok {
		t.Fatal("purser.reconciler.events_detected metric not found")
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("unexpected data type %T for events_detected", m.Data)
	}
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	if total == 0 {
		t.Error("events_detected counter should be > 0 after a reconcile pass with a node_down event")
	}
}

// TestDispatchIncrementsEventsActed verifies that events_acted is incremented
// when the reconciler actually performs an action (AutomationAuto + engine_down
// → RestartEngine).
func TestDispatchIncrementsEventsActed(t *testing.T) {
	mp, collect := newTestMeterProvider(t)
	oldMP := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(oldMP) })

	reg := openReg(t)
	ctx := context.Background()

	act := &mockActuator{}
	cfg := reconciler.DefaultConfig()
	cfg.Interval = time.Hour
	cfg.FailureThreshold = 1
	cfg.Hysteresis = 0
	cfg.NodeTimeout = 24 * time.Hour // node is NOT down (last_seen recent)

	rc := reconciler.New(reg, act, cfg)
	ts := time.Now()
	rc.SetClock(func() time.Time { return ts })

	// Seed a node that is READY (not down) but whose state shows engine_down.
	// engine_down fires when the node is alive but engineHealthy() == false
	// (state not RUNNING or READY). Use DRAINING to trigger engine_down.
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:       "n2",
		Hostname: "h2",
		State:    "NODE_STATE_DRAINING", // not RUNNING/READY → engine_down
		LastSeen: ts,
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	detail, _ := json.Marshal(orchestrator.DeploymentDetail{
		ModelID: "m2",
		Engines: []orchestrator.EngineRef{{NodeID: "n2", Role: "host"}},
	})
	if err := reg.CreateModel(ctx, &registry.Model{ID: "m2"}); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if err := reg.CreateDeployment(ctx, &registry.Deployment{
		ID:      "dep-2",
		ModelID: "m2",
		State:   orchestrator.StateActive,
		Detail:  detail,
	}); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	if _, err := rc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	metrics := collect()
	m, ok := findMetric(metrics, "purser.reconciler.events_acted")
	if !ok {
		t.Fatal("purser.reconciler.events_acted metric not found")
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("unexpected data type %T for events_acted", m.Data)
	}
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	if total == 0 {
		t.Error("events_acted counter should be > 0 after an engine_down action")
	}
}

// TestReconcileLoopDuration verifies that purser.reconciler.loop_duration_ms
// histogram is populated after a Reconcile() call.
func TestReconcileLoopDuration(t *testing.T) {
	mp, collect := newTestMeterProvider(t)
	oldMP := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(oldMP) })

	reg := openReg(t)

	cfg := reconciler.DefaultConfig()
	cfg.Interval = time.Hour
	rc := reconciler.New(reg, nil, cfg)

	if _, err := rc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	metrics := collect()
	_, ok := findMetric(metrics, "purser.reconciler.loop_duration_ms")
	if !ok {
		t.Error("purser.reconciler.loop_duration_ms histogram not found after Reconcile()")
	}
}
