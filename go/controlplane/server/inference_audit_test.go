package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newInferenceAuditServer builds a server with the given license and internal
// token — suitable for testing both the recording (POST) and listing (GET) endpoints.
func newInferenceAuditServer(t *testing.T, lic *license.License, internalToken string) *server.Server {
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
	return server.New(reg, server.Config{
		Addr:          ":0",
		License:       lic,
		InternalToken: internalToken,
	})
}

// signedInferenceAuditLicense returns a valid enterprise license with the
// "inference_audit" feature enabled.
func signedInferenceAuditLicense(t *testing.T) *license.License {
	t.Helper()
	now := time.Now().UTC()
	return signedLicense(t, license.Payload{
		Licensee: "Test Corp",
		Features: []string{"inference_audit"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
}

// postBody issues a POST to path on srv with the given JSON body and headers.
func postBody(t *testing.T, srv *server.Server, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestListInferenceAudit_NoEnterprise verifies that GET /api/v1/inference-audit
// returns 402 Payment Required when no enterprise license is active.
func TestListInferenceAudit_NoEnterprise(t *testing.T) {
	srv := newInferenceAuditServer(t, nil, "")
	rec := get(t, srv, "/api/v1/inference-audit")
	if rec.Code != http.StatusPaymentRequired {
		t.Errorf("want 402, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj == nil {
		t.Errorf("expected error object in body, got %v", body)
	}
	if ft, _ := errObj["feature"].(string); ft != "inference_audit" {
		t.Errorf("error.feature = %q, want inference_audit", ft)
	}
}

// TestListInferenceAudit_WithFeature_Empty verifies that GET /api/v1/inference-audit
// returns 200 with an empty events array when the feature is licensed but no
// events have been recorded.
func TestListInferenceAudit_WithFeature_Empty(t *testing.T) {
	lic := signedInferenceAuditLicense(t)
	srv := newInferenceAuditServer(t, lic, "")
	rec := get(t, srv, "/api/v1/inference-audit")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Events []any `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Events == nil {
		t.Error("events field is null; want an empty array")
	}
	if len(body.Events) != 0 {
		t.Errorf("want 0 events, got %d", len(body.Events))
	}
}

// TestRecordInferenceEvent_WithToken verifies that POST /api/v1/inference-events
// returns 204 No Content when a correct X-Purser-Internal-Token is present.
func TestRecordInferenceEvent_WithToken(t *testing.T) {
	const tok = "test-internal-token-abc123"
	srv := newInferenceAuditServer(t, nil, tok)

	event := registry.InferenceEvent{
		RequestID:        "req-test-001",
		APIKeyHash:       "abc123deadbeef",
		ModelID:          "qwen3-7b",
		TenantID:         "acme",
		Timestamp:        time.Now().UTC(),
		PromptTokens:     10,
		CompletionTokens: 20,
		Endpoint:         "openai",
		ClientIPPrefix:   "203.0.113.0/24",
		LatencyMs:        42.5,
		FinishReason:     "stop",
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	rec := postBody(t, srv, "/api/v1/inference-events", body, map[string]string{
		"X-Purser-Internal-Token": tok,
	})
	if rec.Code != http.StatusNoContent {
		t.Errorf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRecordInferenceEvent_WithoutToken verifies that POST /api/v1/inference-events
// returns 401 Unauthorized when the internal token is absent (and the server
// requires one).
func TestRecordInferenceEvent_WithoutToken(t *testing.T) {
	const tok = "test-internal-token-abc123"
	srv := newInferenceAuditServer(t, nil, tok)

	event := registry.InferenceEvent{
		RequestID: "req-test-002",
		ModelID:   "qwen3-7b",
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	rec := postBody(t, srv, "/api/v1/inference-events", body, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d: %s", rec.Code, rec.Body.String())
	}
}
