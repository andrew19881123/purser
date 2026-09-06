// Package server — v0.4 permission-based RBAC middleware.
//
// rbac_v2.go implements the fine-grained permission enforcement layer that
// replaces the flat 3-role system (admin/viewer/inference) for routes
// registered in routePermission. Existing routes not in the map continue to
// be governed by the legacy role switch in rbacMiddleware (safe degradation),
// ensuring full backward compatibility with pre-v0.4 API keys.
package server

import (
	"net/http"
	"strings"

	"github.com/purser/purser/go/controlplane/permissions"
	"github.com/purser/purser/go/controlplane/registry"
)

// routeKey identifies a route by HTTP method and path pattern.
// Path patterns use Go 1.22 ServeMux syntax (e.g. "/api/v1/nodes/{id}").
type routeKey struct{ method, path string }

// routePermission maps (HTTP method, path pattern) → minimum required
// permission string. Routes not listed here fall through to the legacy role
// switch in rbacMiddleware (safe degradation). Paths use Go 1.22 pattern
// syntax; {param} wildcards match any non-empty path segment.
var routePermission = map[routeKey]string{
	// ── Nodes ─────────────────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/nodes"}:             registry.PermTeamMetricsView,
	{http.MethodPost, "/api/v1/nodes/{id}/drain"}: registry.PermTeamModelsDeploy,
	{http.MethodDelete, "/api/v1/nodes/{id}"}:     registry.PermOrgPoolsRequest,

	// ── Models ────────────────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/models"}:              registry.PermTeamMetricsView,
	{http.MethodPost, "/api/v1/models"}:             registry.PermTeamModelsDeploy,
	{http.MethodDelete, "/api/v1/models/{id}"}:      registry.PermTeamModelsUndeploy,
	{http.MethodPost, "/api/v1/models/{id}/deploy"}: registry.PermTeamModelsDeploy,

	// ── Deployments ───────────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/deployments"}:         registry.PermTeamMetricsView,
	{http.MethodDelete, "/api/v1/deployments/{id}"}: registry.PermTeamModelsUndeploy,

	// ── API Keys ──────────────────────────────────────────────────────────────
	// GET uses team:metrics:view so legacy viewer keys retain list access
	// (backward compat: viewer role carries team:metrics:view but not team:keys:create).
	{http.MethodGet, "/api/v1/apikeys"}:              registry.PermTeamMetricsView,
	{http.MethodPost, "/api/v1/apikeys"}:             registry.PermTeamKeysCreate,
	{http.MethodDelete, "/api/v1/apikeys/{id}"}:      registry.PermTeamKeysRevoke,
	{http.MethodPost, "/api/v1/apikeys/{id}/rotate"}: registry.PermTeamKeysCreate,

	// ── Audit ─────────────────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/enterprise/audit-log"}: registry.PermTeamMetricsView,
	{http.MethodGet, "/api/v1/inference-audit"}:      registry.PermTeamMetricsView,

	// ── Approvals ─────────────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/approvals"}:                         registry.PermTeamApprovalsView,
	{http.MethodPost, "/api/v1/approvals/{deploymentId}/approve"}: registry.PermTeamApprovalsReview,
	{http.MethodPost, "/api/v1/approvals/{deploymentId}/reject"}:  registry.PermTeamApprovalsReview,

	// ── Policies ──────────────────────────────────────────────────────────────
	{http.MethodGet, "/api/v1/policies"}:           registry.PermTeamMetricsView,
	{http.MethodPut, "/api/v1/policies/{name}"}:    registry.PermOrgRolesCreate,
	{http.MethodDelete, "/api/v1/policies/{name}"}: registry.PermOrgRolesDelete,

	// ── Config ────────────────────────────────────────────────────────────────
	{http.MethodPost, "/api/v1/config/apply"}: registry.PermTeamModelsDeploy,
	{http.MethodPost, "/api/v1/config/diff"}:  registry.PermTeamMetricsView,

	// ── Platform endpoints — platform_admin level ─────────────────────────────
	{http.MethodPost, "/api/v1/platform/orgs"}:        registry.PermPlatformOrgsCreate,
	{http.MethodDelete, "/api/v1/platform/orgs/{id}"}: registry.PermPlatformOrgsDelete,
	{http.MethodPost, "/api/v1/platform/pools"}:       registry.PermPlatformPoolsManage,
}

// matchRoutePermission returns the required permission string for the given
// (method, urlPath) pair. It first tries an exact key lookup; if that misses
// it scans for the longest matching Go 1.22 ServeMux pattern. Returns "" when
// no route is mapped (caller falls through to legacy RBAC).
func matchRoutePermission(method, urlPath string) string {
	// 1. Fast-path: exact match (covers parameterless routes like GET /api/v1/models).
	if perm, ok := routePermission[routeKey{method, urlPath}]; ok {
		return perm
	}
	// 2. Pattern match: find the most-specific (longest) matching pattern.
	bestLen := 0
	bestPerm := ""
	for rk, perm := range routePermission {
		if rk.method != method {
			continue
		}
		if matchPattern(rk.path, urlPath) {
			if len(rk.path) > bestLen {
				bestLen = len(rk.path)
				bestPerm = perm
			}
		}
	}
	return bestPerm
}

// matchPattern reports whether urlPath matches the Go 1.22 ServeMux path
// pattern. Segments enclosed in {braces} match any non-empty path segment;
// all other segments must match literally. Segment count must be identical.
func matchPattern(pattern, urlPath string) bool {
	patParts := strings.Split(strings.Trim(pattern, "/"), "/")
	urlParts := strings.Split(strings.Trim(urlPath, "/"), "/")
	if len(patParts) != len(urlParts) {
		return false
	}
	for i, pat := range patParts {
		if strings.HasPrefix(pat, "{") && strings.HasSuffix(pat, "}") {
			continue // wildcard — matches any non-empty segment
		}
		if pat != urlParts[i] {
			return false
		}
	}
	return true
}

// resolvePermissions returns the effective permissions.Context for the request.
// Priority chain (first match wins):
//
//  1. No API key in context (OIDC session) → legacy role from OIDC claims.
//  2. API key with Tenant set → resolve via team_members + custom_roles.
//  3. API key with legacy role → FromLegacyRole() for backward compat.
func (s *Server) resolvePermissions(r *http.Request) permissions.Context {
	key := apiKeyFromContext(r.Context())
	if key == nil {
		// OIDC / unauthenticated path — use the role injected by oidcMiddleware.
		oidcRole, _ := r.Context().Value(ctxKeyOIDCRole).(string)
		if oidcRole == "" {
			oidcRole = "viewer"
		}
		return permissions.FromLegacyRole("oidc", oidcRole)
	}

	// Team context: resolve effective permissions from the roles engine. Uses
	// key.ID as the user identifier so GetEffectivePermissions can join against
	// team_members.user_sub. Tenant is the team ID the key is scoped to.
	if key.Tenant != "" && s.reg != nil {
		ep, err := s.reg.GetEffectivePermissions(r.Context(), key.ID, key.Tenant)
		if err == nil && len(ep.Permissions) > 0 {
			return permissions.Context{
				UserID:          key.ID,
				TeamID:          key.Tenant,
				OrgID:           ep.OrgID,
				Permissions:     ep.Permissions,
				IsOrgAdmin:      ep.IsOrgAdmin,
				IsPlatformAdmin: key.Role == "admin",
			}
		}
		// If GetEffectivePermissions returns empty (e.g. no team-member record),
		// fall through to the legacy role mapping below.
	}

	// Fallback: legacy role-based permissions for backward compat with pre-v0.4
	// API keys that have no team membership records yet.
	return permissions.FromLegacyRole(key.ID, key.Role)
}

// checkPermission reports whether the request carries sufficient permissions
// for the given required permission string. Platform admins bypass all checks.
func (s *Server) checkPermission(r *http.Request, required string) bool {
	ctx := s.resolvePermissions(r)
	if ctx.IsPlatformAdmin {
		return true
	}
	return permissions.Has(ctx.Permissions, required)
}
