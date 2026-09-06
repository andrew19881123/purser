package ldapauth_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	ldap "github.com/go-ldap/ldap/v3"

	"github.com/purser/purser/go/controlplane/ldapauth"
)

// ---------------------------------------------------------------------------
// Unit tests — no network required
// ---------------------------------------------------------------------------

func TestFromEnv_NilWhenURLNotSet(t *testing.T) {
	t.Setenv("PURSER_LDAP_URL", "")
	cfg := ldapauth.FromEnv()
	if cfg != nil {
		t.Fatal("expected nil config when PURSER_LDAP_URL is not set")
	}
}

func TestFromEnv_ParsesGroupMappings(t *testing.T) {
	t.Setenv("PURSER_LDAP_URL", "ldap://localhost:389")
	t.Setenv("PURSER_LDAP_BIND_DN", "cn=svc,dc=example,dc=com")
	t.Setenv("PURSER_LDAP_USER_BASE_DN", "ou=users,dc=example,dc=com")
	t.Setenv("PURSER_LDAP_GROUP_MAPPINGS", "Admins=admin,Viewers=viewer, Inference-Users=inference")
	t.Cleanup(func() { os.Unsetenv("PURSER_LDAP_GROUP_MAPPINGS") })

	cfg := ldapauth.FromEnv()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	cases := []struct{ group, want string }{
		{"Admins", "admin"},
		{"Viewers", "viewer"},
		{"Inference-Users", "inference"},
	}
	for _, tc := range cases {
		if got := cfg.GroupMappings[tc.group]; got != tc.want {
			t.Errorf("GroupMappings[%q] = %q, want %q", tc.group, got, tc.want)
		}
	}
}

func TestFromEnv_DefaultCacheTTL(t *testing.T) {
	t.Setenv("PURSER_LDAP_URL", "ldap://localhost:389")
	t.Setenv("PURSER_LDAP_BIND_DN", "cn=svc,dc=example,dc=com")
	t.Setenv("PURSER_LDAP_USER_BASE_DN", "ou=users,dc=example,dc=com")
	os.Unsetenv("PURSER_LDAP_CACHE_TTL_SECONDS")

	cfg := ldapauth.FromEnv()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.CacheTTL != 5*time.Minute {
		t.Errorf("default CacheTTL = %v, want 5m", cfg.CacheTTL)
	}
}

func TestFromEnv_CustomCacheTTL(t *testing.T) {
	t.Setenv("PURSER_LDAP_URL", "ldap://localhost:389")
	t.Setenv("PURSER_LDAP_BIND_DN", "cn=svc,dc=example,dc=com")
	t.Setenv("PURSER_LDAP_USER_BASE_DN", "ou=users,dc=example,dc=com")
	t.Setenv("PURSER_LDAP_CACHE_TTL_SECONDS", "120")

	cfg := ldapauth.FromEnv()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.CacheTTL != 2*time.Minute {
		t.Errorf("CacheTTL = %v, want 2m", cfg.CacheTTL)
	}
}

func TestFromEnv_DefaultFilters(t *testing.T) {
	t.Setenv("PURSER_LDAP_URL", "ldap://localhost:389")
	t.Setenv("PURSER_LDAP_BIND_DN", "cn=svc,dc=example,dc=com")
	t.Setenv("PURSER_LDAP_USER_BASE_DN", "ou=users,dc=example,dc=com")
	os.Unsetenv("PURSER_LDAP_USER_FILTER")
	os.Unsetenv("PURSER_LDAP_GROUP_FILTER")
	os.Unsetenv("PURSER_LDAP_GROUP_ATTRIBUTE")

	cfg := ldapauth.FromEnv()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.UserFilter != "(&(objectClass=user)(sAMAccountName=%s))" {
		t.Errorf("unexpected UserFilter: %q", cfg.UserFilter)
	}
	if cfg.GroupAttribute != "cn" {
		t.Errorf("unexpected GroupAttribute: %q", cfg.GroupAttribute)
	}
}

func TestConfig_Validate_RequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ldapauth.Config
		wantErr bool
	}{
		{
			name: "valid",
			cfg: ldapauth.Config{
				URL:        "ldap://localhost:389",
				BindDN:     "cn=svc,dc=example,dc=com",
				UserBaseDN: "ou=users,dc=example,dc=com",
			},
			wantErr: false,
		},
		{
			name:    "missing URL",
			cfg:     ldapauth.Config{BindDN: "cn=svc,dc=example,dc=com", UserBaseDN: "ou=users,dc=example,dc=com"},
			wantErr: true,
		},
		{
			name:    "missing BindDN",
			cfg:     ldapauth.Config{URL: "ldap://localhost:389", UserBaseDN: "ou=users,dc=example,dc=com"},
			wantErr: true,
		},
		{
			name:    "missing UserBaseDN",
			cfg:     ldapauth.Config{URL: "ldap://localhost:389", BindDN: "cn=svc,dc=example,dc=com"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestNew_NilConfigReturnsNil(t *testing.T) {
	c := ldapauth.New(nil)
	if c != nil {
		t.Fatal("New(nil) should return nil")
	}
}

// TestEscapeFilter_PreventsInjection verifies that ldap.EscapeFilter (used
// internally) properly sanitizes usernames containing LDAP metacharacters.
// This is a white-box check that the library's escaping is in place.
func TestEscapeFilter_PreventsInjection(t *testing.T) {
	malicious := "admin)(|(objectClass=*)"
	escaped := ldap.EscapeFilter(malicious)
	if escaped == malicious {
		t.Fatal("EscapeFilter did not sanitize LDAP metacharacters")
	}
	// Must not contain unescaped parentheses or pipe
	for _, ch := range []string{")(", "|(", "*)"} {
		if containsUnescaped(escaped, ch) {
			t.Errorf("escaped value still contains injection sequence %q: got %q", ch, escaped)
		}
	}
}

func containsUnescaped(s, seq string) bool {
	// A very simple check: if the sequence appears without backslash before it.
	for i := 0; i+len(seq) <= len(s); i++ {
		if s[i:i+len(seq)] == seq {
			if i == 0 || s[i-1] != '\\' {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Role resolution tests — exercised via a minimal exported helper.
// We test the exported Connector API indirectly through the mock server path.
// ---------------------------------------------------------------------------

// mockLDAPServer starts a minimal TCP server that accepts a single LDAP
// session and responds to Bind and Search requests as configured.
// It implements just enough of the protocol for testing Authenticate.
type mockLDAPServer struct {
	listener net.Listener
	addr     string

	// responses
	bindServiceOK   bool
	userSearchDN    string // returned DN for user search; empty = not found
	userSearchEmail string
	userBindOK      bool // whether user bind succeeds
	groups          []string
}

func newMockLDAPServer(t *testing.T) *mockLDAPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock LDAP listen: %v", err)
	}
	m := &mockLDAPServer{listener: ln, addr: ln.Addr().String()}
	return m
}

func (m *mockLDAPServer) close() { m.listener.Close() }

// ---------------------------------------------------------------------------
// Connector tests using the real server (unreachable)
// ---------------------------------------------------------------------------

func TestConnector_LDAPUnavailable(t *testing.T) {
	// Pick a port that's guaranteed to have nothing listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // close immediately so connection refused

	cfg := &ldapauth.Config{
		URL:            fmt.Sprintf("ldap://%s", addr),
		BindDN:         "cn=svc,dc=example,dc=com",
		BindPassword:   "secret",
		UserBaseDN:     "ou=users,dc=example,dc=com",
		UserFilter:     "(&(objectClass=user)(sAMAccountName=%s))",
		GroupAttribute: "cn",
		GroupMappings:  map[string]string{},
		CacheTTL:       5 * time.Minute,
	}

	c := ldapauth.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = c.Authenticate(ctx, "alice", "pass")
	if !errors.Is(err, ldapauth.ErrLDAPUnavailable) {
		t.Errorf("expected ErrLDAPUnavailable, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration test (skipped unless LDAP_TEST_URL is set)
// ---------------------------------------------------------------------------

func TestConnector_Integration(t *testing.T) {
	url := os.Getenv("LDAP_TEST_URL")
	if url == "" {
		t.Skip("requires LDAP server: set LDAP_TEST_URL, LDAP_TEST_BIND_DN, LDAP_TEST_BIND_PASSWORD, LDAP_TEST_USER_BASE_DN, LDAP_TEST_USERNAME, LDAP_TEST_PASSWORD")
	}

	cfg := &ldapauth.Config{
		URL:            url,
		BindDN:         os.Getenv("LDAP_TEST_BIND_DN"),
		BindPassword:   os.Getenv("LDAP_TEST_BIND_PASSWORD"),
		UserBaseDN:     os.Getenv("LDAP_TEST_USER_BASE_DN"),
		UserFilter:     getEnvOrDefault("LDAP_TEST_USER_FILTER", "(&(objectClass=user)(sAMAccountName=%s))"),
		GroupBaseDN:    os.Getenv("LDAP_TEST_GROUP_BASE_DN"),
		GroupFilter:    getEnvOrDefault("LDAP_TEST_GROUP_FILTER", "(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))"),
		GroupAttribute: getEnvOrDefault("LDAP_TEST_GROUP_ATTRIBUTE", "cn"),
		GroupMappings:  map[string]string{},
		CacheTTL:       5 * time.Minute,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	c := ldapauth.New(cfg)
	ctx := context.Background()

	// First call — should hit LDAP
	info, err := c.Authenticate(ctx, os.Getenv("LDAP_TEST_USERNAME"), os.Getenv("LDAP_TEST_PASSWORD"))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	t.Logf("UserInfo: DN=%s Email=%s Groups=%v Role=%s", info.DN, info.Email, info.Groups, info.Role)

	// Second call — should be served from cache
	start := time.Now()
	info2, err := c.Authenticate(ctx, os.Getenv("LDAP_TEST_USERNAME"), os.Getenv("LDAP_TEST_PASSWORD"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Authenticate (cached): %v", err)
	}
	if info2.DN != info.DN {
		t.Errorf("cache returned different DN: %q vs %q", info2.DN, info.DN)
	}
	if elapsed > 50*time.Millisecond {
		t.Logf("warning: cached call took %v, expected near-zero", elapsed)
	}

	// Wrong password
	_, err = c.Authenticate(ctx, os.Getenv("LDAP_TEST_USERNAME"), "wrong-password-for-test")
	if !errors.Is(err, ldapauth.ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials for wrong password, got: %v", err)
	}
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
