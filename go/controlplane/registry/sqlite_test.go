package registry_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/audit"
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

// reconstructAscending turns ListAudit's newest-first rows into the ascending
// (oldest-first) hash chain that audit.Verify expects.
func reconstructAscending(entries []*registry.AuditEntry) []audit.Entry {
	out := make([]audit.Entry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, entries[i].ChainEntry())
	}
	return out
}

// TestAuditChainVerifiesAndDetectsTamper is the end-to-end check for the
// tamper-evident storage: appends (including one carrying Details) form a chain
// that audit.Verify accepts, and mutating a stored row's content — without
// recomputing its hash — makes Verify fail at exactly that entry.
func TestAuditChainVerifiesAndDetectsTamper(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })

	events := []*registry.AuditEntry{
		{Actor: "api", Action: "model.created", Target: "m1"},
		{Actor: "fleet", Action: "node.decommissioned", Target: "node-7",
			Details: json.RawMessage(`{"reason":"drain","zone":"eu"}`)},
		{Actor: "api", Action: "apikey.created", Target: "key-1"},
	}
	for i, e := range events {
		if err := reg.AppendAudit(ctx, e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if e.Seq != uint64(i)+1 {
			t.Errorf("event %d seq = %d, want %d", i, e.Seq, i+1)
		}
		if e.Hash == "" {
			t.Errorf("event %d hash not assigned", i)
		}
	}

	entries, err := reg.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}

	asc := reconstructAscending(entries)
	if err := audit.Verify(asc); err != nil {
		t.Fatalf("verify untampered chain: %v", err)
	}
	if asc[0].PrevHash != audit.GenesisPrevHash {
		t.Errorf("first prev_hash = %q, want genesis", asc[0].PrevHash)
	}
	for i := 1; i < len(asc); i++ {
		if asc[i].PrevHash != asc[i-1].Hash {
			t.Errorf("entry %d prev_hash does not link to entry %d hash", i, i-1)
		}
	}

	// Tamper with a stored row's content, leaving its stored hash intact.
	if _, err := reg.DB().ExecContext(ctx, `UPDATE audit_log SET target = ? WHERE seq = ?`, "hacked", 2); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	entries, err = reg.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list post-tamper: %v", err)
	}
	verr := audit.Verify(reconstructAscending(entries))
	if verr == nil {
		t.Fatalf("verify detected no tamper, want failure at seq 2")
	}
	var ve *audit.VerifyError
	if !errors.As(verr, &ve) {
		t.Fatalf("error = %v, want *audit.VerifyError", verr)
	}
	if ve.Index != 1 || ve.Kind != audit.KindHash {
		t.Errorf("break index/kind = %d/%s, want 1/%s", ve.Index, ve.Kind, audit.KindHash)
	}
}

// TestAuditChainConcurrentAppends asserts the core write-side guarantee: even
// with many concurrent writers, seq is monotonic and gap-free (exactly 1..n
// with no duplicates) and the resulting chain verifies.
func TestAuditChainConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := &registry.AuditEntry{Actor: "load", Action: "ping", Target: fmt.Sprintf("t%d", i)}
			if err := reg.AppendAudit(ctx, e); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}

	entries, err := reg.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("entries = %d, want %d", len(entries), n)
	}
	seen := make(map[uint64]bool, n)
	for _, e := range entries {
		if e.Seq < 1 || e.Seq > n {
			t.Fatalf("seq %d out of range 1..%d", e.Seq, n)
		}
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	if len(seen) != n {
		t.Fatalf("gap in seq: got %d distinct, want %d", len(seen), n)
	}
	if err := audit.Verify(reconstructAscending(entries)); err != nil {
		t.Fatalf("verify concurrent chain: %v", err)
	}
}

// TestAuditMigrationExistingDBWithLegacyRows verifies the additive migration on
// a database created before the hash-chain columns existed: Migrate adds the
// columns (idempotently), a pre-existing row coexists as an unchained row
// (Seq==0), and new appends start a clean chain at FirstSeq that verifies once
// the legacy prefix is skipped (as the endpoint does).
func TestAuditMigrationExistingDBWithLegacyRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// 1. Fabricate a pre-feature database: audit_log WITHOUT the chain columns,
	//    plus one legacy row.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
		CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			actor TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL DEFAULT '',
			details TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL
		);
		INSERT INTO audit_log (actor, action, target, details, created_at)
		VALUES ('legacy', 'boot', 'system', '{}', '2020-01-01T00:00:00Z');`); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// 2. Open + migrate (twice, to prove idempotency).
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate (2nd, idempotent): %v", err)
	}

	// 3. New appends chain from FirstSeq, independent of the legacy row.
	for _, e := range []*registry.AuditEntry{
		{Actor: "api", Action: "model.created", Target: "m1"},
		{Actor: "api", Action: "model.deleted", Target: "m1"},
	} {
		if err := reg.AppendAudit(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	entries, err := reg.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (1 legacy + 2 chained)", len(entries))
	}

	// The legacy row is unchained (Seq==0); skip it exactly as the endpoint does.
	chained := make([]audit.Entry, 0, len(entries))
	legacy := 0
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Seq < audit.FirstSeq {
			legacy++
			continue
		}
		chained = append(chained, entries[i].ChainEntry())
	}
	if legacy != 1 {
		t.Errorf("legacy (Seq==0) rows = %d, want 1", legacy)
	}
	if len(chained) != 2 || chained[0].Seq != 1 || chained[1].Seq != 2 {
		t.Fatalf("chained seqs = %+v, want [1 2]", chained)
	}
	if err := audit.Verify(chained); err != nil {
		t.Fatalf("verify chain after legacy migration: %v", err)
	}
}

// TestNodeAdvertisedAddrsRoundTrip verifies the advertised address columns are
// persisted and read back on both create and update.
func TestNodeAdvertisedAddrsRoundTrip(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	n := &registry.Node{
		ID:                      "adv-node",
		Hostname:                "gpu.local",
		State:                   "NODE_STATE_ENROLLED",
		AdvertisedAgentAddr:     "10.0.0.5:50151",
		AdvertisedInferenceAddr: "10.0.0.5:8000",
	}
	if err := reg.CreateNode(ctx, n); err != nil {
		t.Fatalf("create node: %v", err)
	}
	got, err := reg.GetNode(ctx, "adv-node")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.AdvertisedAgentAddr != "10.0.0.5:50151" || got.AdvertisedInferenceAddr != "10.0.0.5:8000" {
		t.Fatalf("advertised addrs not persisted: agent=%q inf=%q",
			got.AdvertisedAgentAddr, got.AdvertisedInferenceAddr)
	}

	// Update path must preserve/overwrite the columns too.
	got.AdvertisedAgentAddr = "10.0.0.5:41000"
	if err := reg.UpdateNode(ctx, got); err != nil {
		t.Fatalf("update node: %v", err)
	}
	got2, err := reg.GetNode(ctx, "adv-node")
	if err != nil {
		t.Fatalf("get node after update: %v", err)
	}
	if got2.AdvertisedAgentAddr != "10.0.0.5:41000" {
		t.Errorf("updated agent addr = %q, want 10.0.0.5:41000", got2.AdvertisedAgentAddr)
	}
}

// TestMigrateAddsAdvertisedColumnsToLegacyDB proves the additive migration is
// idempotent: a pre-existing nodes table lacking the advertised columns is
// upgraded in place, and running Migrate again is a no-op.
func TestMigrateAddsAdvertisedColumnsToLegacyDB(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// Build a "legacy" schema: a nodes table without the advertised columns.
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE nodes (
		id TEXT PRIMARY KEY,
		hostname TEXT NOT NULL DEFAULT '',
		os TEXT NOT NULL DEFAULT '',
		arch TEXT NOT NULL DEFAULT '',
		ram_gb REAL NOT NULL DEFAULT 0,
		vram_gb REAL NOT NULL DEFAULT 0,
		state TEXT NOT NULL DEFAULT '',
		last_seen TEXT,
		hardware_profile TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO nodes (id, hostname, created_at, updated_at)
		VALUES ('legacy-1', 'old.local', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	db.Close()

	// Open through the registry and migrate (twice, to prove idempotency).
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate #1: %v", err)
	}
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate #2 (must be idempotent): %v", err)
	}

	// The legacy row must now scan (columns present, defaulted to '').
	n, err := reg.GetNode(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("get legacy node: %v", err)
	}
	if n.AdvertisedAgentAddr != "" || n.AdvertisedInferenceAddr != "" {
		t.Errorf("legacy advertised addrs should default empty, got agent=%q inf=%q",
			n.AdvertisedAgentAddr, n.AdvertisedInferenceAddr)
	}

	// And new writes to the upgraded table must persist the columns.
	if err := reg.UpdateNode(ctx, &registry.Node{
		ID:                      "legacy-1",
		Hostname:                "old.local",
		AdvertisedAgentAddr:     "1.2.3.4:50151",
		AdvertisedInferenceAddr: "1.2.3.4:8000",
	}); err != nil {
		t.Fatalf("update upgraded node: %v", err)
	}
	got, err := reg.GetNode(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.AdvertisedAgentAddr != "1.2.3.4:50151" || got.AdvertisedInferenceAddr != "1.2.3.4:8000" {
		t.Errorf("advertised addrs after upgrade not persisted: agent=%q inf=%q",
			got.AdvertisedAgentAddr, got.AdvertisedInferenceAddr)
	}
}
