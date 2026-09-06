// auth.go — Authorization Code Flow + PKCE browser SSO for the admin dashboard.
//
// Two HTTP endpoints are added when OIDC is configured with a RedirectURI:
//
//	GET /auth/login    — generates PKCE state + challenge, redirects to IdP.
//	GET /auth/callback — receives auth code, exchanges for tokens, sets session
//	                     cookie, redirects to /.
//
// Session cookies are signed with HMAC-SHA256 using a key from
// PURSER_SESSION_SECRET (or an ephemeral key auto-generated at startup).
// The oidcMiddleware in server.go accepts the session cookie as an alternative
// to a Bearer ID token, enabling full browser SSO without client-side PKCE.
package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---- Constants ----

const (
	// sessionCookieName is the name of the browser session cookie.
	sessionCookieName = "purser_session"
	// sessionTTL is the lifetime of a session cookie.
	sessionTTL = 8 * time.Hour
	// pkceTTL is the lifetime of a pending PKCE state entry. Authorization
	// servers typically impose a shorter bound (60–300 s); the entry is
	// single-use and consumed on callback.
	pkceTTL = 5 * time.Minute
)

// pkceMaxEntries caps the number of in-flight PKCE states the store will hold.
// Login attempts that arrive when the store is at capacity (after expired
// entries are evicted) are silently dropped; the OAuth callback will fail and
// the user will need to retry. The limit is deliberately conservative: a
// 1000-entry burst is well above any legitimate operator workload.
const pkceMaxEntries = 1000

// pkceEntry holds the PKCE code_verifier for an in-flight authorization
// request, keyed by the OAuth2 state parameter.
type pkceEntry struct {
	verifier string
	exp      time.Time
}

// pkceStateStore is a bounded, thread-safe map of PKCE state → code-verifier
// entries. The map size is capped at pkceMaxEntries; when full, expired entries
// are evicted on each set call before a new entry is accepted. If the map
// remains full after eviction the new entry is silently dropped — the caller
// will receive an error at the OAuth callback and can restart the flow.
type pkceStateStore struct {
	mu      sync.Mutex
	entries map[string]pkceEntry
}

// newPKCEStateStore returns a ready-to-use pkceStateStore.
func newPKCEStateStore() *pkceStateStore {
	return &pkceStateStore{entries: make(map[string]pkceEntry)}
}

// set stores (state, verifier) with a TTL of pkceTTL. When the store is at
// pkceMaxEntries capacity, expired entries are evicted first; if still full
// after eviction the entry is silently dropped.
func (s *pkceStateStore) set(state, verifier string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= pkceMaxEntries {
		now := time.Now()
		for k, v := range s.entries {
			if now.After(v.exp) {
				delete(s.entries, k)
			}
		}
		// If still full after sweeping expired entries, refuse the new entry.
		if len(s.entries) >= pkceMaxEntries {
			return // silently drop — the login attempt will fail at callback
		}
	}
	s.entries[state] = pkceEntry{verifier: verifier, exp: time.Now().Add(pkceTTL)}
}

// get retrieves and removes the verifier for state. Returns ("", false) when
// the state is unknown or has expired. Single-use: the entry is consumed on
// first successful get.
func (s *pkceStateStore) get(state string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[state]
	if !ok {
		return "", false
	}
	delete(s.entries, state) // consume once
	if time.Now().After(e.exp) {
		return "", false
	}
	return e.verifier, true
}

// lenUnsafe returns the current number of entries (expired or not).
// Intended for tests; not safe to call from concurrent production code without
// holding the lock.
func (s *pkceStateStore) lenUnsafe() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// ---- PKCE helpers ----

// generatePKCE generates a 32-byte random code_verifier (base64url, no
// padding) and its S256 code_challenge = base64url(SHA256(verifier)).
func generatePKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("pkce: generate verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// ---- Session token helpers ----

// signSession creates a session token signed with s.sessionSecret.
//
// Format: base64url(JSON payload) + "." + base64url(HMAC-SHA256 signature)
// Payload: {"sub":"…","email":"…","exp":<unix>}
func (s *Server) signSession(sub, email string) string {
	exp := time.Now().Add(sessionTTL).Unix()
	payload, _ := json.Marshal(map[string]any{
		"sub":   sub,
		"email": email,
		"exp":   exp,
	})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig
}

// verifySession verifies a token produced by signSession and returns the sub
// and email claims. Returns an error when the signature is invalid or the
// token has expired.
func (s *Server) verifySession(token string) (sub, email string, err error) {
	dot := strings.LastIndex(token, ".")
	if dot < 0 {
		return "", "", errors.New("session: invalid format")
	}
	encoded, sig := token[:dot], token[dot+1:]

	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write([]byte(encoded))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", "", errors.New("session: signature invalid")
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", fmt.Errorf("session: decode payload: %w", err)
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", fmt.Errorf("session: unmarshal: %w", err)
	}
	if time.Now().Unix() > claims.Exp {
		return "", "", errors.New("session: token expired")
	}
	return claims.Sub, claims.Email, nil
}

// ---- Authorization Code Flow + PKCE handlers ----

// handleAuthLogin redirects the browser to the IdP's authorization endpoint.
//
// It generates:
//   - a 32-byte random state parameter (hex-encoded) stored in the PKCE store;
//   - a PKCE code_verifier (32 random bytes, base64url-encoded);
//   - a code_challenge = base64url(SHA256(verifier)) (S256 method).
//
// The state → verifier mapping is stored for up to pkceTTL.
//
// GET /auth/login → 302 to {issuer}/oauth2/v2.0/authorize?…
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidcConfig == nil || s.oidcConfig.RedirectURI == "" || s.oidcConfig.TokenEndpoint == "" {
		s.writeError(w, http.StatusServiceUnavailable, "oidc_not_configured",
			"OIDC Authorization Code Flow requires PURSER_OIDC_REDIRECT_URI")
		return
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "pkce_gen_failed", err.Error())
		return
	}

	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		s.writeError(w, http.StatusInternalServerError, "state_gen_failed", err.Error())
		return
	}
	state := hex.EncodeToString(stateBytes)

	s.pkceStore.set(state, verifier)

	authURL := s.oidcConfig.Issuer + "/oauth2/v2.0/authorize"
	q := url.Values{}
	q.Set("client_id", s.oidcConfig.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", s.oidcConfig.RedirectURI)
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")

	http.Redirect(w, r, authURL+"?"+q.Encode(), http.StatusFound)
}

// handleAuthCallback receives the authorization code from the IdP, exchanges
// it for tokens, validates the ID token, and sets a session cookie.
//
// Flow:
//  1. Validate state — must match an entry in the PKCE store (TTL pkceTTL).
//  2. Exchange auth code at TokenEndpoint (RFC 7636 code_verifier attached).
//  3. Verify returned id_token via s.oidcVerifier.
//  4. Set HttpOnly session cookie (HMAC-SHA256 signed, 8h TTL).
//  5. Redirect to /.
//
// GET /auth/callback?code=…&state=…
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidcConfig == nil || s.oidcConfig.RedirectURI == "" || s.oidcConfig.TokenEndpoint == "" {
		s.writeError(w, http.StatusServiceUnavailable, "oidc_not_configured",
			"OIDC Authorization Code Flow is not configured")
		return
	}

	q := r.URL.Query()

	// 1. Validate state.
	state := q.Get("state")
	if state == "" {
		s.writeError(w, http.StatusBadRequest, "missing_state", "state parameter required")
		return
	}
	codeVerifier, ok := s.pkceStore.get(state)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid_state",
			"state is unknown or expired; restart the login flow")
		return
	}

	// 2. Check for an error response from the IdP.
	if errCode := q.Get("error"); errCode != "" {
		desc := q.Get("error_description")
		if desc == "" {
			desc = errCode
		}
		s.writeError(w, http.StatusBadRequest, "idp_error", desc)
		return
	}

	// 3. Extract authorization code.
	code := q.Get("code")
	if code == "" {
		s.writeError(w, http.StatusBadRequest, "missing_code", "code parameter required")
		return
	}

	// 4. Exchange code for tokens at the IdP token endpoint.
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {s.oidcConfig.RedirectURI},
		"client_id":     {s.oidcConfig.ClientID},
		"code_verifier": {codeVerifier},
	}
	if s.oidcConfig.ClientSecret != "" {
		form.Set("client_secret", s.oidcConfig.ClientSecret)
	}

	resp, err := http.PostForm(s.oidcConfig.TokenEndpoint, form)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "token_exchange_failed",
			"token endpoint unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	var tokenResp struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		ErrorCode   string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		s.writeError(w, http.StatusBadGateway, "token_decode_failed",
			"failed to decode token response: "+err.Error())
		return
	}
	if tokenResp.ErrorCode != "" {
		desc := tokenResp.ErrorDesc
		if desc == "" {
			desc = tokenResp.ErrorCode
		}
		s.log.Debug("OIDC token exchange error",
			"error", tokenResp.ErrorCode, "desc", desc)
		s.writeError(w, http.StatusUnauthorized, "token_error", desc)
		return
	}
	if tokenResp.IDToken == "" {
		s.writeError(w, http.StatusBadGateway, "no_id_token",
			"IdP did not return an id_token")
		return
	}

	// 5. Verify the returned ID token.
	sub, email, err := s.oidcVerifier.VerifyToken(r.Context(), tokenResp.IDToken)
	if err != nil {
		s.log.Debug("OIDC callback: ID token verification failed", "err", err)
		s.writeError(w, http.StatusUnauthorized, "invalid_id_token",
			"ID token verification failed")
		return
	}

	// 6. Mint and set the session cookie.
	sessionToken := s.signSession(sub, email)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	s.log.Info("OIDC login complete", "sub", sub, "email", email)

	// 7. Redirect to the dashboard.
	http.Redirect(w, r, "/", http.StatusFound)
}
