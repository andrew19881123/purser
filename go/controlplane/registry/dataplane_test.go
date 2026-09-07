package registry_test

// dataplane_test.go — tests for the v0.5 DataPlane data layer.
//
// Test coverage:
//   - CreateDataPlane: happy path, join token generated, status "registering"
//   - GetDataPlane: not found returns ErrNotFound
//   - UpdateDataPlane: mutable fields updated
//   - DeleteDataPlane: delete + not found
//   - RecordDataPlaneHeartbeat: status and last_heartbeat updated
//   - GetDataPlaneConfigSnapshot: empty default returned when none set
//   - UpdateDataPlaneConfigSnapshot + GetDataPlaneConfigSnapshot: round-trip
//   - AssignNodeToDataPlane: assign and unassign
//   - ListNodesByDataPlane: filters correctly
//   - ValidateDataPlaneToken: correct token matches, wrong token ErrNotFound

import (
	"context"
	"errors"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func mustCreateDP(t *testing.T, reg registry.Registry, name string) (*registry.DataPlane, string) {
	t.Helper()
	dp := &registry.DataPlane{
		Name:       name,
		Tier:       "production",
		GatewayURL: "https://dp.example.com",
	}
	tok, err := reg.CreateDataPlane(context.Background(), dp)
	if err != nil {
		t.Fatalf("CreateDataPlane %q: %v", name, err)
	}
	return dp, tok
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestCreateDataPlane_HappyPath(t *testing.T) {
	ctx := context.Background()
	r := openTemp(t)

	dp, tok := mustCreateDP(t, r, "prod-cluster")

	if dp.ID == "" {
		t.Fatal("ID should be assigned")
	}
	if dp.JoinTokenHash == "" {
		t.Fatal("JoinTokenHash should be set")
	}
	if tok == "" || len(tok) < 10 {
		t.Fatalf("join token should be non-empty, got %q", tok)
	}
	if dp.Status != "registering" {
		t.Errorf("status want %q got %q", "registering", dp.Status)
	}
	if dp.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be set")
	}

	// Verify we can read it back.
	got, err := r.GetDataPlane(ctx, dp.ID)
	if err != nil {
		t.Fatalf("GetDataPlane: %v", err)
	}
	if got.Name != "prod-cluster" {
		t.Errorf("name want %q got %q", "prod-cluster", got.Name)
	}
}

func TestGetDataPlane_NotFound(t *testing.T) {
	r := openTemp(t)
	_, err := r.GetDataPlane(context.Background(), "nonexistent")
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListDataPlanes(t *testing.T) {
	r := openTemp(t)
	mustCreateDP(t, r, "alpha")
	mustCreateDP(t, r, "beta")

	dps, err := r.ListDataPlanes(context.Background())
	if err != nil {
		t.Fatalf("ListDataPlanes: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("want 2 data planes, got %d", len(dps))
	}
}

func TestUpdateDataPlane(t *testing.T) {
	ctx := context.Background()
	r := openTemp(t)
	dp, _ := mustCreateDP(t, r, "staging")

	dp.Status = "active"
	dp.Tier = "staging"
	if err := r.UpdateDataPlane(ctx, dp); err != nil {
		t.Fatalf("UpdateDataPlane: %v", err)
	}
	got, _ := r.GetDataPlane(ctx, dp.ID)
	if got.Status != "active" {
		t.Errorf("status want %q got %q", "active", got.Status)
	}
	if got.Tier != "staging" {
		t.Errorf("tier want %q got %q", "staging", got.Tier)
	}
}

func TestUpdateDataPlane_NotFound(t *testing.T) {
	r := openTemp(t)
	err := r.UpdateDataPlane(context.Background(), &registry.DataPlane{ID: "ghost"})
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteDataPlane(t *testing.T) {
	ctx := context.Background()
	r := openTemp(t)
	dp, _ := mustCreateDP(t, r, "dev")

	if err := r.DeleteDataPlane(ctx, dp.ID); err != nil {
		t.Fatalf("DeleteDataPlane: %v", err)
	}
	if _, err := r.GetDataPlane(ctx, dp.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestDeleteDataPlane_NotFound(t *testing.T) {
	r := openTemp(t)
	err := r.DeleteDataPlane(context.Background(), "ghost")
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRecordDataPlaneHeartbeat(t *testing.T) {
	ctx := context.Background()
	r := openTemp(t)
	dp, _ := mustCreateDP(t, r, "hb-dp")

	hb := &registry.DataPlaneHeartbeat{
		DataPlaneID:  dp.ID,
		Status:       "active",
		NodeCount:    4,
		ActiveModels: []string{"llama3-8b"},
	}
	if err := r.RecordDataPlaneHeartbeat(ctx, hb); err != nil {
		t.Fatalf("RecordDataPlaneHeartbeat: %v", err)
	}
	got, err := r.GetDataPlane(ctx, dp.ID)
	if err != nil {
		t.Fatalf("GetDataPlane after heartbeat: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("status want %q got %q", "active", got.Status)
	}
	if got.LastHeartbeat == nil {
		t.Fatal("LastHeartbeat should be set after heartbeat")
	}
}

func TestRecordDataPlaneHeartbeat_NotFound(t *testing.T) {
	r := openTemp(t)
	hb := &registry.DataPlaneHeartbeat{DataPlaneID: "ghost", Status: "active"}
	err := r.RecordDataPlaneHeartbeat(context.Background(), hb)
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestConfigSnapshot_RoundTrip(t *testing.T) {
	ctx := context.Background()
	r := openTemp(t)
	dp, _ := mustCreateDP(t, r, "cfg-dp")

	// Empty snapshot returned when none set.
	snap, err := r.GetDataPlaneConfigSnapshot(ctx, dp.ID)
	if err != nil {
		t.Fatalf("GetDataPlaneConfigSnapshot (empty): %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot even when unset")
	}

	// Push a snapshot.
	newSnap := &registry.DataPlaneConfigSnapshot{
		RoutingTable: map[string]any{"llama3-8b": "node-1"},
		AuthBundle:   map[string]any{"key-abc": true},
		PolicyBundle: []string{"allow_inference"},
	}
	if err := r.UpdateDataPlaneConfigSnapshot(ctx, dp.ID, newSnap); err != nil {
		t.Fatalf("UpdateDataPlaneConfigSnapshot: %v", err)
	}

	got, err := r.GetDataPlaneConfigSnapshot(ctx, dp.ID)
	if err != nil {
		t.Fatalf("GetDataPlaneConfigSnapshot: %v", err)
	}
	if len(got.PolicyBundle) != 1 || got.PolicyBundle[0] != "allow_inference" {
		t.Errorf("PolicyBundle mismatch: %v", got.PolicyBundle)
	}
	if _, ok := got.RoutingTable["llama3-8b"]; !ok {
		t.Errorf("RoutingTable missing expected key")
	}
}

func TestAssignNodeToDataPlane(t *testing.T) {
	ctx := context.Background()
	r := openTemp(t)
	dp, _ := mustCreateDP(t, r, "assign-dp")

	// Create a node to assign.
	node := &registry.Node{
		ID:       "node-assign-1",
		Hostname: "gpu01.local",
		OS:       "linux",
		Arch:     "amd64",
		State:    "NODE_STATE_READY",
	}
	if err := r.CreateNode(ctx, node); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	// Assign.
	if err := r.AssignNodeToDataPlane(ctx, node.ID, dp.ID); err != nil {
		t.Fatalf("AssignNodeToDataPlane: %v", err)
	}

	// List nodes in DP.
	nodes, err := r.ListNodesByDataPlane(ctx, dp.ID)
	if err != nil {
		t.Fatalf("ListNodesByDataPlane: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != node.ID {
		t.Errorf("expected node %q in DP, got %v", node.ID, nodes)
	}

	// Unassign.
	if err := r.AssignNodeToDataPlane(ctx, node.ID, ""); err != nil {
		t.Fatalf("AssignNodeToDataPlane (unassign): %v", err)
	}
	nodes, err = r.ListNodesByDataPlane(ctx, dp.ID)
	if err != nil {
		t.Fatalf("ListNodesByDataPlane after unassign: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes after unassign, got %d", len(nodes))
	}
}

func TestValidateDataPlaneToken(t *testing.T) {
	ctx := context.Background()
	r := openTemp(t)
	dp, tok := mustCreateDP(t, r, "token-dp")

	// Valid token.
	got, err := r.ValidateDataPlaneToken(ctx, tok)
	if err != nil {
		t.Fatalf("ValidateDataPlaneToken (valid): %v", err)
	}
	if got.ID != dp.ID {
		t.Errorf("ID want %q got %q", dp.ID, got.ID)
	}

	// Wrong token → ErrNotFound.
	_, err = r.ValidateDataPlaneToken(ctx, "dp_wrongtoken")
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("want ErrNotFound for wrong token, got %v", err)
	}
}
