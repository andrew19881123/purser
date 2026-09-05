package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// seedKeyWithRole inserts an API key with the given role and returns its
// plaintext token. The key hash is sha256("psk_test_" + id) so the token
// is deterministic and can be sent in test requests.
func seedKeyWithRole(t *testing.T, reg registry.Registry, id, name, role string) string {
	t.Helper()
	plaintext := "psk_test_" + id
	sum := sha256.Sum256([]byte(plaintext))
	hash := hex.EncodeToString(sum[:])
	if err := reg.CreateAPIKey(context.Background(), &registry.APIKey{
		ID:      id,
		Name:    name,
		KeyHash: hash,
		Tenant:  "test",
		Role:    role,
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed key %q: %v", id, err)
	}
	return plaintext
}

// TestRBACMiddleware_AdminAllowed verifies that an admin key may POST (mutating
// requests are not restricted).
func TestRBACMiddleware_AdminAllowed(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-admin", "admin-key", "admin")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys",
		strings.NewReader(`{"name":"new","tenant":"t"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin key on POST got 403, want allowed; body=%s", rec.Body.String())
	}
}

// TestRBACMiddleware_ViewerGetAllowed verifies that a viewer key may issue GET
// requests to management endpoints.
func TestRBACMiddleware_ViewerGetAllowed(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-viewer-get", "viewer-get", "viewer")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("viewer key on GET got 403, want allowed; body=%s", rec.Body.String())
	}
}

// TestRBACMiddleware_ViewerPostForbidden verifies that a viewer key gets 403
// on any non-GET request.
func TestRBACMiddleware_ViewerPostForbidden(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-viewer-post", "viewer-post", "viewer")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys",
		strings.NewReader(`{"name":"new","tenant":"t"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer key on POST got %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRBACMiddleware_InferenceKeyForbidden verifies that an inference key is
// forbidden from all /api/v1/* management endpoints.
func TestRBACMiddleware_InferenceKeyForbidden(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-inference", "inference-key", "inference")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("inference key on /api/v1 got %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRBACMiddleware_InternalTokenPassThrough verifies that the configured
// internal token bypasses RBAC entirely (used by the gateway for route-sync).
func TestRBACMiddleware_InternalTokenPassThrough(t *testing.T) {
	reg := newReg(t)
	const internalToken = "secret-internal-token"
	srv := server.New(reg, server.Config{InternalToken: internalToken})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+internalToken)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("internal token got 403, want pass-through; body=%s", rec.Body.String())
	}
}

// TestRBACMiddleware_PublicEndpointBypass verifies that GET /api/v1/cluster/health
// is always accessible, even when the request carries an inference key that
// would normally be forbidden on management endpoints.
func TestRBACMiddleware_PublicEndpointBypass(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-inf-public", "inference-pub", "inference")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("GET /cluster/health with inference key got 403, want public bypass; body=%s", rec.Body.String())
	}
}
