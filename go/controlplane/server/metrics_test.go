package server_test

// Unit tests for the SSE metrics endpoint and the NodeMetricsGetter wiring.
//
// Coverage:
//   - metricsSnapshotFromCache: a node with live metrics emits real values.
//   - metricsSnapshotFromCache: a node with no heartbeat emits zero metrics.
//   - handleMetricsSSE: the JSON shape has node_id, state, and a nested
//     metrics object with the expected fields.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// fakeMetricsGetter is a test double for NodeMetricsGetter.
type fakeMetricsGetter struct {
	data map[string]fleet.NodeMetrics
}

func (f *fakeMetricsGetter) Get(nodeID string) (fleet.NodeMetrics, bool) {
	m, ok := f.data[nodeID]
	return m, ok
}

func TestMetricsSSE_RealData(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()

	// Seed two nodes.
	_ = reg.CreateNode(ctx, &registry.Node{ID: "n1", State: "NODE_STATE_RUNNING"})
	_ = reg.CreateNode(ctx, &registry.Node{ID: "n2", State: "NODE_STATE_READY"})

	// Only n1 has reported metrics; n2 is silent.
	getter := &fakeMetricsGetter{data: map[string]fleet.NodeMetrics{
		"n1": {
			NodeID:     "n1",
			State:      "NODE_STATE_RUNNING",
			DecodeTps:  42.5,
			RAMUsedGB:  16.0,
			VRAMUsedGB: 8.0,
			QueueDepth: 3,
		},
	}}

	srv := server.New(reg, server.Config{
		NodeMetrics:     getter,
		MetricsInterval: 50 * time.Millisecond,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, ts.URL+"/api/v1/metrics", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read first SSE line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first SSE line = %q, want data: prefix", line)
	}

	payload := strings.TrimPrefix(strings.TrimRight(line, "\r\n"), "data: ")
	var snap map[string]any
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		t.Fatalf("unmarshal SSE frame: %v; raw=%q", err, payload)
	}

	// Top-level fields.
	if _, ok := snap["at"]; !ok {
		t.Error("SSE frame missing 'at' field")
	}
	if _, ok := snap["aggregate_decode_tok_s"]; !ok {
		t.Error("SSE frame missing 'aggregate_decode_tok_s' field")
	}

	// Node array: both nodes must appear.
	nodes, ok := snap["nodes"].([]any)
	if !ok {
		t.Fatalf("'nodes' is not an array: %T", snap["nodes"])
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes count = %d, want 2", len(nodes))
	}

	// Build a map of nodeID -> node entry for assertions.
	byID := map[string]map[string]any{}
	for _, raw := range nodes {
		n, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("node entry is not a map: %T", raw)
		}
		id, _ := n["node_id"].(string)
		byID[id] = n
	}

	// n1: must have real decode metrics.
	n1, ok := byID["n1"]
	if !ok {
		t.Fatal("n1 missing from nodes")
	}
	n1m, ok := n1["metrics"].(map[string]any)
	if !ok {
		t.Fatal("n1.metrics is not a map")
	}
	if n1m["decode_tok_s"] != 42.5 {
		t.Errorf("n1.decode_tok_s = %v, want 42.5", n1m["decode_tok_s"])
	}

	// n2: must appear with zero metrics (honest zero-fill).
	n2, ok := byID["n2"]
	if !ok {
		t.Fatal("n2 missing from nodes (silent nodes must still appear)")
	}
	n2m, ok := n2["metrics"].(map[string]any)
	if !ok {
		t.Fatal("n2.metrics is not a map")
	}
	if n2m["decode_tok_s"] != 0.0 {
		t.Errorf("n2.decode_tok_s = %v, want 0 (node has not reported)", n2m["decode_tok_s"])
	}
}

func TestMetricsSSE_NoNodeMetrics_FallbackSummary(t *testing.T) {
	// When NodeMetrics is not configured, the endpoint must still emit valid SSE
	// (the registry-state summary fallback).
	reg := newReg(t)
	_ = reg.CreateNode(context.Background(), &registry.Node{ID: "n1", State: "NODE_STATE_READY"})
	srv := server.New(reg, server.Config{MetricsInterval: 50 * time.Millisecond})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/metrics", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read first SSE line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Errorf("first SSE line = %q, want data: prefix", line)
	}
}

func TestNodeMetricsGetter_UpdateAndGet(t *testing.T) {
	// Verify that LiveMetrics.Get returns the most recent heartbeat data.
	lm := fleet.NewLiveMetrics()

	// Not yet reported.
	if _, ok := lm.Get("node-x"); ok {
		t.Error("Get must return ok=false for a node that has never reported")
	}

	// After an update.
	lm.Update("node-x", "NODE_STATE_RUNNING", nil, nil, time.Now())
	m, ok := lm.Get("node-x")
	if !ok {
		t.Fatal("Get must return ok=true after Update")
	}
	if m.NodeID != "node-x" {
		t.Errorf("NodeID = %q, want node-x", m.NodeID)
	}
	if m.State != "NODE_STATE_RUNNING" {
		t.Errorf("State = %q, want NODE_STATE_RUNNING", m.State)
	}
}
