// Package permissions defines the fine-grained permission strings used by
// Purser's custom-role RBAC system (v0.4+). Permission strings follow the
// pattern <scope>:<resource>:<action>. The Scope field groups them for display
// and discovery via GET /api/v1/platform/permissions.
//
// # Single source of truth
//
// The constants declared in this file are THE canonical permission vocabulary.
// Everything downstream derives from them, so the served catalog and the
// enforced checks can never drift apart:
//
//   - All() (this file) is the catalog served by GET /api/v1/platform/permissions.
//   - SystemRoles() (engine.go) grants only these constants.
//   - registry.Perm* are aliases of these constants, and the route→permission
//     map (server/rbac_v2.go) is keyed on those aliases.
//
// Historically the served catalog and the enforced vocabulary were two
// hand-maintained, divergent lists ("team:apikeys:manage" served vs
// "team:keys:create" enforced), which made custom roles built from the catalog
// grant nothing. The invariant "catalog == enforced universe" is now locked in
// by permissions.catalog_test.go and server.rbac_catalog_test.go.
package permissions

// Platform-scope permissions — apply to the control plane itself (cross-org).
const (
	// PermPlatformOrgsCreate allows creating a new organization on the platform.
	PermPlatformOrgsCreate = "platform:orgs:create"
	// PermPlatformOrgsDelete allows deleting an organization and all its teams.
	PermPlatformOrgsDelete = "platform:orgs:delete"
	// PermPlatformPoolsManage allows adding, editing, or removing compute pools
	// platform-wide.
	PermPlatformPoolsManage = "platform:pools:manage"
	// PermPlatformUsersInvite allows inviting users to the platform before they
	// belong to an org.
	PermPlatformUsersInvite = "platform:users:invite"
)

// Org-scope permissions — apply to a single org.
const (
	// PermOrgTeamsCreate allows creating a new team within the organization.
	PermOrgTeamsCreate = "org:teams:create"
	// PermOrgTeamsDelete allows deleting a team and its associated resources.
	PermOrgTeamsDelete = "org:teams:delete"
	// PermOrgMembersInvite allows inviting a user to the organization.
	PermOrgMembersInvite = "org:members:invite"
	// PermOrgMembersRemove allows removing a member from the organization.
	PermOrgMembersRemove = "org:members:remove"
	// PermOrgRolesCreate allows creating a custom role definition scoped to the org.
	PermOrgRolesCreate = "org:roles:create"
	// PermOrgRolesDelete allows deleting a custom role definition.
	PermOrgRolesDelete = "org:roles:delete"
	// PermOrgPoolsRequest allows requesting additional compute pool quota for the org.
	PermOrgPoolsRequest = "org:pools:request"
)

// Team-scope permissions — apply to a single team.
const (
	// PermTeamModelsDeploy allows deploying a model to a team's serving pool.
	PermTeamModelsDeploy = "team:models:deploy"
	// PermTeamModelsUndeploy allows removing a deployed model from the serving pool.
	PermTeamModelsUndeploy = "team:models:undeploy"
	// PermTeamKeysCreate allows issuing (and rotating) a new API key for the team.
	PermTeamKeysCreate = "team:keys:create"
	// PermTeamKeysRevoke allows revoking an existing API key.
	PermTeamKeysRevoke = "team:keys:revoke"
	// PermTeamMembersView allows listing team members and their roles.
	PermTeamMembersView = "team:members:view"
	// PermTeamMembersInvite allows adding a member to the team.
	PermTeamMembersInvite = "team:members:invite"
	// PermTeamMembersRemove allows removing a member from the team.
	PermTeamMembersRemove = "team:members:remove"
	// PermTeamMetricsView allows reading inference throughput, latency, and cost
	// metrics (and, by design, the read/list surface guarded by this permission).
	PermTeamMetricsView = "team:metrics:view"
	// PermTeamApprovalsView allows viewing pending deployment approval requests.
	PermTeamApprovalsView = "team:approvals:view"
	// PermTeamApprovalsReview allows approving or rejecting deployment requests.
	PermTeamApprovalsReview = "team:approvals:review"
)

// Inference-scope permissions — apply to the inference gateway.
const (
	// PermInferenceCall allows calling the inference API (/v1/...).
	PermInferenceCall = "inference:call"
)

// PermDesc describes a single permission string with its scope and human-readable label.
type PermDesc struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Scope       string `json:"scope"` // "platform" | "org" | "team" | "inference"
}

// catalog is the canonical, ordered list of every permission the platform
// recognizes. It is grouped by scope (platform, org, team, inference) for
// display. This is the ONLY place permission descriptors are declared; both
// All() and IsKnown() derive from it, and it must stay set-equal to the
// vocabulary the built-in roles grant (guarded by catalog_test.go).
var catalog = []PermDesc{
	// Platform
	{PermPlatformOrgsCreate, "Create a new organization on the platform", "platform"},
	{PermPlatformOrgsDelete, "Delete an existing organization (and all its teams)", "platform"},
	{PermPlatformPoolsManage, "Add, edit, or remove GPU/compute pools platform-wide", "platform"},
	{PermPlatformUsersInvite, "Invite users to the platform before they belong to an org", "platform"},
	// Org
	{PermOrgTeamsCreate, "Create a new team within the organization", "org"},
	{PermOrgTeamsDelete, "Delete a team and all its associated resources", "org"},
	{PermOrgMembersInvite, "Invite a user to the organization", "org"},
	{PermOrgMembersRemove, "Remove a member from the organization", "org"},
	{PermOrgRolesCreate, "Create a custom role definition scoped to the org", "org"},
	{PermOrgRolesDelete, "Delete a custom role definition", "org"},
	{PermOrgPoolsRequest, "Request additional compute pool quota for the org", "org"},
	// Team
	{PermTeamModelsDeploy, "Deploy a model to a team's serving pool", "team"},
	{PermTeamModelsUndeploy, "Remove a deployed model from the serving pool", "team"},
	{PermTeamKeysCreate, "Issue (or rotate) an API key for the team", "team"},
	{PermTeamKeysRevoke, "Revoke an existing API key", "team"},
	{PermTeamMembersView, "List team members and their roles", "team"},
	{PermTeamMembersInvite, "Add a member to the team", "team"},
	{PermTeamMembersRemove, "Remove a member from the team", "team"},
	{PermTeamMetricsView, "Read inference throughput, latency, and cost metrics", "team"},
	{PermTeamApprovalsView, "View pending deployment approval requests", "team"},
	{PermTeamApprovalsReview, "Approve or reject deployment requests", "team"},
	// Inference
	{PermInferenceCall, "Send requests to the gateway (/v1/chat/completions, etc.)", "inference"},
}

// All returns every known permission descriptor, grouped by scope. This list is
// served verbatim by GET /api/v1/platform/permissions and is the canonical
// reference operators use to build custom roles. Every key here is a string the
// enforcement layer actually checks (see package doc).
func All() []PermDesc {
	// Return a copy so callers cannot mutate the canonical catalog.
	out := make([]PermDesc, len(catalog))
	copy(out, catalog)
	return out
}

// knownPerms is the set form of the catalog, built once for O(1) validation.
var knownPerms = func() map[string]struct{} {
	m := make(map[string]struct{}, len(catalog))
	for _, d := range catalog {
		m[d.Key] = struct{}{}
	}
	return m
}()

// IsKnown reports whether perm is a recognized permission string, i.e. one that
// appears in the served catalog and is therefore enforceable. Unknown / typo'd
// strings return false so callers can reject them before persisting a role that
// would grant nothing.
func IsKnown(perm string) bool {
	_, ok := knownPerms[perm]
	return ok
}
