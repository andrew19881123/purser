// auth_ldap_test.go — unit tests for the LDAP authentication endpoints.
//
// Tests use a stubLDAPAuthenticator injected via Config.LDAPConnector so no
// real LDAP server is required.
package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/ldapauth"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// stubLDAPAuthenticator is a test double for server.LDAPAuthenticator.
// Set info for a successful authentication, err for a failure.
type stubLDAPAuthenticator struct {
	info *ldapauth.UserInfo
	err  error
}

func (s *stubLDAPAuthenticator) Authenticate(_ context.Context, _, _ string) (*ldapauth.UserInfo, error) {
	return s.info, s.err
}

// newLDAPTestServer builds a test server with the given LDAP authenticator.
// If connector is nil, the server runs without LDAP (ldapConnector == nil).
func newLDAPTestServer(t *testing.T, connector server.LDAPAuthenticator) *server.Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "ldap_test.db")
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
		LDAPConnector: connector,
	})
}

// TestLDAPLogin_NotConfigured_Returns404 verifies that POST /auth/ldap-login
// returns 404 when no LDAP connector is wired into the server.
func TestLDAPLogin_NotConfigured_Returns404(t *testing.T) {
	srv := newLDAPTestServer(t, nil)

	form := url.Values{"username": {"alice"}, "password": {"secret"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/ldap-login",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestLDAPLogin_EmptyCredentials_Returns400 verifies that missing username or
// password yields 400 Bad Request before any LDAP call is made.
func TestLDAPLogin_EmptyCredentials_Returns400(t *testing.T) {
	stub := &stubLDAPAuthenticator{info: &ldapauth.UserInfo{Email: "alice@example.com", DN: "cn=alice,dc=example,dc=com"}}
	srv := newLDAPTestServer(t, stub)

	cases := []url.Values{
		{"username": {""}, "password": {"secret"}},
		{"username": {"alice"}, "password": {""}},
		{},
	}
	for _, form := range cases {
		req := httptest.NewRequest(http.MethodPost, "/auth/ldap-login",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("form=%v: status = %d, want 400", form, rec.Code)
		}
	}
}

// TestLDAPLogin_InvalidCredentials_Returns401 verifies that wrong credentials
// yield 401 Unauthorized.
func TestLDAPLogin_InvalidCredentials_Returns401(t *testing.T) {
	stub := &stubLDAPAuthenticator{err: ldapauth.ErrInvalidCredentials}
	srv := newLDAPTestServer(t, stub)

	form := url.Values{"username": {"alice"}, "password": {"wrongpassword"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/ldap-login",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestLDAPLogin_LDAPUnavailable_Returns503 verifies that an unreachable LDAP
// server surfaces as 503 Service Unavailable.
func TestLDAPLogin_LDAPUnavailable_Returns503(t *testing.T) {
	stub := &stubLDAPAuthenticator{err: ldapauth.ErrLDAPUnavailable}
	srv := newLDAPTestServer(t, stub)

	form := url.Values{"username": {"alice"}, "password": {"secret"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/ldap-login",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// TestLDAPLoginForm_NotConfigured_RedirectsToSSO verifies that GET /auth/ldap-login
// redirects to /auth/login when no LDAP connector is configured.
func TestLDAPLoginForm_NotConfigured_RedirectsToSSO(t *testing.T) {
	srv := newLDAPTestServer(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/ldap-login", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("Location = %q, want /auth/login", loc)
	}
}
