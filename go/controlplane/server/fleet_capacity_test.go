package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// nodeWithHW creates a registry.Node with promoted VRAM/RAM columns and a
// serialised HardwareProfile carrying memory-bandwidth data.
func nodeWithHW(t testing.TB, id, state string, vramGB, ramGB, bwGBs float64) *registry.Node {
	t.Helper()
	hw := &purserv1.HardwareProfile{
		RamTotalGb:      ramGB,
		MemBandwidthGbs: bwGBs,
	}
	hwJSON, err := protojson.Marshal(hw)
	if err != nil {
		t.Fatalf("marshal hardware profile: %v", err)
	}
	return &registry.Node{
		ID:              id,
		Hostname:        id + ".local",
		State:           state,
		VRAMGB:          vramGB,
		RAMGB:           ramGB,
		HardwareProfile: hwJSON,
	}
}

func TestHandleFleetCapacity_NoNodes(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/capacity", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body server.FleetCapacity
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.ReadyNodes != 0 {
		t.Errorf("ready_nodes = %d, want 0", body.ReadyNodes)
	}
	if body.VRAMTotalGB != 0 {
		t.Errorf("vram_total_gb = %v, want 0", body.VRAMTotalGB)
	}
	if body.Bottleneck != "none" {
		t.Errorf("bottleneck = %q, want none", body.Bottleneck)
	}
	if body.CanFitModels == nil {
		t.Error("can_fit_models must not be nil (want empty array)")
	}
}

func TestHandleFleetCapacity_Aggregation(t *testing.T) {
	srv, reg := newTestServer(t)
	ctx := context.Background()

	// Two READY nodes.
	for _, n := range []*registry.Node{
		nodeWithHW(t, "n1", "NODE_STATE_READY", 24.0, 64.0, 400.0),
		nodeWithHW(t, "n2", "NODE_STATE_RUNNING", 24.0, 64.0, 400.0),
	} {
		if err := reg.CreateNode(ctx, n); err != nil {
			t.Fatalf("create node %s: %v", n.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/capacity", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body server.FleetCapacity
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if body.ReadyNodes != 2 {
		t.Errorf("ready_nodes = %d, want 2", body.ReadyNodes)
	}
	if body.VRAMTotalGB != 48.0 {
		t.Errorf("vram_total_gb = %v, want 48.0", body.VRAMTotalGB)
	}
	if body.RAMTotalGB != 128.0 {
		t.Errorf("ram_total_gb = %v, want 128.0", body.RAMTotalGB)
	}
	if body.MemBandwidthTotalGBs != 800.0 {
		t.Errorf("mem_bandwidth_total_gbs = %v, want 800.0", body.MemBandwidthTotalGBs)
	}
	// No active deployments → headroom == total.
	if body.VRAMHeadroomGB != 48.0 {
		t.Errorf("vram_headroom_gb = %v, want 48.0", body.VRAMHeadroomGB)
	}
	if body.VRAMUsedGB != 0 {
		t.Errorf("vram_used_gb = %v, want 0", body.VRAMUsedGB)
	}
	if body.CanFitModels == nil {
		t.Error("can_fit_models must not be nil")
	}
}

func TestHandleFleetCapacity_UsedByActiveDeployment(t *testing.T) {
	srv, reg := newTestServer(t)
	ctx := context.Background()

	if err := reg.CreateNode(ctx, nodeWithHW(t, "n1", "NODE_STATE_READY", 24.0, 64.0, 400.0)); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := reg.CreateNode(ctx, nodeWithHW(t, "n2", "NODE_STATE_READY", 24.0, 64.0, 400.0)); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := reg.CreateModel(ctx, &registry.Model{ID: "mdl-cap-1"}); err != nil {
		t.Fatalf("create model: %v", err)
	}

	// Active deployment occupying n1.
	type engineRef struct {
		NodeID string `json:"node_id"`
	}
	type detail struct {
		Engines []engineRef `json:"engines"`
	}
	detailJSON, _ := json.Marshal(detail{Engines: []engineRef{{NodeID: "n1"}}})
	if err := reg.CreateDeployment(ctx, &registry.Deployment{
		ID:      "dep-cap-1",
		ModelID: "mdl-cap-1",
		State:   purserv1.DeploymentState_DEPLOYMENT_STATE_ACTIVE.String(),
		Detail:  detailJSON,
	}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/capacity", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body server.FleetCapacity
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// n1 is used → vram_used = 24 GB.
	if body.VRAMUsedGB != 24.0 {
		t.Errorf("vram_used_gb = %v, want 24.0", body.VRAMUsedGB)
	}
	// Headroom = total - used = 48 - 24 = 24.
	if body.VRAMHeadroomGB != 24.0 {
		t.Errorf("vram_headroom_gb = %v, want 24.0", body.VRAMHeadroomGB)
	}
}

func TestHandleFleetCapacity_IgnoresNonReadyNodes(t *testing.T) {
	srv, reg := newTestServer(t)
	ctx := context.Background()

	// One READY node and one DECOMMISSIONED node; only the READY one counts.
	if err := reg.CreateNode(ctx, nodeWithHW(t, "n1", "NODE_STATE_READY", 24.0, 64.0, 400.0)); err != nil {
		t.Fatalf("create node n1: %v", err)
	}
	if err := reg.CreateNode(ctx, nodeWithHW(t, "n2", "NODE_STATE_DECOMMISSIONED", 24.0, 64.0, 400.0)); err != nil {
		t.Fatalf("create node n2: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/capacity", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var body server.FleetCapacity
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.ReadyNodes != 1 {
		t.Errorf("ready_nodes = %d, want 1", body.ReadyNodes)
	}
	if body.VRAMTotalGB != 24.0 {
		t.Errorf("vram_total_gb = %v, want 24.0 (decommissioned node must not count)", body.VRAMTotalGB)
	}
}

func TestHandleFleetCapacity_BottleneckVRAM(t *testing.T) {
	srv, reg := newTestServer(t)
	ctx := context.Background()

	// Node with small VRAM, large RAM, large bandwidth → VRAM is tightest.
	if err := reg.CreateNode(ctx, nodeWithHW(t, "n1", "NODE_STATE_READY", 4.0, 256.0, 800.0)); err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Active deployment consuming n1 → vram headroom = 0.
	if err := reg.CreateModel(ctx, &registry.Model{ID: "mdl-bn"}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	type engineRef struct {
		NodeID string `json:"node_id"`
	}
	type detail struct {
		Engines []engineRef `json:"engines"`
	}
	detailJSON, _ := json.Marshal(detail{Engines: []engineRef{{NodeID: "n1"}}})
	if err := reg.CreateDeployment(ctx, &registry.Deployment{
		ID:      "dep-bn",
		ModelID: "mdl-bn",
		State:   purserv1.DeploymentState_DEPLOYMENT_STATE_ACTIVE.String(),
		Detail:  detailJSON,
	}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/capacity", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var body server.FleetCapacity
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Bottleneck != "vram" {
		t.Errorf("bottleneck = %q, want vram", body.Bottleneck)
	}
}

// TestHandleFleetCapacity_ViewerRBACAllowed verifies that GET
// /api/v1/fleet/capacity is accessible without auth (viewer-equivalent: the
// RBAC middleware lets anonymous GETs through to the handler).
func TestHandleFleetCapacity_ViewerRBACAllowed(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/capacity", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (GET should be viewer-accessible)", rec.Code)
	}
}
