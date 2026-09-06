// Package permissions implements the permission resolution and checking engine
// for the Purser platform. It is intentionally standalone — it depends only on
// primitive Go types and carries no database or HTTP dependencies.
//
// # Permission string format
//
// Permissions are colon-separated hierarchical strings, e.g.:
//
//	"team:models:deploy"
//	"org:members:invite"
//	"platform:orgs:create"
//	"inference:call"
//
// Wildcards are supported with the ":*" suffix:
//
//	"team:*"     matches any permission starting with "team:"
//	"org:*"      matches any permission starting with "org:"
//	"platform:*" matches any permission starting with "platform:"
package permissions

import (
	"sort"
	"strings"
)

// Has checks if a permission set contains the given required permission.
// Supports wildcard suffixes: "team:*" matches "team:models:deploy".
func Has(effective []string, required string) bool {
	for _, p := range effective {
		if p == required {
			return true
		}
		// Wildcard: "team:*" matches "team:models:deploy"
		if strings.HasSuffix(p, ":*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(required, prefix) {
				return true
			}
		}
		// "inference:*" matches "inference:call"
		if p == "inference:*" && strings.HasPrefix(required, "inference:") {
			return true
		}
	}
	return false
}

// HasAll checks if ALL required permissions are present in the effective set.
// Returns false immediately on the first missing permission.
func HasAll(effective []string, required ...string) bool {
	for _, r := range required {
		if !Has(effective, r) {
			return false
		}
	}
	return true
}

// HasAny checks if AT LEAST ONE required permission is present in the effective set.
// Returns true on the first match found.
func HasAny(effective []string, required ...string) bool {
	for _, r := range required {
		if Has(effective, r) {
			return true
		}
	}
	return false
}

// SystemRoles returns the built-in role definitions.
// These are seeded into the DB by SeedSystemRoles() but also available
// in-memory for permission checks before the DB is available.
func SystemRoles() []BuiltinRole {
	return []BuiltinRole{
		{
			ID:   "platform_admin",
			Name: "Platform Administrator",
			Permissions: []string{
				"platform:orgs:create", "platform:orgs:delete",
				"platform:pools:manage", "platform:users:invite",
				"org:teams:create", "org:teams:delete",
				"org:members:invite", "org:members:remove",
				"org:roles:create", "org:roles:delete", "org:pools:request",
				"team:models:deploy", "team:models:undeploy",
				"team:keys:create", "team:keys:revoke",
				"team:members:view", "team:members:invite", "team:members:remove",
				"team:metrics:view", "team:approvals:view", "team:approvals:review",
				"inference:call",
			},
		},
		{
			ID:   "org_admin",
			Name: "Organization Administrator",
			Permissions: []string{
				"org:teams:create", "org:teams:delete",
				"org:members:invite", "org:members:remove",
				"org:roles:create", "org:roles:delete", "org:pools:request",
				"team:models:deploy", "team:models:undeploy",
				"team:keys:create", "team:keys:revoke",
				"team:members:view", "team:members:invite", "team:members:remove",
				"team:metrics:view", "team:approvals:view", "team:approvals:review",
				"inference:call",
			},
		},
		{
			ID:   "team_admin",
			Name: "Team Administrator",
			Permissions: []string{
				"team:models:deploy", "team:models:undeploy",
				"team:keys:create", "team:keys:revoke",
				"team:members:view", "team:members:invite", "team:members:remove",
				"team:metrics:view", "team:approvals:view", "team:approvals:review",
				"inference:call",
			},
		},
		{
			ID:   "developer",
			Name: "Developer",
			Permissions: []string{
				"team:models:deploy", "team:models:undeploy",
				"team:keys:create",
				"team:metrics:view",
				"inference:call",
			},
		},
		{
			ID:   "viewer",
			Name: "Viewer",
			Permissions: []string{
				"team:members:view",
				"team:metrics:view",
				"team:approvals:view",
			},
		},
		{
			ID:          "inference_only",
			Name:        "Inference Only",
			Permissions: []string{"inference:call"},
		},
	}
}

// BuiltinRole is a platform-defined role definition.
type BuiltinRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// Context carries the resolved identity and permissions for a request.
// Populated by the RBAC middleware from the request credentials.
type Context struct {
	UserID          string // platform user ID (OIDC sub or LDAP DN)
	Email           string
	TeamID          string   // active team for this request (from API key or JWT claim)
	OrgID           string   // org of the active team
	Permissions     []string // resolved effective permissions
	IsOrgAdmin      bool     // shortcut for org:* permissions check
	IsPlatformAdmin bool     // shortcut for platform:* permissions check
	// Legacy: if using API key (not yet migrated to team model), these are set.
	LegacyAPIKeyRole string // "admin" | "viewer" | "inference" from old api_keys.role
}

// FromLegacyRole converts an old-style API key role to a permissions Context.
// Used for backward compat while teams are being migrated.
func FromLegacyRole(keyID, role string) Context {
	perms := legacyRolePerms(role)
	return Context{
		UserID:           keyID,
		LegacyAPIKeyRole: role,
		Permissions:      perms,
		IsPlatformAdmin:  role == "admin",
	}
}

func legacyRolePerms(role string) []string {
	switch role {
	case "admin":
		for _, r := range SystemRoles() {
			if r.ID == "platform_admin" {
				return r.Permissions
			}
		}
	case "viewer":
		for _, r := range SystemRoles() {
			if r.ID == "viewer" {
				return r.Permissions
			}
		}
	case "inference":
		return []string{"inference:call"}
	}
	return nil
}

// Merge merges permissions from multiple sets into a single deduplicated,
// sorted slice. Safe to call with nil or empty slices.
func Merge(permSets ...[]string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, ps := range permSets {
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				result = append(result, p)
			}
		}
	}
	sort.Strings(result)
	return result
}
