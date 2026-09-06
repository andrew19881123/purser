package server_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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

// newPolicyServer returns a server with the "policy_engine" enterprise feature.
func newPolicyServer(t *testing.T) (*server.Server, registry.Registry) {
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

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	prev := license.VerificationKey
	license.VerificationKey = pub
	t.Cleanup(func() { license.VerificationKey = prev })

	payload := license.Payload{
		Licensee: "test",
		Expires:  time.Now().Add(24 * time.Hour),
		Features: []string{"policy_engine"},
	}
	key, err := license.Sign(priv, payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	lic, err := license.Verify(key)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return server.New(reg, server.Config{Addr: ":0", License: lic}), reg
}

// doRequest is a test helper for arbitrary HTTP methods with an optional body.
func doRequest(t *testing.T, srv *server.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestGetPolicies_NoEnterprise verifies that GET /api/v1/policies without a
// policy_engine license returns 402 Payment Required.
func TestGetPolicies_NoEnterprise(t *testing.T) {
	srv, _ := newTestServer(t) // community edition
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/policies", nil)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGetPolicies_Empty verifies that GET /api/v1/policies with a valid
// enterprise license returns 200 with an empty list when no policies exist.
func TestGetPolicies_Empty(t *testing.T) {
	srv, _ := newPolicyServer(t)
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/policies", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Policies []registry.Policy `json:"policies"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Policies) != 0 {
		t.Errorf("want empty policies list, got %d entries", len(out.Policies))
	}
}

// TestUpsertPolicy_ValidRego verifies that PUT /api/v1/policies/{name} with
// valid Rego returns 200 and the saved policy.
func TestUpsertPolicy_ValidRego(t *testing.T) {
	srv, _ := newPolicyServer(t)
	const validRego = `
package purser
default allow = false
allow = true
`
	rec := doRequest(t, srv, http.MethodPut, "/api/v1/policies/test-policy", map[string]any{
		"rego": validRego,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var p registry.Policy
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Name != "test-policy" {
		t.Errorf("name = %q, want 'test-policy'", p.Name)
	}
	if !p.Enabled {
		t.Error("expected policy to be enabled by default")
	}
}

// TestUpsertPolicy_InvalidRego verifies that PUT /api/v1/policies/{name} with
// syntactically invalid Rego returns 400 Bad Request.
func TestUpsertPolicy_InvalidRego(t *testing.T) {
	srv, _ := newPolicyServer(t)
	rec := doRequest(t, srv, http.MethodPut, "/api/v1/policies/bad-policy", map[string]any{
		"rego": "this is not valid rego !!!",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestUpsertPolicy_NoEnterprise verifies that PUT /api/v1/policies/{name}
// without a policy_engine license returns 402.
func TestUpsertPolicy_NoEnterprise(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doRequest(t, srv, http.MethodPut, "/api/v1/policies/test", map[string]any{
		"rego": "package purser\ndefault allow = true",
	})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
}

// TestDeletePolicy_NotFound verifies that DELETE /api/v1/policies/{name} on a
// non-existent policy returns 404.
func TestDeletePolicy_NotFound(t *testing.T) {
	srv, _ := newPolicyServer(t)
	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/policies/no-such-policy", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestDeletePolicy_Success verifies the full upsert → delete lifecycle.
func TestDeletePolicy_Success(t *testing.T) {
	srv, _ := newPolicyServer(t)
	const validRego = "package purser\ndefault allow = true\n"

	// Create.
	rec := doRequest(t, srv, http.MethodPut, "/api/v1/policies/to-delete", map[string]any{
		"rego": validRego,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// Delete.
	rec = doRequest(t, srv, http.MethodDelete, "/api/v1/policies/to-delete", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// Confirm it is gone.
	rec = doRequest(t, srv, http.MethodGet, "/api/v1/policies", nil)
	var out struct {
		Policies []registry.Policy `json:"policies"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Policies) != 0 {
		t.Errorf("expected no policies after delete, got %d", len(out.Policies))
	}
}

// TestEvalPolicy_NoEnterprise verifies that POST /api/v1/policies/eval without
// a policy_engine license returns 402.
func TestEvalPolicy_NoEnterprise(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/policies/eval", map[string]any{
		"action": "deploy", "model_id": "llama3",
	})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
}

// TestEvalPolicy_NoPolicies verifies that eval with no policies loaded returns
// allowed=true (open-by-default).
func TestEvalPolicy_NoPolicies(t *testing.T) {
	srv, _ := newPolicyServer(t)
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/policies/eval", map[string]any{
		"action": "deploy", "model_id": "llama3",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if allowed, _ := out["allowed"].(bool); !allowed {
		t.Errorf("expected allowed=true with no policies, got %v", out)
	}
}

// TestEvalPolicy_WithRestrictivePolicy verifies that eval correctly denies
// a request that doesn't match the policy.
func TestEvalPolicy_WithRestrictivePolicy(t *testing.T) {
	srv, _ := newPolicyServer(t)
	const modelAllowlistRego = `
package purser
default allow = false
allow if {
    input.model_id == "approved-model"
}
`
	// Install policy.
	rec := doRequest(t, srv, http.MethodPut, "/api/v1/policies/allowlist", map[string]any{
		"rego": modelAllowlistRego,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert policy: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Eval with approved model — should be allowed.
	rec = doRequest(t, srv, http.MethodPost, "/api/v1/policies/eval", map[string]any{
		"action": "deploy", "model_id": "approved-model",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("eval(approved): status=%d", rec.Code)
	}
	var out map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if allowed, _ := out["allowed"].(bool); !allowed {
		t.Errorf("expected allowed=true for approved model, got %v", out)
	}

	// Eval with forbidden model — should be denied.
	rec = doRequest(t, srv, http.MethodPost, "/api/v1/policies/eval", map[string]any{
		"action": "deploy", "model_id": "forbidden-model",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("eval(forbidden): status=%d", rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if allowed, _ := out["allowed"].(bool); allowed {
		t.Errorf("expected allowed=false for forbidden model, got %v", out)
	}
}
