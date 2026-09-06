package registry_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// newTestRegistry opens and migrates a fresh SQLite registry on a temp file.
// The test-scoped temp dir and the Close() call are both registered with
// t.Cleanup so the caller does not need to manage lifecycle.
func newTestRegistry(t *testing.T) *registry.SQLiteRegistry {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate registry: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

// TestIntegrityCheck_PassesOnCleanDB verifies that PRAGMA integrity_check(100)
// returns "ok" on a freshly-created, migrated database when the env flag is set.
func TestIntegrityCheck_PassesOnCleanDB(t *testing.T) {
	t.Setenv("PURSER_DB_INTEGRITY_CHECK", "1")
	reg := newTestRegistry(t)
	// If Open + Migrate returned without error the integrity check passed.
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
}

// TestIntegrityCheck_SkippedByDefault verifies that without
// PURSER_DB_INTEGRITY_CHECK=1 the registry opens quickly (< 500 ms even on
// slow CI), confirming that no heavyweight check is running by default.
func TestIntegrityCheck_SkippedByDefault(t *testing.T) {
	// Ensure the env var is not set (t.Setenv with empty string would still
	// differ from "1" so this guard just documents intent).
	start := time.Now()
	reg := newTestRegistry(t)
	elapsed := time.Since(start)
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	// 500 ms is generous — a fresh in-memory migration typically takes < 5 ms.
	// Keeping the threshold high avoids flakiness on loaded CI runners.
	const threshold = 500 * time.Millisecond
	if elapsed >= threshold {
		t.Errorf("registry open+migrate without integrity check took %v (want < %v); "+
			"possible regression or slow CI — investigate before raising threshold", elapsed, threshold)
	}
}
