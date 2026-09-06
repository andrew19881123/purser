package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// seedAPIKey inserts a minimal APIKey row directly into the registry so tests
// do not depend on the POST /apikeys handler.
func seedAPIKey(t *testing.T, reg registry.Registry, id, name, tenant string) {
	t.Helper()
	if err := reg.CreateAPIKey(context.Background(), &registry.APIKey{
		ID:      id,
		Name:    name,
		Tenant:  tenant,
		KeyHash: "deadbeef",
		Enabled: true,
		Quota:   500,
	}); err != nil {
		t.Fatalf("seed api key %q: %v", id, err)
	}
}

// TestListAPIKeys_Empty confirms GET /api/v1/apikeys returns {"apikeys":[]}
// (never null) when no keys exist.
func TestListAPIKeys_Empty(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		APIKeys []*registry.APIKey `json:"apikeys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if body.APIKeys == nil {
		t.Errorf("apikeys must be [] not null; raw=%s", rec.Body.String())
	}
	if len(body.APIKeys) != 0 {
		t.Errorf("apikeys len = %d, want 0", len(body.APIKeys))
	}
}

// TestListAPIKeys_AfterCreate confirms that a key created via POST /apikeys is
// visible in GET /apikeys and that the secret (key field) is never present in
// the list response, while all expected metadata fields are present.
func TestListAPIKeys_AfterCreate(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	// Create a key via the POST endpoint (dev mode — no keys exist yet so
	// unauthenticated access is allowed for bootstrapping).
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec,
		httptest.NewRequest(http.MethodPost, "/api/v1/apikeys",
			strings.NewReader(`{"name":"test-key","tenant":"acme","quota":100}`)))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body=%s", createRec.Code, createRec.Body.String())
	}
	var createResp map[string]any
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)
	createdID, _ := createResp["id"].(string)
	if createdID == "" {
		t.Fatalf("create response missing id; body=%s", createRec.Body.String())
	}
	// Extract the plaintext key so subsequent requests can authenticate.
	// After the first key is created the server enforces fail-closed auth.
	createdKey, _ := createResp["key"].(string)

	// List using the newly-minted key — fail-closed is now active.
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil)
	listReq.Header.Set("Authorization", "Bearer "+createdKey)
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: status = %d, body=%s", listRec.Code, listRec.Body.String())
	}

	var listBody struct {
		APIKeys []*registry.APIKey `json:"apikeys"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list body: %v", err)
	}
	if len(listBody.APIKeys) != 1 {
		t.Fatalf("apikeys len = %d, want 1", len(listBody.APIKeys))
	}
	k := listBody.APIKeys[0]
	if k.ID != createdID {
		t.Errorf("listed key id = %q, want %q", k.ID, createdID)
	}
	if k.Name != "test-key" {
		t.Errorf("listed key name = %q, want test-key", k.Name)
	}
	if k.Tenant != "acme" {
		t.Errorf("listed key tenant = %q, want acme", k.Tenant)
	}
	if !k.Enabled {
		t.Errorf("listed key enabled = false, want true")
	}
	// The plaintext secret must NEVER appear in the list response.
	rawList := listRec.Body.String()
	if strings.Contains(rawList, "psk_") {
		t.Errorf("list response contains raw secret (psk_ prefix): %s", rawList)
	}
	// The hash must also be absent (KeyHash carries json:"-").
	if k.KeyHash != "" {
		t.Errorf("list response contains KeyHash = %q, want empty (json:\"-\")", k.KeyHash)
	}
}

// TestDeleteAPIKey_Existing confirms DELETE /api/v1/apikeys/{id} returns 204
// and the key is no longer present in subsequent list responses.
func TestDeleteAPIKey_Existing(t *testing.T) {
	reg := newReg(t)
	// Use seedKeyWithRole so we have a valid Bearer token for authenticated calls.
	// seedAPIKey produces an unreachable fake hash — unusable for Bearer auth.
	adminToken := seedKeyWithRole(t, reg, "key-admin", "admin-key", "admin")
	seedAPIKey(t, reg, "key-del", "to-revoke", "tenant-x")
	srv := server.New(reg, server.Config{})

	// Delete the key using the admin Bearer token.
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/apikeys/key-del", nil)
	delReq.Header.Set("Authorization", "Bearer "+adminToken)
	delRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body=%s", delRec.Code, delRec.Body.String())
	}
	if delRec.Body.Len() != 0 {
		t.Errorf("204 response must have empty body; got %q", delRec.Body.String())
	}

	// Key must be absent from subsequent list.
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil)
	listReq.Header.Set("Authorization", "Bearer "+adminToken)
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("post-delete list status = %d", listRec.Code)
	}
	var listBody struct {
		APIKeys []*registry.APIKey `json:"apikeys"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list body: %v", err)
	}
	for _, k := range listBody.APIKeys {
		if k.ID == "key-del" {
			t.Errorf("deleted key still appears in list response")
		}
	}

	// Key must also be gone from the registry directly.
	if _, err := reg.GetAPIKey(context.Background(), "key-del"); err == nil {
		t.Error("GetAPIKey still returns the deleted key")
	}
}

// TestDeleteAPIKey_Missing confirms DELETE on a non-existent key returns 404.
func TestDeleteAPIKey_Missing(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/apikeys/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestDeleteAPIKey_SecretNotInList is an explicit table-driven check that the
// raw key hash never leaks through the list endpoint after a round-trip via
// the POST endpoint.
func TestDeleteAPIKey_SecretNotInList(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	// Create the first key unauthenticated (dev mode — no keys exist yet).
	var adminKey string
	{
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec,
			httptest.NewRequest(http.MethodPost, "/api/v1/apikeys",
				strings.NewReader(`{"name":"alpha","tenant":"t1"}`)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create alpha: status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		adminKey, _ = resp["key"].(string)
	}

	// Create the second key using the first as Bearer (fail-closed now active).
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys",
			strings.NewReader(`{"name":"beta","tenant":"t1"}`))
		req.Header.Set("Authorization", "Bearer "+adminKey)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create beta: status = %d; body=%s", rec.Code, rec.Body.String())
		}
	}

	// List with the admin key — confirm no "psk_" or "key_hash" / "keyHash" leaks.
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil)
	listReq.Header.Set("Authorization", "Bearer "+adminKey)
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec, listReq)
	rawList := listRec.Body.String()
	for _, forbidden := range []string{"psk_", "key_hash", "keyHash", "KeyHash"} {
		if strings.Contains(rawList, forbidden) {
			t.Errorf("list response contains forbidden field/value %q: %s", forbidden, rawList)
		}
	}

	var listBody struct {
		APIKeys []*registry.APIKey `json:"apikeys"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listBody.APIKeys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(listBody.APIKeys))
	}
	for _, k := range listBody.APIKeys {
		if k.KeyHash != "" {
			t.Errorf("key %q has non-empty KeyHash in list response: %q", k.ID, k.KeyHash)
		}
	}
}
