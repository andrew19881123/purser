// service_accounts.go — REST handlers and JWT issuance for OAuth2
// client_credentials machine authentication.
//
// Endpoints:
//
//	POST /api/v1/service-accounts          — create (admin only)
//	GET  /api/v1/service-accounts          — list   (admin only)
//	DELETE /api/v1/service-accounts/{id}   — revoke (admin only)
//	POST /auth/token                        — OAuth2 client_credentials grant
//
// Token format (non-standard minimal JWT):
//
//	base64url(json_claims) + "." + base64url(hmac-sha256_sig)
//
// The server signs with s.sessionSecret (HMAC-SHA256). Verification is pure
// in-memory — no DB round-trip per authenticated request. TTL is 15 minutes.
package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	mrand "math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// saTokenClaims are the JSON claims embedded in a service account token.
type saTokenClaims struct {
	Sub    string   `json:"sub"`    // service account ID
	Iss    string   `json:"iss"`    // "purser-cp"
	Aud    string   `json:"aud"`    // "purser-api"
	Iat    int64    `json:"iat"`    // issued-at unix timestamp
	Exp    int64    `json:"exp"`    // expiry unix timestamp (iat + 15 min)
	Role   string   `json:"role"`   // "admin" | "viewer" | "inference"
	Tenant string   `json:"tenant"` // tenant scoping (empty = all tenants)
	Scopes []string `json:"scopes,omitempty"`
	Type   string   `json:"type"` // must be "service_account"
}

// handleCreateServiceAccount creates a service account for CI/CD machine auth.
// POST /api/v1/service-accounts
// Admin only. Returns client_id + client_secret (shown once).
func (s *Server) handleCreateServiceAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string   `json:"name"`
		Tenant    string   `json:"tenant"`
		Role      string   `json:"role"` // default "inference"
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"` // RFC3339, optional
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "name required")
		return
	}
	if req.Role == "" {
		req.Role = "inference"
	}

	sa := &registry.ServiceAccount{
		Name:   req.Name,
		Tenant: req.Tenant,
		Role:   req.Role,
		Scopes: req.Scopes,
	}
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "bad_request", "invalid expires_at: must be RFC3339")
			return
		}
		sa.ExpiresAt = &t
	}

	clientSecret, err := s.reg.CreateServiceAccount(r.Context(), sa)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "create_service_account_failed", err.Error())
		return
	}

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorForSARequest(r),
		Action: "service_account.created",
		Target: sa.ID,
	})

	s.writeJSON(w, http.StatusCreated, map[string]any{
		"id":            sa.ID,
		"client_id":     sa.ClientID,
		"client_secret": clientSecret,
		"role":          sa.Role,
		"tenant":        sa.Tenant,
		"message":       "Copy the client_secret now — it is shown only once.",
	})
}

// handleTokenEndpoint implements the OAuth2 client_credentials grant.
// POST /auth/token (no auth required — it IS the auth endpoint)
// Form body: grant_type=client_credentials&client_id=sa_xxx&client_secret=xxx
//
// Returns a 15-minute HMAC-SHA256 signed access token on success.
func (s *Server) handleTokenEndpoint(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	if r.FormValue("grant_type") != "client_credentials" {
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		return
	}
	if len(s.sessionSecret) == 0 {
		http.Error(w, `{"error":"server_error","message":"token signing key not configured; set PURSER_SESSION_SECRET"}`, http.StatusInternalServerError)
		return
	}

	clientID := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")

	sa, err := s.reg.GetServiceAccountByClientID(r.Context(), clientID)
	if err != nil || !sa.Enabled {
		// Constant-time-ish delay to prevent timing oracle on client-ID existence.
		time.Sleep(time.Duration(50+mrand.Intn(50)) * time.Millisecond)
		http.Error(w, `{"error":"invalid_client","message":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	// Verify client_secret using constant-time comparison of SHA-256 hashes.
	sum := sha256.Sum256([]byte(clientSecret))
	providedHash := hex.EncodeToString(sum[:])
	if !hmac.Equal([]byte(providedHash), []byte(sa.ClientSecretHash)) {
		time.Sleep(time.Duration(50+mrand.Intn(50)) * time.Millisecond)
		http.Error(w, `{"error":"invalid_client","message":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	// Issue a short-lived JWT (15 min). Signed with HMAC-SHA256 and sessionSecret.
	now := time.Now()
	claims := saTokenClaims{
		Sub:    sa.ID,
		Iss:    "purser-cp",
		Aud:    "purser-api",
		Iat:    now.Unix(),
		Exp:    now.Add(15 * time.Minute).Unix(),
		Role:   sa.Role,
		Tenant: sa.Tenant,
		Scopes: sa.Scopes,
		Type:   "service_account",
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)

	_ = s.reg.UpdateServiceAccountLastUsed(r.Context(), sa.ID, now)
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  "service_account:" + sa.ID[:min(8, len(sa.ID))],
		Action: "service_account.token_issued",
		Target: sa.ID,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   900,
		"scope":        strings.Join(sa.Scopes, " "),
	})
}

// handleListServiceAccounts returns all service accounts.
// GET /api/v1/service-accounts
func (s *Server) handleListServiceAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.reg.ListServiceAccounts(r.Context(), "")
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_service_accounts_failed", err.Error())
		return
	}
	if accounts == nil {
		accounts = []*registry.ServiceAccount{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"service_accounts": accounts})
}

// handleRevokeServiceAccount revokes (soft-deletes) a service account.
// DELETE /api/v1/service-accounts/{id}
func (s *Server) handleRevokeServiceAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reg.RevokeServiceAccount(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "service account not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "revoke_service_account_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorForSARequest(r),
		Action: "service_account.revoked",
		Target: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// parseServiceAccountToken verifies an SA JWT and returns the decoded claims.
// Returns (nil, false) when the token is absent, malformed, has an invalid
// signature, has an unknown type, or has expired.
//
// Token wire format: base64url(payload_json) + "." + base64url(hmac-sha256_sig)
// (two base64url segments separated by a single dot — not a standard RFC 7519 JWT).
func (s *Server) parseServiceAccountToken(token string) (*saTokenClaims, bool) {
	if len(s.sessionSecret) == 0 || token == "" {
		return nil, false
	}
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return nil, false
	}
	payloadEnc := token[:dot]
	sigEnc := token[dot+1:]

	// A standard JWT has two dots (header.payload.sig). If sigEnc itself
	// contains a dot, this is almost certainly an OIDC JWT — not ours.
	if strings.ContainsRune(sigEnc, '.') {
		return nil, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(payloadEnc)
	if err != nil {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigEnc)
	if err != nil {
		return nil, false
	}

	// Constant-time HMAC verification.
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, false
	}

	var claims saTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	if claims.Type != "service_account" {
		return nil, false
	}
	if claims.Exp < time.Now().Unix() {
		return nil, false // expired
	}
	return &claims, true
}

// actorForSARequest returns an audit actor label derived from the bearer token
// hash, falling back to "api" when no token is present.
func actorForSARequest(r *http.Request) string {
	tok := bearerToken(r)
	if tok == "" {
		return "api"
	}
	sum := sha256.Sum256([]byte(tok))
	return "key:" + hex.EncodeToString(sum[:])[:8]
}
