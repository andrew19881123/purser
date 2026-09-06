// auth_ldap.go — LDAP username/password authentication endpoints.
//
// Two HTTP endpoints:
//
//	GET  /auth/ldap-login — serves a simple HTML login form.
//	POST /auth/ldap-login — authenticates via LDAP, issues a session cookie.
//
// Both endpoints return 404 when no LDAP connector is configured (PURSER_LDAP_URL
// not set). The session is stored in the same oidc_sessions table with
// auth_method='ldap' so the existing session middleware handles LDAP sessions
// transparently alongside OIDC sessions.
package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/purser/purser/go/controlplane/ldapauth"
	"github.com/purser/purser/go/controlplane/registry"
)

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
