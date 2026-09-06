package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newGDPRServer builds a server with the "gdpr" enterprise feature licensed.
// Returns the server and the underlying registry for direct DB inspection.
func newGDPRServer(t *testing.T) (*server.Server, *registry.SQLiteRegistry) {
	t.Helper()
	now := time.Now().UTC()
	lic := signedLicense(t, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"gdpr", "audit"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return server.New(reg, server.Config{Addr: ":0", License: lic}), reg
}

// newGDPRServerWithAdminKey creates a server with GDPR feature, an admin API
// key, and returns the server, registry, and the plaintext API key for auth.
func newGDPRServerWithAdminKey(t *testing.T) (*server.Server, *registry.SQLiteRegistry, string) {
	t.Helper()
	srv, reg := newGDPRServer(t)

	// Create an admin key via the bootstrap path (no existing keys = no auth required).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create admin key: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode key response: %v", err)
	}
	tok, _ := body["key"].(string)
	if tok == "" {
		t.Fatal("no key returned")
	}
	return srv, reg, tok
}

// insertInferenceEvents directly inserts N inference_audit_log rows for the
// given api_key_hash via raw SQL on the DB handle so we bypass the hash-chain
// lock and keep the test fast and deterministic.
func insertInferenceEvents(t *testing.T, reg *registry.SQLiteRegistry, apiKeyHash string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		_, err := reg.DB().ExecContext(ctx,
			`INSERT INTO inference_audit_log
			 (request_id, api_key_hash, model_id, tenant_id, timestamp,
			  prompt_tokens, completion_tokens, endpoint, client_ip_prefix,
			  latency_ms, finish_reason)
			 VALUES (?, ?, 'test-model', 'test-tenant', CURRENT_TIMESTAMP,
			         100, 50, 'openai', '192.168.1.0/24', 42.5, 'stop')`,
			fmt.Sprintf("req-%s-%d", apiKeyHash[:8], i),
			apiKeyHash,
		)
		if err != nil {
			t.Fatalf("insert inference event %d: %v", i, err)
		}
	}
	// Verify the rows were inserted.
	var count int
	if err := reg.DB().QueryRowContext(ctx,
		"SELECT count(*) FROM inference_audit_log WHERE api_key_hash=?", apiKeyHash,
	).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != n {
		t.Fatalf("inserted %d rows, want %d", count, n)
	}
}

// postErasure sends a POST /api/v1/gdpr/erasure request.
func postErasure(t *testing.T, srv *server.Server, tok, subjectHash, reason string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"subject_type":       "api_key",
		"subject_identifier": subjectHash,
		"reason":             reason,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/gdpr/erasure", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestGDPRErasure_RequiresAdmin verifies that a viewer-role key receives 403.
func TestGDPRErasure_RequiresAdmin(t *testing.T) {
	srv, reg, adminTok := newGDPRServerWithAdminKey(t)

	// Create a viewer key via the admin.
	rec := httptest.NewRecorder()
	reqBody, _ := json.Marshal(map[string]any{"name": "viewer", "role": "viewer"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTok)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create viewer key: %d %s", rec.Code, rec.Body.String())
	}
	var viewerResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &viewerResp)
	viewerTok, _ := viewerResp["key"].(string)
	if viewerTok == "" {
		t.Fatal("no viewer key returned")
	}
	_ = reg // satisfy linter; registry used indirectly

	// Viewer key should get 403 from rbacMiddleware (POST is a write).
	erasureRec := postErasure(t, srv, viewerTok, "deadbeef", "test")
	if erasureRec.Code != http.StatusForbidden {
		t.Errorf("viewer key: got %d, want 403; body=%s", erasureRec.Code, erasureRec.Body.String())
	}
}

// TestGDPRErasure_ErasesInferenceEvents is the primary happy-path test:
//   - Insert 10 inference_audit_log rows for subject X.
//   - POST /api/v1/gdpr/erasure with subject_identifier = X.
//   - Verify events_erased == 10 in the response.
//   - Verify the rows in the DB now have api_key_hash = "ERASED-YYYYMMDD".
//   - Verify gdpr_erasure_log has exactly one row.
func TestGDPRErasure_ErasesInferenceEvents(t *testing.T) {
	srv, reg, adminTok := newGDPRServerWithAdminKey(t)

	// Use a deterministic subject hash.
	subjectHash := "a1b2c3d4e5f60001a1b2c3d4e5f60001a1b2c3d4e5f60001a1b2c3d4e5f60001"
	insertInferenceEvents(t, reg, subjectHash, 10)

	rec := postErasure(t, srv, adminTok, subjectHash, "user requested GDPR Art.17 deletion")
	if rec.Code != http.StatusOK {
		t.Fatalf("erasure: %d %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// erased_events should be 10.
	erasedEvents, _ := resp["erased_events"].(float64)
	if int(erasedEvents) != 10 {
		t.Errorf("erased_events = %v, want 10", erasedEvents)
	}
	if resp["erasure_type"] != "inference_audit" {
		t.Errorf("erasure_type = %v, want inference_audit", resp["erasure_type"])
	}
	if resp["completed_at"] == nil || resp["completed_at"] == "" {
		t.Error("completed_at missing")
	}

	// Check DB: api_key_hash should now start with "ERASED-".
	ctx := context.Background()
	eventsResp, err := reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("list inference events: %v", err)
	}
	for _, evt := range eventsResp.Events {
		if !strings.HasPrefix(evt.APIKeyHash, "ERASED-") {
			t.Errorf("event %s: api_key_hash = %q, want prefix ERASED-", evt.RequestID, evt.APIKeyHash)
		}
		if evt.ClientIPPrefix != "0.0.0.0/0" {
			t.Errorf("event %s: client_ip_prefix = %q, want 0.0.0.0/0", evt.RequestID, evt.ClientIPPrefix)
		}
	}

	// Check gdpr_erasure_log has one row.
	rows, err := reg.DB().QueryContext(ctx, "SELECT count(*) FROM gdpr_erasure_log WHERE subject_hash=?", subjectHash)
	if err != nil {
		t.Fatalf("query gdpr_erasure_log: %v", err)
	}
	defer rows.Close()
	var count int
	if rows.Next() {
		_ = rows.Scan(&count)
	}
	if count != 1 {
		t.Errorf("gdpr_erasure_log rows = %d, want 1", count)
	}
}

// TestActorFromRequest_APIKey verifies that an authenticated request with a
// Bearer token produces an actor string of the form "apikey:XXXXXXXX".
func TestActorFromRequest_APIKey(t *testing.T) {
	srv, _, adminTok := newGDPRServerWithAdminKey(t)

	// Create a model (which appends an audit entry) using the admin key.
	modelSpec := `{"modelId":"test-model-actor","family":"llama","architecture":"transformer","paramsTotalB":7.0,"engine":"llama.cpp"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models", strings.NewReader(modelSpec))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTok)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create model: %d %s", rec.Code, rec.Body.String())
	}

	// The actor in the audit log must be "apikey:<8 hex chars>" not "api".
	// Compute the expected prefix.
	sum := sha256.Sum256([]byte(adminTok))
	wantPrefix := "apikey:" + hex.EncodeToString(sum[:])[:8]

	// Read audit log directly from registry.
	// We use the enterprise audit-log endpoint which requires the "audit" feature.
	// Since newGDPRServer includes "audit", we can call it.
	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/enterprise/audit-log", nil)
	auditReq.Header.Set("Authorization", "Bearer "+adminTok)
	auditRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("audit log: %d %s", auditRec.Code, auditRec.Body.String())
	}

	var auditBody struct {
		Entries []struct {
			Actor  string `json:"actor"`
			Action string `json:"action"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(auditRec.Body.Bytes(), &auditBody); err != nil {
		t.Fatalf("decode audit log: %v", err)
	}

	// Find the model.created entry and check its actor.
	var found bool
	for _, e := range auditBody.Entries {
		if e.Action == "model.created" {
			found = true
			if e.Actor != wantPrefix {
				t.Errorf("audit actor = %q, want %q", e.Actor, wantPrefix)
			}
		}
	}
	if !found {
		t.Error("no model.created audit entry found")
	}
}

// TestAuditEntriesHaveGranularActor verifies that the audit actor is not the
// hardcoded literal "api" on any request-driven operation.
func TestAuditEntriesHaveGranularActor(t *testing.T) {
	srv, _, adminTok := newGDPRServerWithAdminKey(t)

	// Create a second key — this should produce an "apikey.created" audit entry.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys", nil)
	req2.Header.Set("Authorization", "Bearer "+adminTok)
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("create 2nd key: %d %s", rec2.Code, rec2.Body.String())
	}

	// Read the audit log and check no actor equals "api".
	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/enterprise/audit-log", nil)
	auditReq.Header.Set("Authorization", "Bearer "+adminTok)
	auditRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("audit log: %d %s", auditRec.Code, auditRec.Body.String())
	}

	var auditBody struct {
		Entries []struct {
			Actor  string `json:"actor"`
			Action string `json:"action"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(auditRec.Body.Bytes(), &auditBody); err != nil {
		t.Fatalf("decode audit log: %v", err)
	}
	if len(auditBody.Entries) == 0 {
		t.Fatal("no audit entries found")
	}
	for _, e := range auditBody.Entries {
		if e.Actor == "api" {
			t.Errorf("audit entry %q still uses hardcoded actor %q, want granular identity", e.Action, e.Actor)
		}
		if e.Actor == "" {
			t.Errorf("audit entry %q has empty actor", e.Action)
		}
	}
}

// TestGDPRErasure_EmptySubjectIdentifier verifies that missing subject_identifier yields 400.
func TestGDPRErasure_EmptySubjectIdentifier(t *testing.T) {
	srv, _, adminTok := newGDPRServerWithAdminKey(t)
	body, _ := json.Marshal(map[string]any{
		"subject_type": "api_key",
		"reason":       "test",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/gdpr/erasure", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTok)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGDPRErasureLog_AdminOnly verifies that the erasure-log endpoint is
// accessible to admin keys and returns the expected shape.
func TestGDPRErasureLog_AdminOnly(t *testing.T) {
	srv, _, adminTok := newGDPRServerWithAdminKey(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/gdpr/erasure-log", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("erasure-log: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["erasures"]; !ok {
		t.Error("response missing 'erasures' field")
	}
}
