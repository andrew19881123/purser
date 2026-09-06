package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// hashForTest returns the SHA-256 hex hash of token, matching the format
// stored in the key_hash column by handleCreateAPIKey and GetAPIKeyByHash.
func hashForTest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newRegSQLite is like newReg but returns the concrete *registry.SQLiteRegistry
// so tests can access DB() for direct SQL manipulation.
func newRegSQLite(t *testing.T) *registry.SQLiteRegistry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

const adminKeyPlain = "psk_admin_test_key_for_lifecycle"

var adminKeyHash = hashForTest(adminKeyPlain)

// TestRotateAPIKey_Happy verifies the golden path:
//   - POST /api/v1/apikeys/{id}/rotate returns 201 with old_id, new_id, key
//   - the old key is disabled after rotation
//   - the new key exists and is enabled with the same role/tenant
func TestRotateAPIKey_Happy(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()

	// Seed an enabled key to rotate.
	if err := reg.CreateAPIKey(ctx, &registry.APIKey{
		ID:      "key-rotate-src",
		Name:    "rotate-me",
		KeyHash: "aabbccdd1122",
		Tenant:  "acme",
		Role:    "inference",
		Quota:   1000,
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	// Seed an admin key so auth fail-closed allows the request.
	if err := reg.CreateAPIKey(ctx, &registry.APIKey{
		ID:      "admin-for-rotate",
		Name:    "admin",
		KeyHash: adminKeyHash,
		Tenant:  "acme",
		Role:    "admin",
		Quota:   0,
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed admin key: %v", err)
	}

	srv := server.New(reg, server.Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys/key-rotate-src/rotate", nil)
	req.Header.Set("Authorization", "Bearer "+adminKeyPlain)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("rotate: status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw=%s", err, rec.Body.String())
	}

	// Response must carry old_id, new_id and a psk_ prefixed plaintext key.
	if resp["old_id"] != "key-rotate-src" {
		t.Errorf("old_id = %v, want key-rotate-src", resp["old_id"])
	}
	newID, _ := resp["new_id"].(string)
	if newID == "" {
		t.Fatalf("new_id missing or empty; body=%s", rec.Body.String())
	}
	plaintext, _ := resp["key"].(string)
	if len(plaintext) < 4 || plaintext[:4] != "psk_" {
		t.Errorf("key = %q, want psk_ prefix", plaintext)
	}

	// Old key must now be disabled.
	old, err := reg.GetAPIKey(ctx, "key-rotate-src")
	if err != nil {
		t.Fatalf("GetAPIKey(old): %v", err)
	}
	if old.Enabled {
		t.Error("old key is still enabled after rotation; want disabled")
	}

	// New key must exist and be enabled with the same role and tenant.
	newKey, err := reg.GetAPIKey(ctx, newID)
	if err != nil {
		t.Fatalf("GetAPIKey(new %q): %v", newID, err)
	}
	if !newKey.Enabled {
		t.Error("new key is disabled; want enabled")
	}
	if newKey.Role != "inference" {
		t.Errorf("new key role = %q, want inference", newKey.Role)
	}
	if newKey.Tenant != "acme" {
		t.Errorf("new key tenant = %q, want acme", newKey.Tenant)
	}
}

// TestRotateAPIKey_AlreadyDisabled verifies that rotating a disabled key
// returns 409 Conflict with error code "key_already_revoked".
func TestRotateAPIKey_AlreadyDisabled(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()

	// Seed a disabled key.
	if err := reg.CreateAPIKey(ctx, &registry.APIKey{
		ID:      "key-disabled-lc",
		Name:    "disabled",
		KeyHash: "deadbeef010203",
		Tenant:  "acme",
		Role:    "admin",
		Enabled: false,
	}); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	srv := server.New(reg, server.Config{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/apikeys/key-disabled-lc/rotate", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("disabled rotate: status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "key_already_revoked" {
		t.Errorf("error = %v, want key_already_revoked", body["error"])
	}
}

// TestRotateAPIKey_NotFound verifies that rotating a missing key returns 404.
func TestRotateAPIKey_NotFound(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/apikeys/does-not-exist/rotate", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing key rotate: status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGetAPIKeyByHash_Expired verifies that GetAPIKeyByHash treats a key with
// an expires_at in the past as non-existent and returns ErrNotFound.
func TestGetAPIKeyByHash_Expired(t *testing.T) {
	// Use the concrete *SQLiteRegistry so we can set expires_at via raw SQL.
	sqliteReg := newRegSQLite(t)
	ctx := context.Background()

	const token = "psk_test_lifecycle_expired"
	keyHash := hashForTest(token)

	if err := sqliteReg.CreateAPIKey(ctx, &registry.APIKey{
		ID:      "key-expired-lc",
		Name:    "will-expire",
		KeyHash: keyHash,
		Tenant:  "test",
		Role:    "admin",
		Enabled: true,
	}); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Backdate expires_at to one hour ago so the key is definitively expired.
	pastStr := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := sqliteReg.DB().ExecContext(ctx,
		"UPDATE api_keys SET expires_at=? WHERE id='key-expired-lc'", pastStr,
	); err != nil {
		t.Fatalf("set expires_at: %v", err)
	}

	// GetAPIKeyByHash must return ErrNotFound for an expired key.
	_, err := sqliteReg.GetAPIKeyByHash(ctx, keyHash)
	if err == nil {
		t.Fatal("expected ErrNotFound for expired key, got nil error")
	}
	if err != registry.ErrNotFound {
		t.Fatalf("err = %v, want registry.ErrNotFound", err)
	}
}
