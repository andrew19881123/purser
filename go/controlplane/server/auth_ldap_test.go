// auth_ldap_test.go — unit tests for the LDAP authentication endpoints.
//
// Tests use a stubLDAPAuthenticator injected via Config.LDAPConnector so no
// real LDAP server is required.
package server_test

import (
	"context"
	"encoding/json"
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

// ---------------------------------------------------------------------------
// POST /api/v1/ldap/test tests (group mapping, cache, and endpoint behaviour).
// All tests use stub connectors so no real LDAP server is required.
// ---------------------------------------------------------------------------

// stubLDAPWithCache is a test double that implements both LDAPAuthenticator and
// LDAPCacheChecker. Use cachedInfo to simulate a cache hit (non-nil = hit).
type stubLDAPWithCache struct {
	info       *ldapauth.UserInfo
	err        error
	cachedInfo *ldapauth.UserInfo // non-nil → CheckCache returns a hit
}

func (s *stubLDAPWithCache) Authenticate(_ context.Context, _, _ string) (*ldapauth.UserInfo, error) {
	return s.info, s.err
}

func (s *stubLDAPWithCache) CheckCache(_, _ string) *ldapauth.UserInfo {
	return s.cachedInfo
}

// ldapTestRequest sends POST /api/v1/ldap/test with the given username/password
// and returns the recorder.
func ldapTestRequest(t *testing.T, srv *server.Server, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"username":"` + username + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ldap/test", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// decodeLDAPTestResponse decodes the JSON body of a /ldap/test response into a
// map[string]any for easy field inspection.
func decodeLDAPTestResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return resp
}

// TestLDAPGroupMapping_AdminGroup verifies that when Authenticate returns a
// UserInfo with Role "admin", the test endpoint reports mapped_role "admin".
func TestLDAPGroupMapping_AdminGroup(t *testing.T) {
	stub := &stubLDAPWithCache{
		info: &ldapauth.UserInfo{
			DN:       "CN=alice,OU=users,DC=example,DC=com",
			Email:    "alice@example.com",
			Groups:   []string{"purser-admins"},
			GroupDNs: []string{"CN=purser-admins,OU=groups,DC=example,DC=com"},
			Role:     "admin",
		},
	}
	srv := newLDAPTestServer(t, stub)

	rec := ldapTestRequest(t, srv, "alice", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeLDAPTestResponse(t, rec)
	if resp["mapped_role"] != "admin" {
		t.Errorf("mapped_role = %v, want admin", resp["mapped_role"])
	}
	if resp["authenticated"] != true {
		t.Errorf("authenticated = %v, want true", resp["authenticated"])
	}
}

// TestLDAPGroupMapping_NoMatch_DefaultRole verifies that when no group matches
// but a DefaultRole is configured, the connector assigns the default role and
// the test endpoint reports it correctly.
func TestLDAPGroupMapping_NoMatch_DefaultRole(t *testing.T) {
	stub := &stubLDAPWithCache{
		info: &ldapauth.UserInfo{
			DN:    "CN=bob,OU=users,DC=example,DC=com",
			Email: "bob@example.com",
			// No groups matched any mapping; connector applied DefaultRole "viewer".
			Role: "viewer",
		},
	}
	srv := newLDAPTestServer(t, stub)

	rec := ldapTestRequest(t, srv, "bob", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeLDAPTestResponse(t, rec)
	if resp["mapped_role"] != "viewer" {
		t.Errorf("mapped_role = %v, want viewer", resp["mapped_role"])
	}
}

// TestLDAPGroupMapping_NoMatch_NilDefault_Deny verifies that when no group
// matches and no DefaultRole is set, the connector returns ErrNoGroupMapping
// and the test endpoint responds 403 with denied: true.
func TestLDAPGroupMapping_NoMatch_NilDefault_Deny(t *testing.T) {
	stub := &stubLDAPWithCache{
		err: ldapauth.ErrNoGroupMapping,
	}
	srv := newLDAPTestServer(t, stub)

	rec := ldapTestRequest(t, srv, "charlie", "secret")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	resp := decodeLDAPTestResponse(t, rec)
	if resp["denied"] != true {
		t.Errorf("denied = %v, want true", resp["denied"])
	}
	if resp["authenticated"] != true {
		t.Errorf("authenticated = %v, want true (user credentials were valid)", resp["authenticated"])
	}
}

// TestLDAPGroupCache_Hit verifies that when CheckCache returns a non-nil entry
// (cache hit), the test endpoint reports cache_hit: true.
func TestLDAPGroupCache_Hit(t *testing.T) {
	cached := &ldapauth.UserInfo{
		DN:    "CN=alice,OU=users,DC=example,DC=com",
		Email: "alice@example.com",
		Role:  "admin",
	}
	stub := &stubLDAPWithCache{
		// Authenticate also succeeds (called after cache check in handleLDAPTest).
		info:       cached,
		cachedInfo: cached, // non-nil → CheckCache returns a hit
	}
	srv := newLDAPTestServer(t, stub)

	rec := ldapTestRequest(t, srv, "alice", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeLDAPTestResponse(t, rec)
	if resp["cache_hit"] != true {
		t.Errorf("cache_hit = %v, want true", resp["cache_hit"])
	}
}

// TestLDAPGroupCache_Expiry verifies that when CheckCache returns nil (cache
// miss or entry expired), the test endpoint reports cache_hit: false and falls
// back to a live LDAP call.
func TestLDAPGroupCache_Expiry(t *testing.T) {
	stub := &stubLDAPWithCache{
		info: &ldapauth.UserInfo{
			DN:    "CN=alice,OU=users,DC=example,DC=com",
			Email: "alice@example.com",
			Role:  "viewer",
		},
		cachedInfo: nil, // nil → CheckCache returns a miss (expired or never cached)
	}
	srv := newLDAPTestServer(t, stub)

	rec := ldapTestRequest(t, srv, "alice", "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeLDAPTestResponse(t, rec)
	if resp["cache_hit"] != false {
		t.Errorf("cache_hit = %v, want false", resp["cache_hit"])
	}
}

// TestLDAPTestEndpoint_OK verifies the full happy-path response from
// POST /api/v1/ldap/test: correct status, authenticated flag, user_dn,
// groups, mapped_role, and cache_hit fields.
func TestLDAPTestEndpoint_OK(t *testing.T) {
	stub := &stubLDAPWithCache{
		info: &ldapauth.UserInfo{
			DN:       "CN=dave,OU=users,DC=acme,DC=com",
			Email:    "dave@acme.com",
			Groups:   []string{"ml-engineers"},
			GroupDNs: []string{"CN=ml-engineers,OU=groups,DC=acme,DC=com"},
			Role:     "inference",
		},
	}
	srv := newLDAPTestServer(t, stub)

	rec := ldapTestRequest(t, srv, "dave", "pass")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	resp := decodeLDAPTestResponse(t, rec)

	if resp["authenticated"] != true {
		t.Errorf("authenticated = %v, want true", resp["authenticated"])
	}
	if resp["user_dn"] != "CN=dave,OU=users,DC=acme,DC=com" {
		t.Errorf("user_dn = %v, want CN=dave,OU=users,DC=acme,DC=com", resp["user_dn"])
	}
	if resp["mapped_role"] != "inference" {
		t.Errorf("mapped_role = %v, want inference", resp["mapped_role"])
	}
	if resp["cache_hit"] != false {
		t.Errorf("cache_hit = %v, want false", resp["cache_hit"])
	}
	groups, ok := resp["groups"].([]any)
	if !ok || len(groups) == 0 {
		t.Errorf("groups = %v, want non-empty slice", resp["groups"])
	} else if groups[0] != "CN=ml-engineers,OU=groups,DC=acme,DC=com" {
		t.Errorf("groups[0] = %v, want CN=ml-engineers,OU=groups,DC=acme,DC=com", groups[0])
	}
}

// TestLDAPTestEndpoint_WrongPassword verifies that POST /api/v1/ldap/test
// returns 401 when the LDAP connector rejects the credentials.
func TestLDAPTestEndpoint_WrongPassword(t *testing.T) {
	stub := &stubLDAPWithCache{err: ldapauth.ErrInvalidCredentials}
	srv := newLDAPTestServer(t, stub)

	rec := ldapTestRequest(t, srv, "alice", "wrongpassword")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	resp := decodeLDAPTestResponse(t, rec)
	if resp["authenticated"] != false {
		t.Errorf("authenticated = %v, want false", resp["authenticated"])
	}
}
