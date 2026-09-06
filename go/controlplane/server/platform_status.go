package server

// platform_status.go — GET /api/v1/platform/status and
// GET /api/v1/platform/health endpoints.
//
// /status returns an admin overview of the platform state (org, team, pool,
// and user counts plus enabled feature flags). Requires admin access.
//
// /health is an unauthenticated liveness probe suitable for Kubernetes
// readinessProbe / livenessProbe configurations: it pings the backing SQLite
// store and returns 200 OK when healthy, 503 Service Unavailable otherwise.

import (
	"net/http"
)

// handlePlatformStatus handles GET /api/v1/platform/status.
// Returns a summary of the platform state: organisation count, team count
// (enumerated across all orgs), node pool count, and platform user count,
// plus a feature-flag map for admin dashboards.
func (s *Server) handlePlatformStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	orgs, _ := s.reg.ListOrganizations(ctx)
	pools, _ := s.reg.ListNodePools(ctx)
	users, _ := s.reg.ListPlatformUsers(ctx)

	// Count teams across all orgs.
	teamCount := 0
	for _, org := range orgs {
		teams, _ := s.reg.ListTeamsByOrg(ctx, org.ID)
		teamCount += len(teams)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"platform_version":     "v0.4",
		"organizations":        len(orgs),
		"teams":                teamCount,
		"node_pools":           len(pools),
		"platform_users":       len(users),
		"system_roles_seeded":  true,
		"features": map[string]bool{
			"organizations": true,
			"team_pools":    true,
			"custom_roles":  true,
			"ldap_auth":     false,
			"rbac_v2":       true,
		},
	})
}

// handlePlatformHealth handles GET /api/v1/platform/health.
// This is an unauthenticated liveness probe: it pings the backing store and
// returns 200 {"status":"ok"} when healthy or 503 {"status":"unhealthy"} when
// the store is unreachable. No auth required — Kubernetes probes run without
// credentials.
func (s *Server) handlePlatformHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.reg.Ping(r.Context()); err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unhealthy",
			"error":  err.Error(),
		})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": "v0.4",
	})
}
