package server

// Code note: this file is the SINGLE SOURCE OF TRUTH for the control-plane's
// HTTP route table. It is consumed twice:
//
//  1. routes() (server.go) iterates apiRoutes to register every handler on the
//     mux. Registration behaviour is unchanged from the old hand-written
//     s.mux.HandleFunc(...) block — same method+path patterns, same handlers.
//  2. GenerateOpenAPISpec (openapi_gen.go) iterates the same table to emit
//     openapi.json, so the published contract can never again drift from the
//     routes the server actually serves (the failure this registry fixes: only
//     22 of 127 routes used to appear in the hand-maintained contract).
//
// To add an endpoint: add ONE routeDef row here. The mux registration and the
// OpenAPI operation are produced from it automatically. Regenerate the spec
// with `go generate ./server/...` (or `go test` will flag it as stale).
//
// Rich request/response schemas for a subset of routes live in openapi.base.json
// keyed by "METHOD /full/path"; rows without an overlay entry are emitted with
// a generic body and an x-purser-todo marker rather than an invented schema.

import "net/http"

// routeKind selects how routes() wraps a handler before registering it.
type routeKind int

const (
	// kindPlain registers the handler directly: s.mux.HandleFunc(pattern, h).
	kindPlain routeKind = iota
	// kindPolicyDeploy wraps the handler in policyMiddleware("deploy"), matching
	// the pre-registry behaviour of POST /models/{id}/deploy.
	kindPolicyDeploy
)

// routeDef is one row of the declarative route table.
type routeDef struct {
	Method  string // HTTP method (GET, POST, PUT, DELETE)
	Path    string // full path including the /api/v1 prefix, with {param} segments
	Tag     string // OpenAPI tag: the resource group this operation belongs to
	Summary string // one-line human summary for the OpenAPI operation
	OpID    string // OpenAPI operationId (stable client method name)
	Exempt  bool   // true = internal/auth/health; omitted from the OpenAPI contract
	kind    routeKind
	handler func(*Server) http.HandlerFunc // resolves the handler against a Server
}

// apiRoutes is the complete, ordered route table. Order is preserved from the
// original routes() registrations so behaviour and any pattern-precedence are
// identical.
var apiRoutes = []routeDef{
	{Method: "GET", Path: "/auth/login", Tag: "Authentication", Summary: "Begin the OIDC login redirect", OpID: "authLogin", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleAuthLogin }},
	{Method: "GET", Path: "/auth/callback", Tag: "Authentication", Summary: "OIDC callback", OpID: "authCallback", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleAuthCallback }},
	{Method: "GET", Path: "/auth/logout", Tag: "Authentication", Summary: "OIDC logout", OpID: "authLogout", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleAuthLogout }},
	{Method: "POST", Path: "/auth/backchannel-logout", Tag: "Authentication", Summary: "OIDC backchannel logout", OpID: "backchannelLogout", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleBackchannelLogout }},
	{Method: "POST", Path: "/auth/token", Tag: "Authentication", Summary: "OAuth2 client_credentials token grant", OpID: "tokenEndpoint", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleTokenEndpoint }},
	{Method: "GET", Path: "/auth/ldap-login", Tag: "Authentication", Summary: "Render the LDAP login form", OpID: "lDAPLoginForm", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleLDAPLoginForm }},
	{Method: "POST", Path: "/auth/ldap-login", Tag: "Authentication", Summary: "Submit LDAP credentials", OpID: "lDAPLogin", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleLDAPLogin }},
	{Method: "POST", Path: "/api/v1/ldap/test", Tag: "Authentication", Summary: "Test LDAP connectivity (admin)", OpID: "lDAPTest", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleLDAPTest }},
	{Method: "POST", Path: "/api/v1/service-accounts", Tag: "Service Accounts", Summary: "Create a service account", OpID: "createServiceAccount", handler: func(s *Server) http.HandlerFunc { return s.handleCreateServiceAccount }},
	{Method: "GET", Path: "/api/v1/service-accounts", Tag: "Service Accounts", Summary: "List service accounts", OpID: "listServiceAccounts", handler: func(s *Server) http.HandlerFunc { return s.handleListServiceAccounts }},
	{Method: "DELETE", Path: "/api/v1/service-accounts/{id}", Tag: "Service Accounts", Summary: "Revoke a service account", OpID: "revokeServiceAccount", handler: func(s *Server) http.HandlerFunc { return s.handleRevokeServiceAccount }},
	{Method: "GET", Path: "/api/v1/nodes", Tag: "Nodes", Summary: "List all enrolled nodes", OpID: "listNodes", handler: func(s *Server) http.HandlerFunc { return s.handleListNodes }},
	{Method: "GET", Path: "/api/v1/nodes/{id}", Tag: "Nodes", Summary: "Get a node by ID", OpID: "getNode", handler: func(s *Server) http.HandlerFunc { return s.handleGetNode }},
	{Method: "POST", Path: "/api/v1/nodes/{id}/drain", Tag: "Nodes", Summary: "Cordon a node (mark it DRAINING)", OpID: "drainNode", handler: func(s *Server) http.HandlerFunc { return s.handleDrainNode }},
	{Method: "POST", Path: "/api/v1/nodes/{id}/restart", Tag: "Nodes", Summary: "Restart a node's agent", OpID: "restartNode", handler: func(s *Server) http.HandlerFunc { return s.handleRestartNode }},
	{Method: "DELETE", Path: "/api/v1/nodes/{id}", Tag: "Nodes", Summary: "Decommission a node", OpID: "deleteNode", handler: func(s *Server) http.HandlerFunc { return s.handleDeleteNode }},
	{Method: "GET", Path: "/api/v1/models", Tag: "Models", Summary: "List the model catalog", OpID: "listModels", handler: func(s *Server) http.HandlerFunc { return s.handleListModels }},
	{Method: "POST", Path: "/api/v1/models", Tag: "Models", Summary: "Register a model in the catalog", OpID: "createModel", handler: func(s *Server) http.HandlerFunc { return s.handleCreateModel }},
	{Method: "POST", Path: "/api/v1/models/import", Tag: "Models", Summary: "Import a model from a Hugging Face-style source", OpID: "importModel", handler: func(s *Server) http.HandlerFunc { return s.handleImportModel }},
	{Method: "POST", Path: "/api/v1/models/import/cpu", Tag: "Models", Summary: "Import a model for CPU-only inference", OpID: "importCPUModel", handler: func(s *Server) http.HandlerFunc { return s.handleImportCPUModel }},
	{Method: "GET", Path: "/api/v1/models/{id}", Tag: "Models", Summary: "Get a model by ID", OpID: "getModel", handler: func(s *Server) http.HandlerFunc { return s.handleGetModel }},
	{Method: "DELETE", Path: "/api/v1/models/{id}", Tag: "Models", Summary: "Remove a model from the catalog", OpID: "deleteModel", handler: func(s *Server) http.HandlerFunc { return s.handleDeleteModel }},
	{Method: "GET", Path: "/api/v1/models/{id}/health", Tag: "Models", Summary: "Get live health for a deployed model", OpID: "modelHealth", handler: func(s *Server) http.HandlerFunc { return s.handleModelHealth }},
	{Method: "POST", Path: "/api/v1/models/{id}/plan", Tag: "Models", Summary: "Dry-run plan preview (read-only, no side effects)", OpID: "previewPlan", handler: func(s *Server) http.HandlerFunc { return s.handlePreviewPlan }},
	{Method: "POST", Path: "/api/v1/models/{id}/deploy", Tag: "Models", Summary: "Deploy a model", OpID: "deployModel", kind: kindPolicyDeploy, handler: func(s *Server) http.HandlerFunc { return s.handleDeployModel }},
	{Method: "POST", Path: "/api/v1/join-token", Tag: "Enrollment", Summary: "Mint a cluster join token", OpID: "createJoinToken", handler: func(s *Server) http.HandlerFunc { return s.handleJoinToken }},
	{Method: "GET", Path: "/api/v1/enrollment-bundle", Tag: "Enrollment", Summary: "Download a node enrollment bundle", OpID: "enrollmentBundle", handler: func(s *Server) http.HandlerFunc { return s.handleEnrollmentBundle }},
	{Method: "POST", Path: "/api/v1/enrollment/renew", Tag: "Enrollment", Summary: "Renew a node certificate (internal agent to CP)", OpID: "enrollmentRenew", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleEnrollmentRenew }},
	{Method: "GET", Path: "/api/v1/deployments", Tag: "Deployments", Summary: "List all deployments", OpID: "listDeployments", handler: func(s *Server) http.HandlerFunc { return s.handleListDeployments }},
	{Method: "DELETE", Path: "/api/v1/deployments/{id}", Tag: "Deployments", Summary: "Tear down a deployment", OpID: "deleteDeployment", handler: func(s *Server) http.HandlerFunc { return s.handleDeleteDeployment }},
	{Method: "GET", Path: "/api/v1/plans/{id}", Tag: "Plans", Summary: "Get a stored deployment plan by ID", OpID: "getPlan", handler: func(s *Server) http.HandlerFunc { return s.handleGetPlan }},
	{Method: "GET", Path: "/api/v1/cluster/health", Tag: "Cluster", Summary: "Liveness probe", OpID: "clusterHealth", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleClusterHealth }},
	{Method: "GET", Path: "/api/v1/cluster/status", Tag: "Cluster", Summary: "Get control-plane cluster (Raft) status", OpID: "clusterStatus", handler: func(s *Server) http.HandlerFunc { return s.handleClusterStatus }},
	{Method: "POST", Path: "/api/v1/apikeys", Tag: "API Keys", Summary: "Mint a new gateway API key", OpID: "createAPIKey", handler: func(s *Server) http.HandlerFunc { return s.handleCreateAPIKey }},
	{Method: "GET", Path: "/api/v1/apikeys", Tag: "API Keys", Summary: "List gateway API keys (metadata only)", OpID: "listAPIKeys", handler: func(s *Server) http.HandlerFunc { return s.handleListAPIKeys }},
	{Method: "DELETE", Path: "/api/v1/apikeys/{id}", Tag: "API Keys", Summary: "Revoke an API key", OpID: "deleteAPIKey", handler: func(s *Server) http.HandlerFunc { return s.handleDeleteAPIKey }},
	{Method: "POST", Path: "/api/v1/apikeys/{id}/rotate", Tag: "API Keys", Summary: "Rotate an API key", OpID: "rotateAPIKey", handler: func(s *Server) http.HandlerFunc { return s.handleRotateAPIKey }},
	{Method: "GET", Path: "/api/v1/apikeys/{id}/access-log", Tag: "API Keys", Summary: "List access-log entries for an API key (redirects to /logs/access)", OpID: "aPIKeyAccessLogRedirect", handler: func(s *Server) http.HandlerFunc { return s.handleAPIKeyAccessLogRedirect }},
	{Method: "GET", Path: "/api/v1/logs/access", Tag: "Access Logs", Summary: "List API access-log entries", OpID: "listAccessLogs", handler: func(s *Server) http.HandlerFunc { return s.handleListAccessLogs }},
	{Method: "GET", Path: "/api/v1/metrics", Tag: "Metrics", Summary: "Live cluster metrics (Server-Sent Events)", OpID: "metricsSSE", handler: func(s *Server) http.HandlerFunc { return s.handleMetricsSSE }},
	{Method: "GET", Path: "/api/v1/openapi.json", Tag: "Meta", Summary: "Serve this OpenAPI document", OpID: "getOpenAPISpec", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleOpenAPISpec }},
	{Method: "POST", Path: "/api/v1/usage", Tag: "Usage", Summary: "Ingest usage records (internal gateway to CP)", OpID: "recordUsage", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleRecordUsage }},
	{Method: "GET", Path: "/api/v1/apikeys/{id}/usage", Tag: "API Keys", Summary: "Get usage totals for an API key", OpID: "getKeyUsage", handler: func(s *Server) http.HandlerFunc { return s.handleGetKeyUsage }},
	{Method: "GET", Path: "/api/v1/usage/summary", Tag: "Usage", Summary: "Get aggregate usage summary", OpID: "usageSummary", handler: func(s *Server) http.HandlerFunc { return s.handleUsageSummary }},
	{Method: "GET", Path: "/api/v1/enterprise/status", Tag: "Enterprise", Summary: "Report the active Purser edition", OpID: "enterpriseStatus", handler: func(s *Server) http.HandlerFunc { return s.handleEnterpriseStatus }},
	{Method: "GET", Path: "/api/v1/enterprise/audit-log", Tag: "Enterprise", Summary: "Tamper-evident audit log (enterprise only)", OpID: "enterpriseAuditLog", handler: func(s *Server) http.HandlerFunc { return s.handleEnterpriseAuditLog }},
	{Method: "GET", Path: "/api/v1/reconciler/status", Tag: "Reconciler", Summary: "Get reconciler configuration and tracker state", OpID: "reconcilerStatus", handler: func(s *Server) http.HandlerFunc { return s.handleReconcilerStatus }},
	{Method: "GET", Path: "/api/v1/fleet/capacity", Tag: "Fleet", Summary: "Get fleet capacity headroom", OpID: "fleetCapacity", handler: func(s *Server) http.HandlerFunc { return s.handleFleetCapacity }},
	{Method: "POST", Path: "/api/v1/config/apply", Tag: "Config-as-Code", Summary: "Apply desired-state configuration (purser.yaml)", OpID: "configApply", handler: func(s *Server) http.HandlerFunc { return s.handleConfigApply }},
	{Method: "POST", Path: "/api/v1/config/diff", Tag: "Config-as-Code", Summary: "Diff desired-state configuration against current state", OpID: "configDiff", handler: func(s *Server) http.HandlerFunc { return s.handleConfigDiff }},
	{Method: "GET", Path: "/api/v1/config/export", Tag: "Config-as-Code", Summary: "Export current state as configuration", OpID: "configExport", handler: func(s *Server) http.HandlerFunc { return s.handleConfigExport }},
	{Method: "GET", Path: "/api/v1/inference-audit", Tag: "Inference Audit", Summary: "List inference audit-log entries", OpID: "listInferenceAudit", handler: func(s *Server) http.HandlerFunc { return s.handleListInferenceAudit }},
	{Method: "GET", Path: "/api/v1/inference-audit/verify", Tag: "Inference Audit", Summary: "Verify the inference audit hash chain", OpID: "verifyInferenceChain", handler: func(s *Server) http.HandlerFunc { return s.handleVerifyInferenceChain }},
	{Method: "POST", Path: "/api/v1/inference-events", Tag: "Inference Audit", Summary: "Ingest inference events (internal gateway to CP)", OpID: "recordInferenceEvent", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleRecordInferenceEvent }},
	{Method: "GET", Path: "/api/v1/policies", Tag: "Policies", Summary: "List policies", OpID: "listPolicies", handler: func(s *Server) http.HandlerFunc { return s.handleListPolicies }},
	{Method: "PUT", Path: "/api/v1/policies/{name}", Tag: "Policies", Summary: "Create or update a policy", OpID: "upsertPolicy", handler: func(s *Server) http.HandlerFunc { return s.handleUpsertPolicy }},
	{Method: "DELETE", Path: "/api/v1/policies/{name}", Tag: "Policies", Summary: "Delete a policy", OpID: "deletePolicy", handler: func(s *Server) http.HandlerFunc { return s.handleDeletePolicy }},
	{Method: "POST", Path: "/api/v1/policies/eval", Tag: "Policies", Summary: "Evaluate a policy against a sample input", OpID: "evalPolicy", handler: func(s *Server) http.HandlerFunc { return s.handleEvalPolicy }},
	{Method: "GET", Path: "/api/v1/approvals", Tag: "Deployment Approvals", Summary: "List deployment approvals", OpID: "listApprovals", handler: func(s *Server) http.HandlerFunc { return s.handleListApprovals }},
	{Method: "GET", Path: "/api/v1/approvals/{deploymentId}", Tag: "Deployment Approvals", Summary: "Get a deployment approval", OpID: "getApproval", handler: func(s *Server) http.HandlerFunc { return s.handleGetApproval }},
	{Method: "POST", Path: "/api/v1/approvals/{deploymentId}/approve", Tag: "Deployment Approvals", Summary: "Approve a pending deployment", OpID: "approveDeployment", handler: func(s *Server) http.HandlerFunc { return s.handleApproveDeployment }},
	{Method: "POST", Path: "/api/v1/approvals/{deploymentId}/reject", Tag: "Deployment Approvals", Summary: "Reject a pending deployment", OpID: "rejectDeployment", handler: func(s *Server) http.HandlerFunc { return s.handleRejectDeployment }},
	{Method: "GET", Path: "/api/v1/billing/report", Tag: "Billing", Summary: "Get the detailed chargeback report", OpID: "billingReport", handler: func(s *Server) http.HandlerFunc { return s.handleBillingReport }},
	{Method: "GET", Path: "/api/v1/billing/summary", Tag: "Billing", Summary: "Get the billing summary", OpID: "billingSummary", handler: func(s *Server) http.HandlerFunc { return s.handleBillingSummary }},
	{Method: "GET", Path: "/api/v1/billing/forecast", Tag: "Billing", Summary: "Get a cost forecast", OpID: "billingForecast", handler: func(s *Server) http.HandlerFunc { return s.handleBillingForecast }},
	{Method: "GET", Path: "/api/v1/billing/models/adoption", Tag: "Billing", Summary: "Get per-model adoption metrics", OpID: "modelAdoption", handler: func(s *Server) http.HandlerFunc { return s.handleModelAdoption }},
	{Method: "GET", Path: "/api/v1/platform/orgs/{orgId}/billing", Tag: "Organizations", Summary: "Get the billing report for an org", OpID: "orgBillingReport", handler: func(s *Server) http.HandlerFunc { return s.handleOrgBillingReport }},
	{Method: "GET", Path: "/api/v1/platform/teams/{teamId}/billing", Tag: "Teams", Summary: "Get the billing report for a team", OpID: "teamBillingReport", handler: func(s *Server) http.HandlerFunc { return s.handleTeamBillingReport }},
	{Method: "GET", Path: "/api/v1/slo/compliance", Tag: "SLO", Summary: "Get per-model SLO compliance rates", OpID: "sLOCompliance", handler: func(s *Server) http.HandlerFunc { return s.handleSLOCompliance }},
	{Method: "GET", Path: "/api/v1/compliance/ai-act/technical-doc", Tag: "Compliance", Summary: "Generate the AI Act technical documentation", OpID: "aIActTechnicalDoc", handler: func(s *Server) http.HandlerFunc { return s.handleAIActTechnicalDoc }},
	{Method: "GET", Path: "/api/v1/compliance/gdpr/record-of-processing", Tag: "Compliance", Summary: "Generate the GDPR record of processing", OpID: "gDPRRecordOfProcessing", handler: func(s *Server) http.HandlerFunc { return s.handleGDPRRecordOfProcessing }},
	{Method: "POST", Path: "/api/v1/gdpr/erasure", Tag: "GDPR", Summary: "Execute a GDPR right-to-erasure request", OpID: "gDPRErasure", handler: func(s *Server) http.HandlerFunc { return s.handleGDPRErasure }},
	{Method: "GET", Path: "/api/v1/gdpr/erasure-log", Tag: "GDPR", Summary: "List GDPR erasure-log entries", OpID: "gDPRErasureLog", handler: func(s *Server) http.HandlerFunc { return s.handleGDPRErasureLog }},
	{Method: "POST", Path: "/api/v1/platform/orgs", Tag: "Organizations", Summary: "Create org", OpID: "createOrg", handler: func(s *Server) http.HandlerFunc { return s.handleCreateOrg }},
	{Method: "GET", Path: "/api/v1/platform/orgs", Tag: "Organizations", Summary: "List orgs", OpID: "listOrgs", handler: func(s *Server) http.HandlerFunc { return s.handleListOrgs }},
	{Method: "GET", Path: "/api/v1/platform/orgs/{id}", Tag: "Organizations", Summary: "Get org", OpID: "getOrg", handler: func(s *Server) http.HandlerFunc { return s.handleGetOrg }},
	{Method: "PUT", Path: "/api/v1/platform/orgs/{id}", Tag: "Organizations", Summary: "Update org", OpID: "updateOrg", handler: func(s *Server) http.HandlerFunc { return s.handleUpdateOrg }},
	{Method: "DELETE", Path: "/api/v1/platform/orgs/{id}", Tag: "Organizations", Summary: "Delete org", OpID: "deleteOrg", handler: func(s *Server) http.HandlerFunc { return s.handleDeleteOrg }},
	{Method: "POST", Path: "/api/v1/platform/orgs/{orgId}/teams", Tag: "Organizations", Summary: "Create a team in an org", OpID: "createTeam", handler: func(s *Server) http.HandlerFunc { return s.handleCreateTeam }},
	{Method: "GET", Path: "/api/v1/platform/orgs/{orgId}/teams", Tag: "Organizations", Summary: "List teams in an org", OpID: "listTeams", handler: func(s *Server) http.HandlerFunc { return s.handleListTeams }},
	{Method: "GET", Path: "/api/v1/platform/teams/{id}", Tag: "Teams", Summary: "Get team", OpID: "getTeam", handler: func(s *Server) http.HandlerFunc { return s.handleGetTeam }},
	{Method: "PUT", Path: "/api/v1/platform/teams/{id}", Tag: "Teams", Summary: "Update team", OpID: "updateTeam", handler: func(s *Server) http.HandlerFunc { return s.handleUpdateTeam }},
	{Method: "DELETE", Path: "/api/v1/platform/teams/{id}", Tag: "Teams", Summary: "Delete team", OpID: "deleteTeam", handler: func(s *Server) http.HandlerFunc { return s.handleDeleteTeam }},
	{Method: "POST", Path: "/api/v1/platform/orgs/{orgId}/members", Tag: "Organizations", Summary: "Add a member to an org", OpID: "addOrgMember", handler: func(s *Server) http.HandlerFunc { return s.handleAddOrgMember }},
	{Method: "GET", Path: "/api/v1/platform/orgs/{orgId}/members", Tag: "Organizations", Summary: "List org members", OpID: "listOrgMembers", handler: func(s *Server) http.HandlerFunc { return s.handleListOrgMembers }},
	{Method: "PUT", Path: "/api/v1/platform/orgs/{orgId}/members/{userId}", Tag: "Organizations", Summary: "Update an org member's role", OpID: "updateOrgMember", handler: func(s *Server) http.HandlerFunc { return s.handleUpdateOrgMember }},
	{Method: "DELETE", Path: "/api/v1/platform/orgs/{orgId}/members/{userId}", Tag: "Organizations", Summary: "Remove a member from an org", OpID: "removeOrgMember", handler: func(s *Server) http.HandlerFunc { return s.handleRemoveOrgMember }},
	{Method: "POST", Path: "/api/v1/platform/teams/{teamId}/members", Tag: "Teams", Summary: "Add a member to a team", OpID: "addTeamMember", handler: func(s *Server) http.HandlerFunc { return s.handleAddTeamMember }},
	{Method: "GET", Path: "/api/v1/platform/teams/{teamId}/members", Tag: "Teams", Summary: "List team members", OpID: "listTeamMembers", handler: func(s *Server) http.HandlerFunc { return s.handleListTeamMembers }},
	{Method: "PUT", Path: "/api/v1/platform/teams/{teamId}/members/{userId}", Tag: "Teams", Summary: "Update a team member's role", OpID: "updateTeamMember", handler: func(s *Server) http.HandlerFunc { return s.handleUpdateTeamMember }},
	{Method: "DELETE", Path: "/api/v1/platform/teams/{teamId}/members/{userId}", Tag: "Teams", Summary: "Remove a member from a team", OpID: "removeTeamMember", handler: func(s *Server) http.HandlerFunc { return s.handleRemoveTeamMember }},
	{Method: "GET", Path: "/api/v1/platform/users", Tag: "Users", Summary: "List users", OpID: "listUsers", handler: func(s *Server) http.HandlerFunc { return s.handleListUsers }},
	{Method: "GET", Path: "/api/v1/platform/users/me", Tag: "Users", Summary: "Get the current user", OpID: "getMe", handler: func(s *Server) http.HandlerFunc { return s.handleGetMe }},
	{Method: "GET", Path: "/api/v1/platform/users/{id}", Tag: "Users", Summary: "Get user", OpID: "getUser", handler: func(s *Server) http.HandlerFunc { return s.handleGetUser }},
	{Method: "POST", Path: "/api/v1/platform/orgs/{orgId}/roles", Tag: "Organizations", Summary: "Create a custom role in an org", OpID: "createRole", handler: func(s *Server) http.HandlerFunc { return s.handleCreateRole }},
	{Method: "GET", Path: "/api/v1/platform/orgs/{orgId}/roles", Tag: "Organizations", Summary: "List custom roles in an org", OpID: "listRoles", handler: func(s *Server) http.HandlerFunc { return s.handleListRoles }},
	{Method: "GET", Path: "/api/v1/platform/orgs/{orgId}/roles/{id}", Tag: "Organizations", Summary: "Get role", OpID: "getRole", handler: func(s *Server) http.HandlerFunc { return s.handleGetRole }},
	{Method: "PUT", Path: "/api/v1/platform/orgs/{orgId}/roles/{id}", Tag: "Organizations", Summary: "Update role", OpID: "updateRole", handler: func(s *Server) http.HandlerFunc { return s.handleUpdateRole }},
	{Method: "DELETE", Path: "/api/v1/platform/orgs/{orgId}/roles/{id}", Tag: "Organizations", Summary: "Delete role", OpID: "deleteRole", handler: func(s *Server) http.HandlerFunc { return s.handleDeleteRole }},
	{Method: "GET", Path: "/api/v1/platform/permissions", Tag: "RBAC", Summary: "List all assignable permissions", OpID: "listPermissions", handler: func(s *Server) http.HandlerFunc { return s.handleListPermissions }},
	{Method: "GET", Path: "/api/v1/platform/teams/{teamId}/my-permissions", Tag: "Teams", Summary: "List the caller's permissions in a team", OpID: "getMyPermissions", handler: func(s *Server) http.HandlerFunc { return s.handleGetMyPermissions }},
	{Method: "POST", Path: "/api/v1/platform/pools", Tag: "Node Pools", Summary: "Create node pool", OpID: "createPool", handler: func(s *Server) http.HandlerFunc { return s.handleCreatePool }},
	{Method: "GET", Path: "/api/v1/platform/pools", Tag: "Node Pools", Summary: "List node pools", OpID: "listPools", handler: func(s *Server) http.HandlerFunc { return s.handleListPools }},
	{Method: "GET", Path: "/api/v1/platform/pools/{id}", Tag: "Node Pools", Summary: "Get node pool", OpID: "getPool", handler: func(s *Server) http.HandlerFunc { return s.handleGetPool }},
	{Method: "PUT", Path: "/api/v1/platform/pools/{id}", Tag: "Node Pools", Summary: "Update node pool", OpID: "updatePool", handler: func(s *Server) http.HandlerFunc { return s.handleUpdatePool }},
	{Method: "DELETE", Path: "/api/v1/platform/pools/{id}", Tag: "Node Pools", Summary: "Delete node pool", OpID: "deletePool", handler: func(s *Server) http.HandlerFunc { return s.handleDeletePool }},
	{Method: "POST", Path: "/api/v1/platform/pools/{id}/nodes", Tag: "Node Pools", Summary: "Assign a node to a pool", OpID: "assignNodeToPool", handler: func(s *Server) http.HandlerFunc { return s.handleAssignNodeToPool }},
	{Method: "GET", Path: "/api/v1/platform/pools/{id}/nodes", Tag: "Node Pools", Summary: "List nodes in a pool", OpID: "listPoolNodes", handler: func(s *Server) http.HandlerFunc { return s.handleListPoolNodes }},
	{Method: "DELETE", Path: "/api/v1/platform/pools/{id}/nodes/{nodeId}", Tag: "Node Pools", Summary: "Remove a node from a pool", OpID: "removeNodeFromPool", handler: func(s *Server) http.HandlerFunc { return s.handleRemoveNodeFromPool }},
	{Method: "PUT", Path: "/api/v1/platform/pools/{id}/quotas/{teamId}", Tag: "Node Pools", Summary: "Create or update a team's pool quota", OpID: "upsertPoolQuota", handler: func(s *Server) http.HandlerFunc { return s.handleUpsertPoolQuota }},
	{Method: "GET", Path: "/api/v1/platform/pools/{id}/quotas", Tag: "Node Pools", Summary: "List quotas for a pool", OpID: "listPoolQuotas", handler: func(s *Server) http.HandlerFunc { return s.handleListPoolQuotas }},
	{Method: "DELETE", Path: "/api/v1/platform/pools/{id}/quotas/{teamId}", Tag: "Node Pools", Summary: "Delete a team's pool quota", OpID: "deletePoolQuota", handler: func(s *Server) http.HandlerFunc { return s.handleDeletePoolQuota }},
	{Method: "GET", Path: "/api/v1/platform/status", Tag: "Platform", Summary: "Platform status overview (admin)", OpID: "platformStatus", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handlePlatformStatus }},
	{Method: "GET", Path: "/api/v1/platform/health", Tag: "Platform", Summary: "Platform liveness probe", OpID: "platformHealth", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handlePlatformHealth }},
	{Method: "POST", Path: "/api/v1/platform/dataplanes", Tag: "Data Planes", Summary: "Create data plane", OpID: "createDataPlane", handler: func(s *Server) http.HandlerFunc { return s.handleCreateDataPlane }},
	{Method: "GET", Path: "/api/v1/platform/dataplanes", Tag: "Data Planes", Summary: "List data planes", OpID: "listDataPlanes", handler: func(s *Server) http.HandlerFunc { return s.handleListDataPlanes }},
	{Method: "GET", Path: "/api/v1/platform/dataplanes/{id}", Tag: "Data Planes", Summary: "Get data plane", OpID: "getDataPlane", handler: func(s *Server) http.HandlerFunc { return s.handleGetDataPlane }},
	{Method: "PUT", Path: "/api/v1/platform/dataplanes/{id}", Tag: "Data Planes", Summary: "Update data plane", OpID: "updateDataPlane", handler: func(s *Server) http.HandlerFunc { return s.handleUpdateDataPlane }},
	{Method: "DELETE", Path: "/api/v1/platform/dataplanes/{id}", Tag: "Data Planes", Summary: "Delete data plane", OpID: "deleteDataPlane", handler: func(s *Server) http.HandlerFunc { return s.handleDeleteDataPlane }},
	{Method: "POST", Path: "/api/v1/platform/dataplanes/{id}/heartbeat", Tag: "Data Planes", Summary: "Data-plane heartbeat (internal DP to CP)", OpID: "dataPlaneHeartbeat", Exempt: true, handler: func(s *Server) http.HandlerFunc { return s.handleDataPlaneHeartbeat }},
	{Method: "GET", Path: "/api/v1/platform/dataplanes/{id}/config", Tag: "Data Planes", Summary: "Get a data plane's config", OpID: "getDataPlaneConfig", handler: func(s *Server) http.HandlerFunc { return s.handleGetDataPlaneConfig }},
	{Method: "PUT", Path: "/api/v1/platform/dataplanes/{id}/config", Tag: "Data Planes", Summary: "Update a data plane's config", OpID: "putDataPlaneConfig", handler: func(s *Server) http.HandlerFunc { return s.handlePutDataPlaneConfig }},
	{Method: "POST", Path: "/api/v1/platform/dataplanes/{id}/config/refresh", Tag: "Data Planes", Summary: "Push a config refresh to a data plane", OpID: "refreshDataPlaneConfig", handler: func(s *Server) http.HandlerFunc { return s.handleRefreshDataPlaneConfig }},
	{Method: "POST", Path: "/api/v1/platform/dataplanes/{id}/nodes/{nodeId}", Tag: "Data Planes", Summary: "Assign a node to a data plane", OpID: "assignNodeToDataPlane", handler: func(s *Server) http.HandlerFunc { return s.handleAssignNodeToDataPlane }},
	{Method: "DELETE", Path: "/api/v1/platform/dataplanes/{id}/nodes/{nodeId}", Tag: "Data Planes", Summary: "Unassign a node from a data plane", OpID: "unassignNodeFromDataPlane", handler: func(s *Server) http.HandlerFunc { return s.handleUnassignNodeFromDataPlane }},
	{Method: "GET", Path: "/api/v1/platform/dataplanes/{id}/nodes", Tag: "Data Planes", Summary: "List nodes assigned to a data plane", OpID: "listDataPlaneNodes", handler: func(s *Server) http.HandlerFunc { return s.handleListDataPlaneNodes }},
	{Method: "POST", Path: "/api/v1/planner/what-if", Tag: "Planner", Summary: "Run a what-if hardware ROI simulation", OpID: "whatIfPlan", handler: func(s *Server) http.HandlerFunc { return s.handleWhatIfPlan }},
}
