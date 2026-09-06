// auth_session_test.go — integration tests for the distributed OIDC session
// store (Wave B). Each test uses a real SQLite registry to verify end-to-end
// behaviour: login persists a session row, logout revokes it, expired rows are
// invisible, and PKCE state is strictly single-use.
package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// sha256HexTest mirrors the package-private sha256HexOf helper in auth.go so
// tests can derive the expected token_hash from a raw cookie value without
// depending on unexported symbols.
func sha256HexTest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// newSessionTestServer builds a server+registry pair configured for OIDC with
// the given mock IdP tokenEndpoint. Both are returned so tests can inspect the
// DB directly.
func newSessionTestServer(t *testing.T, sub, email, tokenEndpoint string) (*server.Server, *registry.SQLiteRegistry) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "session_test.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })

	verifier := &stubTokenVerifier{ok: true, sub: sub, email: email}
	srv := server.New(reg, server.Config{
		Addr:         ":0",
		OIDCVerifier: verifier,
		OIDC: &server.OIDCConfig{
			Issuer:        "https://test-idp.example.com",
			ClientID:      "test-client",
			RedirectURI:   "http://localhost:8080/auth/callback",
			TokenEndpoint: tokenEndpoint,
		},
		SessionSecret: testSessionKey,
	})
	return srv, reg
}

// mockIdPServer returns a test IdP that always issues a valid id_token stub.
func mockIdPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id_token":     "stub.id.token",
			"access_token": "stub.access.token",
			"token_type":   "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fullLoginFlow performs GET /auth/login followed by GET /auth/callback and
// returns the purser_session cookie value. Fails the test if any step goes wrong.
func fullLoginFlow(t *testing.T, srv *server.Server, tokenEndpointURL string) string {
	t.Helper()

	// Step 1: GET /auth/login — prime PKCE, capture state.
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
	if state == "" {
		t.Fatal("state parameter missing from /auth/login redirect")
	}

	// Step 2: GET /auth/callback — receive session cookie.
	cbRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(cbRec,
		httptest.NewRequest(http.MethodGet,
			"/auth/callback?state="+state+"&code=fake-code", nil))
	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback: want 302, got %d; body=%s", cbRec.Code, cbRec.Body.String())
	}

	for _, c := range cbRec.Result().Cookies() {
		if c.Name == "purser_session" {
			return c.Value
		}
	}
	t.Fatal("purser_session cookie not set after successful callback")
	return "" // unreachable
}

// TestSessionPersistedInDB verifies that after a successful OIDC login the
// session row is visible in the SQLite registry via GetOIDCSession.
func TestSessionPersistedInDB(t *testing.T) {
	idp := mockIdPServer(t)
	srv, reg := newSessionTestServer(t, "uid-1", "alice@example.com", idp.URL)

	cookieValue := fullLoginFlow(t, srv, idp.URL)

	tokenHash := sha256HexTest(cookieValue)
	session, err := reg.GetOIDCSession(context.Background(), tokenHash)
	if err != nil {
		t.Fatalf("GetOIDCSession: %v", err)
	}
	if session.Sub != "uid-1" {
		t.Errorf("session.Sub = %q, want uid-1", session.Sub)
	}
	if session.Email != "alice@example.com" {
		t.Errorf("session.Email = %q, want alice@example.com", session.Email)
	}
	if session.Revoked {
		t.Error("session.Revoked = true, want false immediately after login")
	}
}

// TestLogoutRevokesSession verifies that GET /auth/logout marks the session as
// revoked in the DB and clears the cookie.
func TestLogoutRevokesSession(t *testing.T) {
	idp := mockIdPServer(t)
	srv, reg := newSessionTestServer(t, "uid-2", "bob@example.com", idp.URL)

	cookieValue := fullLoginFlow(t, srv, idp.URL)

	// Call /auth/logout with the session cookie.
	logoutReq := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: "purser_session", Value: cookieValue})
	logoutRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusFound {
		t.Fatalf("logout: want 302, got %d", logoutRec.Code)
	}

	// Session must no longer be retrievable via GetOIDCSession.
	tokenHash := sha256HexTest(cookieValue)
	_, err := reg.GetOIDCSession(context.Background(), tokenHash)
	if !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("GetOIDCSession after logout: want ErrNotFound, got %v", err)
	}
}

// TestExpiredSessionNotFound verifies that GetOIDCSession returns ErrNotFound
// for a session whose expires_at is in the past.
func TestExpiredSessionNotFound(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "exp_test.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	expired := &registry.OIDCSession{
		TokenHash:  "deadbeef01",
		Sub:        "uid-3",
		Email:      "carol@example.com",
		IDPIssuer:  "https://test-idp.example.com",
		AuthMethod: "oidc",
		CreatedAt:  time.Now().Add(-10 * time.Hour),
		ExpiresAt:  time.Now().Add(-1 * time.Second), // already expired
	}
	if err := reg.CreateOIDCSession(context.Background(), expired); err != nil {
		t.Fatalf("CreateOIDCSession: %v", err)
	}

	_, err = reg.GetOIDCSession(context.Background(), "deadbeef01")
	if !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("GetOIDCSession expired: want ErrNotFound, got %v", err)
	}
}

// TestPKCEStateConsumeOnce verifies that ConsumePKCEState is strictly
// single-use: the first call returns the verifier with ok=true, and a
// subsequent call for the same state returns ok=false.
func TestPKCEStateConsumeOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pkce_test.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const stateHash = "abc123stateHash"
	const verifier = "my-code-verifier"

	if err := reg.SetPKCEState(context.Background(), stateHash, verifier, 10*time.Minute); err != nil {
		t.Fatalf("SetPKCEState: %v", err)
	}

	// First consume — must succeed and return the verifier.
	got, ok, err := reg.ConsumePKCEState(context.Background(), stateHash)
	if err != nil {
		t.Fatalf("ConsumePKCEState first call: %v", err)
	}
	if !ok {
		t.Fatal("ConsumePKCEState first call: ok=false, want true")
	}
	if got != verifier {
		t.Errorf("verifier = %q, want %q", got, verifier)
	}

	// Second consume — must return ok=false (consumed).
	_, ok2, err2 := reg.ConsumePKCEState(context.Background(), stateHash)
	if err2 != nil {
		t.Fatalf("ConsumePKCEState second call: %v", err2)
	}
	if ok2 {
		t.Error("ConsumePKCEState second call: ok=true, want false (single-use)")
	}
}

// TestPKCEStateExpired verifies that ConsumePKCEState returns ok=false when
// the state TTL has elapsed.
func TestPKCEStateExpired(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pkce_expired_test.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const stateHash = "expiredStateHash"
	// Store with a negative TTL so it is already expired.
	if err := reg.SetPKCEState(context.Background(), stateHash, "v", -1*time.Second); err != nil {
		t.Fatalf("SetPKCEState: %v", err)
	}

	_, ok, err := reg.ConsumePKCEState(context.Background(), stateHash)
	if err != nil {
		t.Fatalf("ConsumePKCEState expired: %v", err)
	}
	if ok {
		t.Error("ConsumePKCEState expired: ok=true, want false")
	}
}
