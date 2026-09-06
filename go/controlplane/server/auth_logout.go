// auth_logout.go — session revocation endpoints.
//
//	GET  /auth/logout             — revoke current session, clear cookie, redirect to /.
//	POST /auth/backchannel-logout — IdP-initiated backchannel logout (RFC 9470).
//
// Both endpoints are exempt from oidcMiddleware so they are reachable with an
// expired or already-revoked session token.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/purser/purser/go/controlplane/registry"
)

// handleAuthLogout revokes the current session and clears the session cookie.
//
// GET /auth/logout
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if s.reg != nil {
			tokenHash := sha256HexOf(cookie.Value)
			if rErr := s.reg.RevokeOIDCSessionByTokenHash(r.Context(), tokenHash); rErr != nil {
				s.log.Debug("RevokeOIDCSessionByTokenHash failed", "err", rErr)
			}
			_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
				Actor:  "session",
				Action: "session.logout",
			})
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleBackchannelLogout handles IdP-initiated backchannel logout requests
// (RFC 9470 / OpenID Connect Back-Channel Logout 1.0).
//
// The IdP POST-s a signed logout_token JWT; we verify it via the existing
// oidcVerifier (same issuer), revoke all sessions for the subject, and respond
// 200 OK as required by the spec.
//
// POST /auth/backchannel-logout
func (s *Server) handleBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rawToken := r.FormValue("logout_token")
	if rawToken == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	sub, err := s.verifyLogoutToken(r.Context(), rawToken)
	if err != nil {
		s.log.Warn("backchannel_logout: invalid token", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if s.reg != nil {
		n, revErr := s.reg.RevokeOIDCSessionsBySubject(r.Context(), sub)
		if revErr != nil {
			s.log.Warn("backchannel_logout: RevokeOIDCSessionsBySubject failed",
				"sub", sub, "err", revErr)
		}
		_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
			Actor:   "idp",
			Action:  "session.backchannel_logout",
			Target:  sub,
			Details: json.RawMessage(fmt.Sprintf(`{"sessions_revoked":%d}`, n)),
		})
	}
	w.WriteHeader(http.StatusOK)
}

// verifyLogoutToken verifies the logout_token JWT issued by the IdP. The
// logout_token carries the same issuer and is signed with the same key as an
// id_token, so the existing oidcVerifier suffices. The sub claim is returned.
func (s *Server) verifyLogoutToken(ctx context.Context, rawToken string) (string, error) {
	if s.oidcVerifier == nil {
		return "", errors.New("verifyLogoutToken: OIDC not configured")
	}
	sub, _, err := s.oidcVerifier.VerifyToken(ctx, rawToken)
	return sub, err
}
