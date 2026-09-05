package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// stubTokenVerifier is a test-double for server.TokenVerifier. Set ok=true for
// successful verification; ok=false (with an optional err) to simulate
// invalid or expired tokens. No real HTTP calls are made.
type stubTokenVerifier struct {
	ok    bool
	sub   string
	email string
	err   error
}

func (s *stubTokenVerifier) VerifyToken(_ context.Context, _ string) (string, string, error) {
	if !s.ok {
		if s.err != nil {
			return "", "", s.err
		}
		return "", "", errors.New("stub: token invalid")
	}
	return s.sub, s.email, nil
}

// newOIDCServer builds a test server with a stub TokenVerifier injected via
// Config.OIDCVerifier — no OIDC discovery, no network calls. internalToken, if
// non-empty, enables the gateway X-Purser-Internal-Token exemption path.
func newOIDCServer(t *testing.T, verifier server.TokenVerifier, internalToken string) *server.Server {
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
		OIDCVerifier:  verifier,
		InternalToken: internalToken,
	})
}

// TestOIDCDisabledPassthrough: when Config.OIDCVerifier is nil (OIDC not
// configured), all requests pass through regardless of Authorization headers.
// This also validates that the existing test setup (OIDC == nil) is unaffected.
func TestOIDCDisabledPassthrough(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })

	// No OIDCVerifier set — OIDC is disabled.
	srv := server.New(reg, server.Config{Addr: ":0"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("status = 401, want non-401 (OIDC disabled; all requests must pass through)")
	}
}

// TestOIDCMissingToken: OIDC is enabled but the request carries no
// Authorization header — expect 401 with the standard error body.
func TestOIDCMissingToken(t *testing.T) {
	srv := newOIDCServer(t, &stubTokenVerifier{}, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assertUnauthorized(t, rec)
}

// TestOIDCInvalidToken: OIDC is enabled and the verifier rejects the token
// (simulating an expired or tampered JWT) — expect 401.
func TestOIDCInvalidToken(t *testing.T) {
	srv := newOIDCServer(t, &stubTokenVerifier{
		ok:  false,
		err: errors.New("oidc: token is expired"),
	}, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	req.Header.Set("Authorization", "Bearer bad.jwt.value")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assertUnauthorized(t, rec)
}

// TestOIDCValidToken: OIDC is enabled and the verifier accepts the token —
// the request is forwarded and a normal 200 is returned.
func TestOIDCValidToken(t *testing.T) {
	srv := newOIDCServer(t, &stubTokenVerifier{
		ok:    true,
		sub:   "user-123",
		email: "admin@example.com",
	}, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	req.Header.Set("Authorization", "Bearer valid.jwt.value")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (valid token); body=%s", rec.Code, rec.Body.String())
	}
}

// TestOIDCInternalTokenExempt: a request carrying the correct
// X-Purser-Internal-Token header bypasses OIDC entirely so the gateway can
// perform route-sync without a human token.
func TestOIDCInternalTokenExempt(t *testing.T) {
	// The stub rejects all tokens — but the internal-token path must bypass it.
	srv := newOIDCServer(t, &stubTokenVerifier{ok: false}, "secret-gateway-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	req.Header.Set("X-Purser-Internal-Token", "secret-gateway-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (internal token exempt); body=%s", rec.Code, rec.Body.String())
	}
}

// TestOIDCWrongInternalToken: an incorrect X-Purser-Internal-Token value does
// NOT grant exemption — the request still requires a valid Bearer token.
func TestOIDCWrongInternalToken(t *testing.T) {
	srv := newOIDCServer(t, &stubTokenVerifier{ok: false}, "correct-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	req.Header.Set("X-Purser-Internal-Token", "wrong-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assertUnauthorized(t, rec)
}

// assertUnauthorized checks that rec is a 401 with the standard OIDC error body.
func assertUnauthorized(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 401 body: %v; raw=%s", err, rec.Body.String())
	}
	if body.Error != "unauthorized" {
		t.Errorf("error = %q, want 'unauthorized'", body.Error)
	}
	if body.Message != "valid OIDC token required" {
		t.Errorf("message = %q, want 'valid OIDC token required'", body.Message)
	}
}
