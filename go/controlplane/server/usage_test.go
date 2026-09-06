package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newTestServerWithToken builds a test server with InternalToken set.
func newTestServerWithToken(t *testing.T, tok string) (*server.Server, registry.Registry) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return server.New(reg, server.Config{Addr: ":0", InternalToken: tok}), reg
}

// seedKey creates an API key in the registry and returns its ID.
func seedKey(t *testing.T, reg registry.Registry, id, tenant string) {
	t.Helper()
	if err := reg.CreateAPIKey(context.Background(), &registry.APIKey{
		ID: id, Name: id, KeyHash: "hash-" + id, Tenant: tenant, Enabled: true,
	}); err != nil {
		t.Fatalf("seed api key %s: %v", id, err)
	}
}

// postUsage posts a usage record to the server and returns the recorder.
func postUsage(t *testing.T, handler http.Handler, tok, apiKeyID, modelID string, in, out int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"api_key_id":    apiKeyID,
		"model_id":      modelID,
		"input_tokens":  in,
		"output_tokens": out,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/usage", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("X-Purser-Internal-Token", tok)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandleRecordUsageValidToken(t *testing.T) {
	srv, reg := newTestServerWithToken(t, "secret-token")
	seedKey(t, reg, "k1", "acme")

	rec := postUsage(t, srv.Handler(), "secret-token", "k1", "llama-3-8b", 100, 50)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("ok = %v, want true", resp["ok"])
	}
}

func TestHandleRecordUsageWrongToken(t *testing.T) {
	srv, reg := newTestServerWithToken(t, "secret-token")
	seedKey(t, reg, "k1", "acme")

	rec := postUsage(t, srv.Handler(), "wrong-token", "k1", "llama-3-8b", 100, 50)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRecordUsageFailsClosedWhenKeysExist(t *testing.T) {
	// GAP-02: once API keys exist and no InternalToken is configured, the
	// management API must reject unauthenticated requests (fail-closed). The old
	// "open endpoint when InternalToken is empty" behaviour only applies in
	// pure dev/bootstrap mode (no API keys in the registry at all).
	srv, reg := newTestServer(t)
	seedKey(t, reg, "k1", "acme") // bootstrapped → fail-closed kicks in

	rec := postUsage(t, srv.Handler(), "", "k1", "llama-3-8b", 100, 50)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (fail-closed) when API keys exist and no token provided, got %d; body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestHandleGetKeyUsage(t *testing.T) {
	// Use an InternalToken so usage POSTs pass through the new fail-closed RBAC,
	// and an admin Bearer token for management GETs.
	srv, reg := newTestServerWithToken(t, "test-tok")
	adminToken := seedKeyWithRole(t, reg, "admin-k", "admin-key", "admin")
	seedKey(t, reg, "k1", "acme")

	// Record two requests via the internal gateway token.
	postUsage(t, srv.Handler(), "test-tok", "k1", "m1", 100, 40)
	postUsage(t, srv.Handler(), "test-tok", "k1", "m1", 200, 60)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys/k1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var s registry.KeyUsageSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if s.APIKeyID != "k1" {
		t.Errorf("api_key_id = %q, want k1", s.APIKeyID)
	}
	if s.TotalRequests != 2 {
		t.Errorf("total_requests = %d, want 2", s.TotalRequests)
	}
	if s.InputTokens != 300 {
		t.Errorf("input_tokens = %d, want 300", s.InputTokens)
	}
	if s.OutputTokens != 100 {
		t.Errorf("output_tokens = %d, want 100", s.OutputTokens)
	}
}

func TestHandleGetKeyUsageNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys/no-such-key/usage", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleUsageSummary(t *testing.T) {
	// Use InternalToken for usage POSTs and admin Bearer for the summary GET.
	srv, reg := newTestServerWithToken(t, "test-tok")
	adminToken := seedKeyWithRole(t, reg, "admin-k", "admin-key", "admin")
	seedKey(t, reg, "k1", "acme")
	seedKey(t, reg, "k2", "beta")

	postUsage(t, srv.Handler(), "test-tok", "k1", "m1", 100, 40)
	postUsage(t, srv.Handler(), "test-tok", "k1", "m1", 200, 60)
	postUsage(t, srv.Handler(), "test-tok", "k2", "m2", 300, 100)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/summary", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Tenants []registry.TenantUsage `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if len(body.Tenants) != 2 {
		t.Fatalf("tenants count = %d, want 2", len(body.Tenants))
	}
	// acme first (alphabetical).
	acme := body.Tenants[0]
	if acme.Tenant != "acme" {
		t.Errorf("first tenant = %q, want acme", acme.Tenant)
	}
	if acme.TotalRequests != 2 || acme.InputTokens != 300 || acme.OutputTokens != 100 {
		t.Errorf("acme mismatch: %+v", acme)
	}
	beta := body.Tenants[1]
	if beta.Tenant != "beta" || beta.TotalRequests != 1 || beta.InputTokens != 300 || beta.OutputTokens != 100 {
		t.Errorf("beta mismatch: %+v", beta)
	}
}

func TestHandleUsageSummaryEmpty(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/summary", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Tenants []registry.TenantUsage `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Tenants == nil || len(body.Tenants) != 0 {
		t.Errorf("tenants = %v, want empty slice", body.Tenants)
	}
}
