package server

// logs.go — unified access-log endpoint introduced in v0.5.
//
//   GET /api/v1/logs/access  — list API key access-log entries with optional
//   filtering. Supersedes GET /api/v1/apikeys/{id}/access-log (which now
//   issues a 301 redirect here for backward compatibility).

import (
	"net/http"
	"strconv"

	"github.com/purser/purser/go/controlplane/registry"
)

// handleListAccessLogs serves GET /api/v1/logs/access.
//
// Query parameters:
//   - api_key_id  — filter by key ID (optional; omit to return all entries)
//   - team_id     — reserved; not yet implemented (ignored)
//   - from        — RFC3339 lower bound on request_at (reserved; ignored)
//   - to          — RFC3339 upper bound on request_at (reserved; ignored)
//   - limit       — max entries to return (default 100, max 1000)
func (s *Server) handleListAccessLogs(w http.ResponseWriter, r *http.Request) {
	apiKeyID := r.URL.Query().Get("api_key_id")
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	entries, err := s.reg.ListAPIKeyAccessLog(r.Context(), apiKeyID, limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_access_log_failed", err.Error())
		return
	}
	if entries == nil {
		entries = []*registry.APIKeyAccessEntry{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"count":   len(entries),
	})
}
