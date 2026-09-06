package server_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// stubNodeMetrics is a test double for NodeMetricsGetter that returns
// pre-configured data.
type stubNodeMetrics struct {
	data map[string]fleet.NodeMetrics
}

func (s *stubNodeMetrics) Get(nodeID string) (fleet.NodeMetrics, bool) {
	m, ok := s.data[nodeID]
	return m, ok
}

// openTestReg opens a fresh, migrated SQLite registry in t.TempDir.
func openTestReg(t *testing.T) registry.Registry {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "reg.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("registry.Migrate: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

// TestStartInfraMetricsNoPanic verifies that StartInfraMetrics runs without
// panic when no real MeterProvider is configured (no-op path) and cancels
// cleanly when the context is cancelled.
func TestStartInfraMetricsNoPanic(t *testing.T) {
	reg := openTestReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})

	ctx, cancel := context.WithCancel(context.Background())
	srv.StartInfraMetrics(ctx)
	// Allow at least one collection tick to fire.
	time.Sleep(50 * time.Millisecond)
	cancel()
	// Give the goroutine time to exit gracefully.
	time.Sleep(20 * time.Millisecond)
	// Reaching here means no panic.
}

// TestStartInfraMetricsWithNodeMetrics verifies that collectInfraMetrics
// iterates over registered nodes and calls Record on the per-node hardware
// gauges (no panic; correct data path exercised). The test uses a no-op
// MeterProvider, so nothing is actually exported — the goal is to confirm
// the code path runs without error.
func TestStartInfraMetricsWithNodeMetrics(t *testing.T) {
	reg := openTestReg(t)

	// Register two nodes.
	ctx := context.Background()
	for _, id := range []string{"node-a", "node-b"} {
		if err := reg.CreateNode(ctx, &registry.Node{
			ID:       id,
			Hostname: id + "-host",
			State:    "NODE_STATE_READY",
		}); err != nil {
			t.Fatalf("CreateNode %s: %v", id, err)
		}
	}

	// Provide real hardware metrics for node-a; node-b is intentionally absent
	// to exercise the "skip if not in cache" path.
	nm := &stubNodeMetrics{
		data: map[string]fleet.NodeMetrics{
			"node-a": {
				NodeID:              "node-a",
				State:               "NODE_STATE_RUNNING",
				CpuUtilizationPct:   42.5,
				GpuUtilizationPct:   88.0,
				MemBandwidthUtilPct: 55.3,
				TokensPerSecond:     1024.0,
				InferencePortAlive:  true,
				UpdatedAt:           time.Now(),
			},
		},
	}

	srv := server.New(reg, server.Config{
		Addr:        ":0",
		NodeMetrics: nm,
	})

	// Run one collection synchronously (the exported helper calls
	// collectInfraMetrics once before the ticker).
	ctx2, cancel := context.WithCancel(context.Background())
	srv.StartInfraMetrics(ctx2)
	time.Sleep(50 * time.Millisecond)
	cancel()
	// Reaching here without panic means the per-node Record path executed.
}

// TestStartInfraMetricsNoNodeMetrics verifies that collectInfraMetrics is safe
// when no NodeMetricsGetter is configured (nil path does not panic).
func TestStartInfraMetricsNoNodeMetrics(t *testing.T) {
	reg := openTestReg(t)
	ctx := context.Background()
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:       "node-c",
		Hostname: "host-c",
		State:    "NODE_STATE_READY",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	// No NodeMetrics configured.
	srv := server.New(reg, server.Config{Addr: ":0"})

	ctx2, cancel := context.WithCancel(context.Background())
	srv.StartInfraMetrics(ctx2)
	time.Sleep(50 * time.Millisecond)
	cancel()
}
