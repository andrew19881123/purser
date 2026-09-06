package registry_test

import (
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/stretchr/testify/require"
)

// newTestRegistry returns a freshly migrated in-memory registry for schema
// tests. It delegates to openTemp (defined in sqlite_test.go).
func newTestRegistry(t *testing.T) registry.Registry {
	t.Helper()
	return openTemp(t)
}

// TestSchemaMigrationAddsNewTables verifies that every Wave B table exists in
// the schema after a fresh Migrate.
func TestSchemaMigrationAddsNewTables(t *testing.T) {
	reg := newTestRegistry(t)
	tables := []string{
		"oidc_sessions",
		"pkce_state",
		"api_key_access_log",
		"model_pricing",
		"tenant_quotas",
		"tenant_quota_usage",
		"policy_versions",
		"gdpr_erasure_log",
	}
	sr := reg.(*registry.SQLiteRegistry)
	for _, tbl := range tables {
		var count int
		err := sr.DB().QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 1, count, "table %q must exist after migration", tbl)
	}
}

// TestAPIKeyExpiresAtColumnExists verifies that the enterprise lifecycle
// columns are present in api_keys after a fresh Migrate.
func TestAPIKeyExpiresAtColumnExists(t *testing.T) {
	reg := newTestRegistry(t)
	sr := reg.(*registry.SQLiteRegistry)
	// Insert a row with expires_at to confirm the column is present.
	_, err := sr.DB().Exec(
		`INSERT INTO api_keys
		    (id, name, key_hash, tenant, role, quota, enabled, created_at, updated_at, expires_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"test-id", "test", "hash", "t", "viewer", 0, 1,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
		"2030-01-01T00:00:00Z",
	)
	require.NoError(t, err, "expires_at column must exist in api_keys")
}

// TestAPIKeyEnterpriseColumnsExist verifies that all five enterprise lifecycle
// columns were added to the api_keys table.
func TestAPIKeyEnterpriseColumnsExist(t *testing.T) {
	reg := newTestRegistry(t)
	sr := reg.(*registry.SQLiteRegistry)

	rows, err := sr.DB().Query("PRAGMA table_info(api_keys)")
	require.NoError(t, err)
	defer rows.Close()

	want := map[string]bool{
		"expires_at":     false,
		"last_used_at":   false,
		"predecessor_id": false,
		"rotated_at":     false,
		"scopes":         false,
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt interface{}
		var pk int
		require.NoError(t, rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk))
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	require.NoError(t, rows.Err())

	for col, found := range want {
		require.True(t, found, "api_keys column %q must exist after migration", col)
	}
}

// TestInferenceAuditLogModelVersionColumns verifies that the AI Act Art.12
// model-version tracking columns were added to inference_audit_log.
func TestInferenceAuditLogModelVersionColumns(t *testing.T) {
	reg := newTestRegistry(t)
	sr := reg.(*registry.SQLiteRegistry)

	rows, err := sr.DB().Query("PRAGMA table_info(inference_audit_log)")
	require.NoError(t, err)
	defer rows.Close()

	want := map[string]bool{
		"model_revision":     false,
		"model_quantization": false,
		"node_id":            false,
		"inference_engine":   false,
		"seq":                false,
		"prev_hash":          false,
		"hash":               false,
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt interface{}
		var pk int
		require.NoError(t, rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk))
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	require.NoError(t, rows.Err())

	for col, found := range want {
		require.True(t, found, "inference_audit_log column %q must exist after migration", col)
	}
}
