package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// testSessionKey is a deterministic 32-byte HMAC key used across PKCE tests.
var testSessionKey = func() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}()

// newPKCEServer builds a test server with OIDC + PKCE configured.
// tokenEndpoint is the URL the server will POST to when exchanging the auth code.
func newPKCEServer(t *testing.T, verifier server.TokenVerifier, tokenEndpoint string) *server.Server {
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
		Addr:         ":0",
		OIDCVerifier: verifier,
		OIDC: &server.OIDCConfig{
			Issuer:        "https://mock-idp.example.com",
			ClientID:      "test-client-id",
			RedirectURI:   "http://localhost:8080/auth/callback",
			TokenEndpoint: tokenEndpoint,
		},
		SessionSecret: testSessionKey,
	})
}

// TestPKCEFlowGeneratesChallenge verifies that GET /auth/login:
//   - returns 302 to the IdP authorization URL;
//   - includes a valid base64url S256 code_challenge (43 chars, 32-byte SHA256);
//   - sets code_challenge_method=S256;
//   - includes a 64-character hex state parameter (32 random bytes).
func TestPKCEFlowGeneratesChallenge(t *testing.T) {
	srv := newPKCEServer(t, &stubTokenVerifier{ok: true}, "http://mock-idp.example.com/token")

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("want 302 redirect, got %d; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("Location header missing in /auth/login response")
	}
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location %q: %v", loc, err)
	}

	// code_challenge must be a valid base64url-encoded SHA256 hash (43 chars, no padding).
	challenge := parsed.Query().Get("code_challenge")
	if challenge == "" {
		t.Fatal("code_challenge parameter missing from authorization URL")
	}
	if got := len(challenge); got != 43 {
		t.Errorf("code_challenge length = %d, want 43 (base64url of 32-byte SHA256)", got)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	if err != nil {
		t.Errorf("code_challenge is not valid base64url: %v", err)
	}
	if got := len(decoded); got != sha256.Size {
		t.Errorf("decoded challenge = %d bytes, want %d (SHA256 output size)", got, sha256.Size)
	}

	// code_challenge_method must be S256.
	if m := parsed.Query().Get("code_challenge_method"); m != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", m)
	}

	// state must be 64 hex characters (32 random bytes).
	state := parsed.Query().Get("state")
	if got := len(state); got != 64 {
		t.Errorf("state length = %d, want 64 (hex of 32 random bytes)", got)
	}
}

// TestCallbackValidatesState verifies:
//   - an unknown / wrong state parameter → 400 with error="invalid_state";
//   - a state issued by /auth/login is accepted and the request proceeds past
//     state validation (any subsequent failure is not invalid_state).
func TestCallbackValidatesState(t *testing.T) {
	t.Run("wrong_state", func(t *testing.T) {
		srv := newPKCEServer(t, &stubTokenVerifier{ok: true}, "http://mock-idp.example.com/token")

		req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=nosuchthing&code=x", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400 for unknown state, got %d; body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode 400 body: %v; raw=%s", err, rec.Body.String())
		}
		if body.Error != "invalid_state" {
			t.Errorf("error = %q, want invalid_state", body.Error)
		}
	})

	t.Run("valid_state", func(t *testing.T) {
		// Mock token endpoint that returns an IdP-side error — code exchange
		// fails, but this is unrelated to state validation.
		mockIdP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":             "invalid_grant",
				"error_description": "fake code — expected in test",
			})
		}))
		defer mockIdP.Close()

		srv := newPKCEServer(t, &stubTokenVerifier{ok: true}, mockIdP.URL)

		// Prime a valid state via /auth/login.
		loginRec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(loginRec,
			httptest.NewRequest(http.MethodGet, "/auth/login", nil))
		if loginRec.Code != http.StatusFound {
			t.Fatalf("login: want 302, got %d", loginRec.Code)
		}
		loc, err := url.Parse(loginRec.Header().Get("Location"))
		if err != nil {
			t.Fatalf("parse Location: %v", err)
		}
		state := loc.Query().Get("state")

		// Call /auth/callback with the valid state.
		cbReq := httptest.NewRequest(http.MethodGet,
			"/auth/callback?state="+state+"&code=fake-code", nil)
		cbRec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(cbRec, cbReq)

		// State was accepted — the error must NOT be invalid_state.
		if cbRec.Code == http.StatusBadRequest {
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(cbRec.Body.Bytes(), &body)
			if body.Error == "invalid_state" {
				t.Fatalf("state issued by /auth/login was rejected as invalid_state; body=%s",
					cbRec.Body.String())
			}
		}
	})
}

// TestSessionCookieAccepted verifies the full SSO path:
//  1. GET /auth/login → 302 to IdP authorization URL (captures state).
//  2. GET /auth/callback with the real state + a mock IdP token endpoint →
//     302 to / + sets purser_session cookie.
//  3. The session cookie is accepted by oidcMiddleware on a protected endpoint
//     without a Bearer token.
func TestSessionCookieAccepted(t *testing.T) {
	// Mock token endpoint: returns a minimal valid id_token.
	// The stub verifier accepts any id_token without network calls.
	mockIdP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id_token":     "stub.id.token",
			"access_token": "stub.access.token",
			"token_type":   "Bearer",
		})
	}))
	defer mockIdP.Close()

	srv := newPKCEServer(t,
		&stubTokenVerifier{ok: true, sub: "user-42", email: "alice@example.com"},
		mockIdP.URL,
	)

	// Step 1: GET /auth/login — prime the PKCE store, capture state.
	loginRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(loginRec,
		httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login: want 302, got %d; body=%s", loginRec.Code, loginRec.Body.String())
	}
	loc, err := url.Parse(loginRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	state := loc.Query().Get("state")

	// Step 2: GET /auth/callback — exchange code, receive session cookie.
	cbRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(cbRec,
		httptest.NewRequest(http.MethodGet,
			"/auth/callback?state="+state+"&code=any-code", nil))
	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback: want 302, got %d; body=%s", cbRec.Code, cbRec.Body.String())
	}

	var sessionCookie *http.Cookie
	for _, c := range cbRec.Result().Cookies() {
		if c.Name == "purser_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("purser_session cookie not set after successful callback")
	}

	// Step 3: Use the session cookie on a protected endpoint — no Bearer token.
	apiReq := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	apiReq.AddCookie(sessionCookie)
	apiRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(apiRec, apiReq)

	if apiRec.Code != http.StatusOK {
		t.Fatalf("session cookie not accepted by middleware: want 200, got %d; body=%s",
			apiRec.Code, apiRec.Body.String())
	}
}

// TestOIDCDisabledPassthrough is also covered in oidc_test.go; included here
// as a regression guard for the auth-flow additions.
func TestPKCEOIDCDisabledPassthrough(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })

	// No OIDC verifier — OIDC is disabled.
	srv := server.New(reg, server.Config{Addr: ":0"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("want non-401 when OIDC is disabled, got 401")
	}
}
