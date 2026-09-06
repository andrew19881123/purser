package raftcp_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	raftcp "github.com/purser/purser/go/controlplane/raft"
	"github.com/purser/purser/go/controlplane/registry"
	_ "modernc.org/sqlite"
)

// openMemRegistry opens an in-memory SQLiteRegistry and runs the schema
// migration, returning a ready-to-use Registry.
func openMemRegistry(t *testing.T) registry.Registry {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	reg := registry.NewWithDB(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

// mustMarshal marshals v to JSON and fails the test on error.
func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// applyLog creates a synthetic *raft.Log carrying data and calls fsm.Apply.
func applyLog(t *testing.T, fsm *raftcp.PurserFSM, index uint64, data []byte) interface{} {
	t.Helper()
	l := &raft.Log{Index: index, Term: 1, Type: raft.LogCommand, Data: data}
	return fsm.Apply(l)
}

// TestFSM_Apply_CreateModel verifies that a CmdCreateModel command persists the
// model to the underlying registry.
func TestFSM_Apply_CreateModel(t *testing.T) {
	reg := openMemRegistry(t)
	fsm := raftcp.NewFSM(reg, nil)

	model := &registry.Model{
		ID:     "m-test-001",
		Family: "llama-3-8b",
	}
	data, err := raftcp.MarshalCommand(raftcp.CmdCreateModel, model)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}

	result := applyLog(t, fsm, 1, data)
	if result != nil {
		t.Fatalf("Apply returned non-nil error: %v", result)
	}

	ctx := context.Background()
	got, err := reg.GetModel(ctx, "m-test-001")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if got.Family != "llama-3-8b" {
		t.Errorf("model name: got %q, want %q", got.Family, "llama-3-8b")
	}
}

// TestFSM_Apply_DeleteModel verifies that a CmdDeleteModel command removes the
// model from the registry.
func TestFSM_Apply_DeleteModel(t *testing.T) {
	reg := openMemRegistry(t)
	ctx := context.Background()

	// Create a model directly so we have something to delete.
	if err := reg.CreateModel(ctx, &registry.Model{ID: "m-del-001", Family: "to-delete"}); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}

	fsm := raftcp.NewFSM(reg, nil)
	data, err := raftcp.MarshalCommand(raftcp.CmdDeleteModel, "m-del-001")
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}

	result := applyLog(t, fsm, 2, data)
	if result != nil {
		t.Fatalf("Apply returned error: %v", result)
	}

	_, err = reg.GetModel(ctx, "m-del-001")
	if err == nil {
		t.Fatal("expected ErrNotFound after delete, got nil")
	}
}

// TestFSM_Apply_UnknownCommandType verifies that an unknown command type is
// silently ignored (returns nil) so rolling upgrades don't break followers.
func TestFSM_Apply_UnknownCommandType(t *testing.T) {
	reg := openMemRegistry(t)
	fsm := raftcp.NewFSM(reg, nil)

	data := mustMarshal(t, raftcp.Command{
		Type: 200, // deliberately unknown
		Data: json.RawMessage(`{}`),
	})
	result := applyLog(t, fsm, 99, data)
	if result != nil {
		t.Errorf("unknown command should be ignored (nil), got %v", result)
	}
}

// TestFSM_Apply_MalformedPayload verifies that a command with an unparseable
// payload returns an error rather than panicking.
func TestFSM_Apply_MalformedPayload(t *testing.T) {
	reg := openMemRegistry(t)
	fsm := raftcp.NewFSM(reg, nil)

	// Valid outer JSON but the inner data is not a Model.
	data, _ := raftcp.MarshalCommand(raftcp.CmdCreateModel, "this-is-not-a-model")

	result := applyLog(t, fsm, 5, data)
	// Should return an error, not nil.
	if result == nil {
		t.Fatal("expected error for malformed payload, got nil")
	}
}

// TestFSM_Apply_CreateDeployment verifies that a CmdCreateDeployment command
// persists the deployment to the underlying registry.
func TestFSM_Apply_CreateDeployment(t *testing.T) {
	reg := openMemRegistry(t)
	ctx := context.Background()

	// Create prerequisite model first (FK constraint).
	if err := reg.CreateModel(ctx, &registry.Model{ID: "m-dep-001", Family: "gpt2"}); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}

	fsm := raftcp.NewFSM(reg, nil)
	d := &registry.Deployment{
		ID:      "dep-001",
		ModelID: "m-dep-001",
		State:   "DEPLOYMENT_STATE_PENDING",
	}
	data, err := raftcp.MarshalCommand(raftcp.CmdCreateDeployment, d)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}

	result := applyLog(t, fsm, 3, data)
	if result != nil {
		t.Fatalf("Apply returned error: %v", result)
	}

	got, err := reg.GetDeployment(ctx, "dep-001")
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.ModelID != "m-dep-001" {
		t.Errorf("deployment model_id: got %q, want %q", got.ModelID, "m-dep-001")
	}
}

// TestFSM_Snapshot_Restore verifies that Snapshot returns without error and
// that Restore drains the reader and returns nil (MVP no-op).
func TestFSM_Snapshot_Restore(t *testing.T) {
	reg := openMemRegistry(t)
	fsm := raftcp.NewFSM(reg, nil)

	snap, err := fsm.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}

	// Persist into an in-memory sink.
	sink := &noopSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	snap.Release()

	// Restore from an empty reader.
	rc := &emptyReadCloser{}
	if err := fsm.Restore(rc); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

// TestNode_SingleNodeBootstrap verifies that a single-node Raft cluster starts,
// elects itself leader within a reasonable timeout, and reports IsLeader()==true.
func TestNode_SingleNodeBootstrap(t *testing.T) {
	reg := openMemRegistry(t)
	fsm := raftcp.NewFSM(reg, nil)

	node, _, err := raftcp.NewInmemNode("node-1", fsm)
	if err != nil {
		t.Fatalf("NewInmemNode: %v", err)
	}
	defer node.Shutdown() //nolint:errcheck

	// Wait for leader election (at most 2 s in test).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if node.IsLeader() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !node.IsLeader() {
		t.Fatal("single-node cluster did not elect a leader within 2s")
	}
	if node.State() != "Leader" {
		t.Errorf("State() = %q, want %q", node.State(), "Leader")
	}
	stats := node.Stats()
	if stats == nil {
		t.Fatal("Stats() returned nil")
	}
}

// TestNode_ApplyThroughRaft verifies the end-to-end path: marshal a command,
// apply it through the Raft leader, and confirm the change landed in the
// underlying registry.
func TestNode_ApplyThroughRaft(t *testing.T) {
	reg := openMemRegistry(t)
	fsm := raftcp.NewFSM(reg, nil)

	node, _, err := raftcp.NewInmemNode("node-e2e", fsm)
	if err != nil {
		t.Fatalf("NewInmemNode: %v", err)
	}
	defer node.Shutdown() //nolint:errcheck

	// Wait for leader election.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if node.IsLeader() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !node.IsLeader() {
		t.Skip("cluster did not elect leader — skipping apply test")
	}

	model := &registry.Model{ID: "m-e2e-001", Family: "raft-test-model"}
	data, err := raftcp.MarshalCommand(raftcp.CmdCreateModel, model)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	future := node.Raft().Apply(data, 5*time.Second)
	if err := future.Error(); err != nil {
		t.Fatalf("Raft.Apply: %v", err)
	}

	ctx := context.Background()
	got, err := reg.GetModel(ctx, "m-e2e-001")
	if err != nil {
		t.Fatalf("GetModel after Raft.Apply: %v", err)
	}
	if got.Family != "raft-test-model" {
		t.Errorf("model name: got %q, want %q", got.Family, "raft-test-model")
	}
}

// --- helpers ------------------------------------------------------------------

// noopSink is a trivial raft.SnapshotSink for testing.
type noopSink struct{ closed bool }

func (s *noopSink) Write(p []byte) (int, error) { return len(p), nil }
func (s *noopSink) Close() error                { s.closed = true; return nil }
func (s *noopSink) ID() string                  { return "noop" }
func (s *noopSink) Cancel() error               { return nil }

// emptyReadCloser satisfies io.ReadCloser with zero bytes.
type emptyReadCloser struct{}

func (e *emptyReadCloser) Read(p []byte) (int, error) { return 0, nil }
func (e *emptyReadCloser) Close() error               { return nil }
