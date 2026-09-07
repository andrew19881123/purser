package server_test

// sk_key_test.go — tests for v0.5 API key improvements:
//   - sk- prefix on new keys
//   - backward-compat acceptance of psk_ legacy keys
//   - created_by field captured on creation
//   - GET /api/v1/logs/access (unified access-log endpoint)

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// openSQLiteReg opens a fresh *registry.SQLiteRegistry for sk_key tests.
func openSQLiteReg(t *testing.T) *registry.SQLiteRegistry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "sk_key.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

// TestCreateAPIKey_HasSkPrefix verifies that POST /api/v1/apikeys returns a key
// with the new sk- prefix format: "sk-" followed by exactly 40 hex characters.
func TestCreateAPIKey_HasSkPrefix(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/apikeys",
			strings.NewReader(`{"name":"sk-test","tenant":"t1"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	key, _ := resp["key"].(string)
	if len(key) != 43 {
		t.Errorf("key length = %d, want 43 (sk-<40 hex>); key=%q", len(key), key)
	}
	if !strings.HasPrefix(key, "sk-") {
		t.Errorf("key = %q, want sk- prefix", key)
	}
	// Tail must be valid lowercase hex.
	tail := strings.TrimPrefix(key, "sk-")
	for _, c := range tail {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("key tail %q is not lowercase hex (char %q)", tail, c)
			break
		}
	}
}

// TestGetAPIKeyByHash_AcceptsSkFormat verifies that a key generated with the sk-
// format authenticates successfully (round-trip: create → hash → lookup).
func TestGetAPIKeyByHash_AcceptsSkFormat(t *testing.T) {
	sqliteReg := openSQLiteReg(t)
	ctx := context.Background()

	// A well-formed sk- key (43 chars, 40 hex tail).
	const skToken = "sk-a3f8bc12de456789abcdef0123456789abcdef01"
	sum := sha256.Sum256([]byte(skToken))
	keyHash := hex.EncodeToString(sum[:])

	if err := sqliteReg.CreateAPIKey(ctx, &registry.APIKey{
		ID:      "key-sk-test",
		Name:    "sk-format-key",
		KeyHash: keyHash,
		Tenant:  "test",
		Role:    "admin",
		Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := sqliteReg.GetAPIKeyByHash(ctx, keyHash)
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if got.ID != "key-sk-test" {
		t.Errorf("id = %q, want key-sk-test", got.ID)
	}
}

// TestGetAPIKeyByHash_AcceptsPskFormat verifies that legacy psk_ tokens are still
// accepted by GetAPIKeyByHash (backward compatibility).
func TestGetAPIKeyByHash_AcceptsPskFormat(t *testing.T) {
	sqliteReg := openSQLiteReg(t)
	ctx := context.Background()

	const pskToken = "psk_legacy_token_for_compat_test"
	sum := sha256.Sum256([]byte(pskToken))
	keyHash := hex.EncodeToString(sum[:])

	if err := sqliteReg.CreateAPIKey(ctx, &registry.APIKey{
		ID:      "key-psk-compat",
		Name:    "psk-compat-key",
		KeyHash: keyHash,
		Tenant:  "legacy-tenant",
		Role:    "viewer",
		Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := sqliteReg.GetAPIKeyByHash(ctx, keyHash)
	if err != nil {
		t.Fatalf("GetAPIKeyByHash (psk_ legacy): %v; want successful lookup", err)
	}
	if got.ID != "key-psk-compat" {
		t.Errorf("id = %q, want key-psk-compat", got.ID)
	}
	if got.Role != "viewer" {
		t.Errorf("role = %q, want viewer", got.Role)
	}
}

// TestListAccessLogs_Empty verifies GET /api/v1/logs/access returns 200 with an
// empty entries array when no access-log rows exist.
func TestListAccessLogs_Empty(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/logs/access", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Entries []*registry.APIKeyAccessEntry `json:"entries"`
		Count   int                           `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if body.Entries == nil {
		t.Errorf("entries must be [] not null")
	}
	if len(body.Entries) != 0 {
		t.Errorf("entries len = %d, want 0", len(body.Entries))
	}
	if body.Count != 0 {
		t.Errorf("count = %d, want 0", body.Count)
	}
}

// TestListAccessLogs_FilterByKey verifies GET /api/v1/logs/access?api_key_id=
// returns only the entries for the specified key.
func TestListAccessLogs_FilterByKey(t *testing.T) {
	sqliteReg := openSQLiteReg(t)
	ctx := context.Background()

	// Seed two access-log entries for key-a and one for key-b.
	entries := []registry.APIKeyAccessEntry{
		{APIKeyID: "key-a", KeyHash: "hash-a", Method: "POST", Path: "/v1/chat", StatusCode: 200, RequestAt: time.Now()},
		{APIKeyID: "key-a", KeyHash: "hash-a", Method: "GET", Path: "/v1/models", StatusCode: 200, RequestAt: time.Now()},
		{APIKeyID: "key-b", KeyHash: "hash-b", Method: "POST", Path: "/v1/chat", StatusCode: 403, RequestAt: time.Now()},
	}
	for i := range entries {
		if err := sqliteReg.RecordAPIKeyAccess(ctx, &entries[i]); err != nil {
			t.Fatalf("seed entry %d: %v", i, err)
		}
	}

	srv := server.New(sqliteReg, server.Config{})

	// Filter for key-a only.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/logs/access?api_key_id=key-a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Entries []*registry.APIKeyAccessEntry `json:"entries"`
		Count   int                           `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 2 {
		t.Errorf("count = %d, want 2 (only key-a entries)", body.Count)
	}
	for _, e := range body.Entries {
		if e.APIKeyID != "key-a" {
			t.Errorf("entry has api_key_id = %q, want key-a", e.APIKeyID)
		}
	}
}

// TestCreatedBy_CapturedOnCreate verifies that handleCreateAPIKey populates the
// created_by field with actorFromRequest (system when no token is present in dev
// mode; apikey:<fingerprint> when a Bearer token is used).
func TestCreatedBy_CapturedOnCreate(t *testing.T) {
	sqliteReg := openSQLiteReg(t)
	ctx := context.Background()

	srv := server.New(sqliteReg, server.Config{})

	// Create the first key unauthenticated (dev mode — no keys exist yet).
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/apikeys",
			strings.NewReader(`{"name":"first","tenant":"t1"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status=%d; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	firstID, _ := resp["id"].(string)
	firstKey, _ := resp["key"].(string)

	// The first key was created with no auth token — actor should be "system".
	stored, err := sqliteReg.GetAPIKey(ctx, firstID)
	if err != nil {
		t.Fatalf("GetAPIKey: %v", err)
	}
	if stored.CreatedBy != "system" {
		t.Errorf("created_by = %q, want %q (no auth in dev mode)", stored.CreatedBy, "system")
	}

	// Create a second key authenticated with the first key.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys",
		strings.NewReader(`{"name":"second","tenant":"t1"}`))
	req2.Header.Set("Authorization", "Bearer "+firstKey)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("create second: status=%d; body=%s", rec2.Code, rec2.Body.String())
	}

	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	secondID, _ := resp2["id"].(string)

	stored2, err := sqliteReg.GetAPIKey(ctx, secondID)
	if err != nil {
		t.Fatalf("GetAPIKey second: %v", err)
	}
	// created_by should be "apikey:<8 hex chars>" (fingerprint of the bearer token).
	if !strings.HasPrefix(stored2.CreatedBy, "apikey:") {
		t.Errorf("created_by = %q, want apikey:<fingerprint> prefix", stored2.CreatedBy)
	}
}
