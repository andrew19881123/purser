// Package permissions defines the fine-grained permission strings used by
// Purser's custom-role RBAC system (v0.4+). Permission strings follow the
// pattern <scope>:<resource>:<action>. The Scope field groups them for display
// and discovery via GET /api/v1/platform/permissions.
package permissions

// Platform-scope permissions — apply to the control plane itself.
const (
	// PermPlatformUsersView allows listing and reading platform users.
	PermPlatformUsersView = "platform:users:view"
	// PermPlatformUsersManage allows creating and deactivating platform users.
	PermPlatformUsersManage = "platform:users:manage"
	// PermPlatformOrgsView allows listing and reading orgs.
	PermPlatformOrgsView = "platform:orgs:view"
	// PermPlatformOrgsManage allows creating and managing orgs.
	PermPlatformOrgsManage = "platform:orgs:manage"
	// PermPlatformAuditView allows reading the platform-level audit log.
	PermPlatformAuditView = "platform:audit:view"
)

// Org-scope permissions — apply to a single org.
const (
	// PermOrgMembersView allows listing org members.
	PermOrgMembersView = "org:members:view"
	// PermOrgMembersManage allows adding/removing members from an org.
	PermOrgMembersManage = "org:members:manage"
	// PermOrgRolesView allows listing custom roles in an org.
	PermOrgRolesView = "org:roles:view"
	// PermOrgRolesManage allows creating/editing/deleting custom roles in an org.
	PermOrgRolesManage = "org:roles:manage"
	// PermOrgTeamsView allows listing teams within an org.
	PermOrgTeamsView = "org:teams:view"
	// PermOrgTeamsManage allows creating and managing teams within an org.
	PermOrgTeamsManage = "org:teams:manage"
	// PermOrgBillingView allows viewing billing and quota information for an org.
	PermOrgBillingView = "org:billing:view"
	// PermOrgBillingManage allows adjusting billing limits and quotas for an org.
	PermOrgBillingManage = "org:billing:manage"
	// PermOrgAuditView allows reading the org-level audit log.
	PermOrgAuditView = "org:audit:view"
	// PermOrgPolicyView allows reading OPA/Rego policies in an org.
	PermOrgPolicyView = "org:policy:view"
	// PermOrgPolicyManage allows creating/editing policies within an org.
	PermOrgPolicyManage = "org:policy:manage"
)

// Team-scope permissions — apply to a single team.
const (
	// PermTeamModelsView allows listing models registered to the team.
	PermTeamModelsView = "team:models:view"
	// PermTeamModelsDeploy allows deploying models to the team's node pool.
	PermTeamModelsDeploy = "team:models:deploy"
	// PermTeamModelsDelete allows removing model deployments in the team.
	PermTeamModelsDelete = "team:models:delete"
	// PermTeamNodesView allows listing nodes registered to the team.
	PermTeamNodesView = "team:nodes:view"
	// PermTeamNodesManage allows enrolling and draining nodes in the team.
	PermTeamNodesManage = "team:nodes:manage"
	// PermTeamMetricsView allows reading live metrics for the team's resources.
	PermTeamMetricsView = "team:metrics:view"
	// PermTeamAuditView allows reading the team-level audit log.
	PermTeamAuditView = "team:audit:view"
	// PermTeamAPIKeysView allows listing API keys scoped to the team.
	PermTeamAPIKeysView = "team:apikeys:view"
	// PermTeamAPIKeysManage allows creating and revoking API keys in the team.
	PermTeamAPIKeysManage = "team:apikeys:manage"
	// PermTeamConfigView allows reading the team's desired-state config.
	PermTeamConfigView = "team:config:view"
	// PermTeamConfigApply allows applying config-as-code to the team.
	PermTeamConfigApply = "team:config:apply"
	// PermTeamApprovalVote allows casting an approval vote for team deployments.
	PermTeamApprovalVote = "team:approval:vote"
)

// Inference-scope permissions — apply to the inference gateway.
const (
	// PermInferenceCall allows calling the inference API (/v1/...).
	PermInferenceCall = "inference:call"
	// PermInferenceStream allows streaming inference responses.
	PermInferenceStream = "inference:stream"
	// PermInferenceAuditView allows reading inference audit logs for the team.
	PermInferenceAuditView = "inference:audit:view"
	// PermInferenceUsageView allows reading per-request token usage.
	PermInferenceUsageView = "inference:usage:view"
	// PermInferenceQuotaManage allows adjusting inference quotas.
	PermInferenceQuotaManage = "inference:quota:manage"
)

// PermDesc describes a single permission string with its scope and human-readable label.
type PermDesc struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Scope       string `json:"scope"` // "platform" | "org" | "team" | "inference"
}

// All returns every known permission descriptor, sorted by scope then key. This
// list is served verbatim by GET /api/v1/platform/permissions.
func All() []PermDesc {
	return []PermDesc{
		// Platform
		{PermPlatformUsersView, "List and read platform users", "platform"},
		{PermPlatformUsersManage, "Create and deactivate platform users", "platform"},
		{PermPlatformOrgsView, "List and read organisations", "platform"},
		{PermPlatformOrgsManage, "Create and manage organisations", "platform"},
		{PermPlatformAuditView, "Read the platform-level audit log", "platform"},
		// Org
		{PermOrgMembersView, "List org members", "org"},
		{PermOrgMembersManage, "Add and remove org members", "org"},
		{PermOrgRolesView, "List custom roles in an org", "org"},
		{PermOrgRolesManage, "Create, edit, and delete custom roles in an org", "org"},
		{PermOrgTeamsView, "List teams within an org", "org"},
		{PermOrgTeamsManage, "Create and manage teams within an org", "org"},
		{PermOrgBillingView, "View billing and quota information", "org"},
		{PermOrgBillingManage, "Adjust billing limits and quotas", "org"},
		{PermOrgAuditView, "Read the org-level audit log", "org"},
		{PermOrgPolicyView, "Read OPA/Rego policies in an org", "org"},
		{PermOrgPolicyManage, "Create and edit policies within an org", "org"},
		// Team
		{PermTeamModelsView, "List models registered to the team", "team"},
		{PermTeamModelsDeploy, "Deploy models to the team's node pool", "team"},
		{PermTeamModelsDelete, "Remove model deployments in the team", "team"},
		{PermTeamNodesView, "List nodes registered to the team", "team"},
		{PermTeamNodesManage, "Enroll and drain nodes in the team", "team"},
		{PermTeamMetricsView, "Read live metrics for the team's resources", "team"},
		{PermTeamAuditView, "Read the team-level audit log", "team"},
		{PermTeamAPIKeysView, "List API keys scoped to the team", "team"},
		{PermTeamAPIKeysManage, "Create and revoke API keys in the team", "team"},
		{PermTeamConfigView, "Read the team's desired-state config", "team"},
		{PermTeamConfigApply, "Apply config-as-code to the team", "team"},
		{PermTeamApprovalVote, "Cast an approval vote for team deployments", "team"},
		// Inference
		{PermInferenceCall, "Call the inference API (/v1/...)", "inference"},
		{PermInferenceStream, "Stream inference responses", "inference"},
		{PermInferenceAuditView, "Read inference audit logs for the team", "inference"},
		{PermInferenceUsageView, "Read per-request token usage", "inference"},
		{PermInferenceQuotaManage, "Adjust inference quotas", "inference"},
	}
}
