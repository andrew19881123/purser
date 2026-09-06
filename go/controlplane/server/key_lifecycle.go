package server

// key_lifecycle.go — REST handlers for the API key lifecycle endpoints:
//
//	POST /api/v1/apikeys/{id}/rotate    — atomically replace a key
//	GET  /api/v1/apikeys/{id}/access-log — per-key request audit trail

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/purser/purser/go/controlplane/registry"
)

// generateAPIKey creates a fresh API key pair. It returns the plaintext token
// (shown to the caller exactly once) and the SHA-256 hex hash (persisted in
// the database — the plaintext is never stored).
func generateAPIKey() (plaintext, keyHash string) {
	secret := make([]byte, 24)
	_, _ = rand.Read(secret)
	plaintext = "psk_" + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(plaintext))
	keyHash = hex.EncodeToString(sum[:])
	return
}

// handleRotateAPIKey atomically creates a replacement key and disables the old
// one. The new key inherits role, tenant, quota, and expiry from the old key.
// The plaintext of the new key is returned exactly once in the response body.
//
// POST /api/v1/apikeys/{id}/rotate
func (s *Server) handleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	oldID := r.PathValue("id")

	old, err := s.reg.GetAPIKey(r.Context(), oldID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_apikey_failed", err.Error())
		return
	}
	if !old.Enabled {
		s.writeError(w, http.StatusConflict, "key_already_revoked",
			"cannot rotate a disabled key")
		return
	}

	newID := "key-" + randHex(8)
	plaintext, keyHash := generateAPIKey()

	newKey := &registry.APIKey{
		ID:            newID,
		Name:          old.Name + " (rotated)",
		KeyHash:       keyHash,
		Tenant:        old.Tenant,
		Role:          old.Role,
		Quota:         old.Quota,
		Enabled:       true,
		Scopes:        old.Scopes,
		ExpiresAt:     old.ExpiresAt, // inherit expiry
		PredecessorID: oldID,
	}

	if err := s.reg.RotateAPIKey(r.Context(), oldID, newKey); err != nil {
		s.writeError(w, http.StatusInternalServerError, "rotate_failed", err.Error())
		return
	}

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:   "api",
		Action:  "apikey.rotated",
		Target:  oldID,
		Details: json.RawMessage(`{"new_id":"` + newID + `"}`),
	})

	s.writeJSON(w, http.StatusCreated, map[string]any{
		"old_id":  oldID,
		"new_id":  newID,
		"key":     plaintext,
		"role":    newKey.Role,
		"tenant":  newKey.Tenant,
		"message": "Copy the key now — it is shown only once.",
	})
}

// handleListAPIKeyAccess returns the per-request access log for an API key.
// Falls back to an empty list when the backend does not support access-log
// reads (i.e. is not a *registry.SQLiteRegistry).
//
// GET /api/v1/apikeys/{id}/access-log?limit=50
func (s *Server) handleListAPIKeyAccess(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	limit := 50
	if qs := r.URL.Query().Get("limit"); qs != "" {
		if n, err := strconv.Atoi(qs); err == nil && n > 0 {
			limit = n
		}
	}

	// Verify the key exists so callers get 404 instead of an empty list for
	// a key that was never created.
	if _, err := s.reg.GetAPIKey(r.Context(), id); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}

	entries := []*registry.APIKeyAccessEntry{}
	// ListAPIKeyAccessLog is an optional extension on *SQLiteRegistry (not on
	// the Registry interface) — type-assert to call it only when available.
	if q, ok := s.reg.(*registry.SQLiteRegistry); ok {
		got, err := q.ListAPIKeyAccessLog(r.Context(), id, limit)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "list_access_log_failed", err.Error())
			return
		}
		if got != nil {
			entries = got
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
