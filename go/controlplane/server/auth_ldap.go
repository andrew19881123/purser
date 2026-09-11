// auth_ldap.go — LDAP username/password authentication endpoints.
//
// Endpoints:
//
//	GET  /auth/ldap-login        — serves a simple HTML login form.
//	POST /auth/ldap-login        — authenticates via LDAP, issues a session cookie.
//	POST /api/v1/ldap/test       — admin diagnostic: resolve groups + role for a user.
//
// The login endpoints return 404 when no LDAP connector is configured
// (PURSER_LDAP_URL not set). The session is stored in the same oidc_sessions
// table with auth_method='ldap' so the existing session middleware handles LDAP
// sessions transparently alongside OIDC sessions.
//
// POST /api/v1/ldap/test is a diagnostic endpoint that lets operators verify
// LDAP connectivity and group → role mapping without a user logging in. It
// returns the resolved groups, mapped role, and whether the result was served
// from the in-memory cache.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/purser/purser/go/controlplane/ldapauth"
	"github.com/purser/purser/go/controlplane/registry"
)

// LDAPCacheChecker is an optional extension to LDAPAuthenticator that allows
// inspecting the in-memory group cache without triggering a live LDAP lookup.
// It is satisfied by *ldapauth.Connector.
// The server uses it (via type assertion) in handleLDAPTest to populate the
// cache_hit field of the diagnostic response.
type LDAPCacheChecker interface {
	CheckCache(username, password string) *ldapauth.UserInfo
}

// handleLDAPLogin processes username/password submitted via form POST.
// Creates an OIDC-style session (same table: oidc_sessions with auth_method='ldap')
// so the existing session middleware handles LDAP sessions transparently.
//
// POST /auth/ldap-login
// Content-Type: application/x-www-form-urlencoded
// Body: username=...&password=...
func (s *Server) handleLDAPLogin(w http.ResponseWriter, r *http.Request) {
	if s.ldapConnector == nil {
		s.writeError(w, http.StatusNotFound, "not_configured",
			"LDAP authentication is not configured on this server")
		return
	}

	if err := r.ParseForm(); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "cannot parse form")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "username and password are required")
		return
	}

	info, err := s.ldapConnector.Authenticate(r.Context(), username, password)
	if errors.Is(err, ldapauth.ErrInvalidCredentials) {
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if errors.Is(err, ldapauth.ErrLDAPUnavailable) {
		s.log.Warn("LDAP server unavailable", "err", err)
		s.writeError(w, http.StatusServiceUnavailable, "ldap_unavailable",
			"LDAP server is temporarily unavailable")
		return
	}
	if errors.Is(err, ldapauth.ErrNoGroupMapping) {
		s.writeError(w, http.StatusForbidden, "no_group_mapping",
			"authenticated but no group mapping grants access; contact your administrator")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	// Issue a session token (same as OIDC path).
	sessionToken := s.signSession(info.Email, info.Email)
	tokenHash := sha256HexOf(sessionToken)

	// Persist session in SQLite (auth_method='ldap').
	if s.reg != nil {
		_ = s.reg.CreateOIDCSession(r.Context(), &registry.OIDCSession{
			TokenHash:  tokenHash,
			Sub:        info.DN,
			Email:      info.Email,
			IDPIssuer:  "ldap",
			AuthMethod: "ldap",
			CreatedAt:  time.Now(),
			ExpiresAt:  time.Now().Add(sessionTTL),
		})

		_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
			Actor: "ldap:" + info.Email, Action: "session.ldap_login",
		})
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	// Redirect to home.
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleLDAPLoginForm serves a simple HTML login form for LDAP authentication.
//
// GET /auth/ldap-login
func (s *Server) handleLDAPLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.ldapConnector == nil {
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html>
<html><head><title>Purser — LDAP Login</title>
<style>body{font-family:sans-serif;max-width:400px;margin:100px auto;padding:20px}
input{width:100%;padding:8px;margin:8px 0;box-sizing:border-box}
button{width:100%;padding:10px;background:#4f46e5;color:white;border:none;cursor:pointer;border-radius:4px}
</style></head><body>
<h2>Sign in with LDAP</h2>
<form method="POST" action="/auth/ldap-login">
<input type="text" name="username" placeholder="Username" required autofocus>
<input type="password" name="password" placeholder="Password" required>
<button type="submit">Sign in</button>
</form>
<p><a href="/auth/login">&#8592; Sign in with SSO</a></p>
</body></html>`)
}

// handleLDAPTest is an admin diagnostic endpoint that tests LDAP connectivity
// and group → role resolution for a given username/password pair without
// creating a session. Useful for operators debugging group mappings.
//
// POST /api/v1/ldap/test
// Content-Type: application/json
// Body: {"username":"alice","password":"secret"}
//
// Successful response (200 OK):
//
//	{
//	  "authenticated": true,
//	  "user_dn":       "CN=alice,OU=users,DC=example,DC=com",
//	  "groups":        ["CN=ml-engineers,DC=example,DC=com"],
//	  "mapped_role":   "inference",
//	  "cache_hit":     false
//	}
//
// Error responses:
//   - 404  LDAP not configured
//   - 400  missing username or password
//   - 401  invalid credentials
//   - 403  authenticated but no group mapping matches (and no default_role)
//   - 503  LDAP server unreachable
func (s *Server) handleLDAPTest(w http.ResponseWriter, r *http.Request) {
	if s.ldapConnector == nil {
		s.writeError(w, http.StatusNotFound, "not_configured",
			"LDAP authentication is not configured on this server")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "username and password are required")
		return
	}

	// Check cache before authenticating so we can report cache_hit accurately.
	cacheHit := false
	if cc, ok := s.ldapConnector.(LDAPCacheChecker); ok {
		if cc.CheckCache(req.Username, req.Password) != nil {
			cacheHit = true
		}
	}

	info, err := s.ldapConnector.Authenticate(r.Context(), req.Username, req.Password)
	if errors.Is(err, ldapauth.ErrInvalidCredentials) {
		s.writeJSON(w, http.StatusUnauthorized, map[string]any{
			"authenticated": false,
			"error":         "invalid_credentials",
			"message":       "invalid username or password",
		})
		return
	}
	if errors.Is(err, ldapauth.ErrLDAPUnavailable) {
		s.log.Warn("LDAP test: server unavailable", "err", err)
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"authenticated": false,
			"error":         "ldap_unavailable",
			"message":       "LDAP server is temporarily unavailable",
		})
		return
	}
	if errors.Is(err, ldapauth.ErrNoGroupMapping) {
		s.writeJSON(w, http.StatusForbidden, map[string]any{
			"authenticated": true,
			"denied":        true,
			"mapped_role":   "",
			"cache_hit":     cacheHit,
			"message":       "user authenticated but no group mapping grants access",
		})
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	// Prefer full DNs for the groups field; fall back to CN names.
	groups := info.GroupDNs
	if len(groups) == 0 {
		groups = info.Groups
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user_dn":       info.DN,
		"groups":        groups,
		"mapped_role":   info.Role,
		"cache_hit":     cacheHit,
	})
}
