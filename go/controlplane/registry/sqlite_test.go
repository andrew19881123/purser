package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// openTemp opens a migrated SQLite registry on a temp file that is cleaned up
// with the test.
func openTemp(t *testing.T) registry.Registry {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

func TestNodeRoundTrip(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	want := &registry.Node{
		ID:              "node-1",
		Hostname:        "gpu-box.local",
		OS:              "OS_LINUX",
		Arch:            "ARCH_X86_64",
		RAMGB:           128,
		VRAMGB:          48,
		State:           "NODE_STATE_READY",
		LastSeen:        time.Now().UTC().Truncate(time.Second),
		HardwareProfile: json.RawMessage(`{"node_id":"node-1","gpus":[{"name":"RTX 6000","vram_gb":48}]}`),
	}
	if err := reg.CreateNode(ctx, want); err != nil {
		t.Fatalf("create node: %v", err)
	}

	got, err := reg.GetNode(ctx, "node-1")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.ID != want.ID || got.Hostname != want.Hostname || got.OS != want.OS ||
		got.Arch != want.Arch || got.State != want.State {
		t.Errorf("scalar fields mismatch: got %+v want %+v", got, want)
	}
	if got.RAMGB != want.RAMGB || got.VRAMGB != want.VRAMGB {
		t.Errorf("memory fields mismatch: got ram=%v vram=%v", got.RAMGB, got.VRAMGB)
	}
	if !got.LastSeen.Equal(want.LastSeen) {
		t.Errorf("last_seen mismatch: got %v want %v", got.LastSeen, want.LastSeen)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not populated: created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}
	// HardwareProfile must survive as equivalent JSON.
	var gotHW, wantHW map[string]any
	if err := json.Unmarshal(got.HardwareProfile, &gotHW); err != nil {
		t.Fatalf("unmarshal got hw: %v", err)
	}
	if err := json.Unmarshal(want.HardwareProfile, &wantHW); err != nil {
		t.Fatalf("unmarshal want hw: %v", err)
	}
	if gotHW["node_id"] != wantHW["node_id"] {
		t.Errorf("hardware_profile node_id mismatch: got %v", gotHW["node_id"])
	}

	// List should return exactly one node.
	list, err := reg.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	// Update changes state.
	got.State = "NODE_STATE_DRAINING"
	if err := reg.UpdateNode(ctx, got); err != nil {
		t.Fatalf("update node: %v", err)
	}
	reread, err := reg.GetNode(ctx, "node-1")
	if err != nil {
		t.Fatalf("re-get node: %v", err)
	}
	if reread.State != "NODE_STATE_DRAINING" {
		t.Errorf("state after update = %q, want NODE_STATE_DRAINING", reread.State)
	}

	// Delete removes it.
	if err := reg.DeleteNode(ctx, "node-1"); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if _, err := reg.GetNode(ctx, "node-1"); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)
	if _, err := reg.GetNode(ctx, "nope"); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("GetNode missing: err = %v, want ErrNotFound", err)
	}
	if _, err := reg.GetModel(ctx, "nope"); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("GetModel missing: err = %v, want ErrNotFound", err)
	}
	if _, err := reg.GetDeployment(ctx, "nope"); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("GetDeployment missing: err = %v, want ErrNotFound", err)
	}
	if _, err := reg.GetAPIKey(ctx, "nope"); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("GetAPIKey missing: err = %v, want ErrNotFound", err)
	}
}

func TestModelDeploymentAPIKeyCRUD(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	if err := reg.CreateModel(ctx, &registry.Model{ID: "m1", Family: "llama", Architecture: "llama", ParamsTotalB: 70}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := reg.CreateDeployment(ctx, &registry.Deployment{ID: "d1", ModelID: "m1", PlanID: "p1", State: "DEPLOYMENT_STATE_ACTIVE"}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if err := reg.CreateAPIKey(ctx, &registry.APIKey{ID: "k1", Name: "test", KeyHash: "deadbeef", Tenant: "acme", Quota: 1000, Enabled: true}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	models, err := reg.ListModels(ctx)
	if err != nil || len(models) != 1 {
		t.Fatalf("list models: len=%d err=%v", len(models), err)
	}
	deps, err := reg.ListDeployments(ctx)
	if err != nil || len(deps) != 1 {
		t.Fatalf("list deployments: len=%d err=%v", len(deps), err)
	}
	keys, err := reg.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list api keys: len=%d err=%v", len(keys), err)
	}
	if !keys[0].Enabled || keys[0].Tenant != "acme" || keys[0].Quota != 1000 {
		t.Errorf("api key round-trip mismatch: %+v", keys[0])
	}
}

func TestLinkUpsertAndList(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	if err := reg.UpsertLink(ctx, &registry.Link{FromNode: "a", ToNode: "b", BandwidthGBs: 10, RTTMs: 0.5}); err != nil {
		t.Fatalf("upsert link: %v", err)
	}
	if err := reg.UpsertLink(ctx, &registry.Link{FromNode: "b", ToNode: "a", BandwidthGBs: 12, RTTMs: 0.4}); err != nil {
		t.Fatalf("upsert link: %v", err)
	}
	// Upsert on an existing (from,to) must update in place, not duplicate.
	if err := reg.UpsertLink(ctx, &registry.Link{FromNode: "a", ToNode: "b", BandwidthGBs: 25, RTTMs: 0.2}); err != nil {
		t.Fatalf("re-upsert link: %v", err)
	}

	links, err := reg.ListLinks(ctx)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("links = %d, want 2 (upsert must not duplicate)", len(links))
	}
	// Ordered by (from_node, to_node): a->b first, with the updated bandwidth.
	if links[0].FromNode != "a" || links[0].ToNode != "b" || links[0].BandwidthGBs != 25 {
		t.Errorf("a->b link wrong after upsert: %+v", links[0])
	}
	if links[0].MeasuredAt.IsZero() {
		t.Error("measured_at should default to now on upsert")
	}
}

func TestAppendAudit(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	e := &registry.AuditEntry{
		Actor:   "admin",
		Action:  "deploy",
		Target:  "deployment/d1",
		Details: json.RawMessage(`{"model_id":"m1"}`),
	}
	if err := reg.AppendAudit(ctx, e); err != nil {
		t.Fatalf("append audit: %v", err)
	}
	if e.ID == 0 {
		t.Errorf("audit ID not assigned")
	}
	if err := reg.AppendAudit(ctx, &registry.AuditEntry{Actor: "op", Action: "stop", Target: "deployment/d1"}); err != nil {
		t.Fatalf("append audit 2: %v", err)
	}

	entries, err := reg.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d, want 2", len(entries))
	}
	// Newest first.
	if entries[0].Action != "stop" {
		t.Errorf("expected newest-first ordering, got first action %q", entries[0].Action)
	}
}
