package registry

import (
	"context"
	"time"
)

// Registry is the storage abstraction for all persistent control-plane state.
//
// It is intentionally backend-neutral: the MVP ships a single-node
// [SQLiteRegistry], while HA deployments can supply a replicated
// implementation without any caller changes. All methods take a context so
// callers can bound latency and propagate cancellation.
//
// The MVP implements full CRUD for nodes, models, deployments and API keys,
// plus an append-only audit log. Links, plans, sessions and certs have schema
// support; their typed accessors are added as those subsystems land (phase 2).
type Registry interface {
	// Migrate creates or upgrades the schema. It is idempotent and safe to
	// call on every startup.
	Migrate(ctx context.Context) error
	// Ping verifies the store is reachable.
	Ping(ctx context.Context) error
	// Close releases underlying resources (connections, file handles).
	Close() error

	// --- Nodes -------------------------------------------------------------
	CreateNode(ctx context.Context, n *Node) error
	GetNode(ctx context.Context, id string) (*Node, error)
	ListNodes(ctx context.Context) ([]*Node, error)
	UpdateNode(ctx context.Context, n *Node) error
	DeleteNode(ctx context.Context, id string) error

	// --- Links -------------------------------------------------------------
	// UpsertLink inserts or updates the measured edge (from_node, to_node).
	UpsertLink(ctx context.Context, l *Link) error
	ListLinks(ctx context.Context) ([]*Link, error)

	// --- Models ------------------------------------------------------------
	CreateModel(ctx context.Context, m *Model) error
	GetModel(ctx context.Context, id string) (*Model, error)
	ListModels(ctx context.Context) ([]*Model, error)
	UpdateModel(ctx context.Context, m *Model) error
	DeleteModel(ctx context.Context, id string) error

	// --- Plans -------------------------------------------------------------
	CreatePlan(ctx context.Context, p *Plan) error
	GetPlan(ctx context.Context, id string) (*Plan, error)
	ListPlans(ctx context.Context) ([]*Plan, error)
	DeletePlan(ctx context.Context, id string) error

	// --- Deployments -------------------------------------------------------
	CreateDeployment(ctx context.Context, d *Deployment) error
	GetDeployment(ctx context.Context, id string) (*Deployment, error)
	ListDeployments(ctx context.Context) ([]*Deployment, error)
	// ListDeploymentsByTenant returns deployments scoped to tenant.
	// When tenant is "", all deployments are returned (admin view).
	// When tenant is non-empty, only deployments whose Detail JSON contains a
	// matching "tenant" field are returned. This is a Go-side filter because
	// the deployments table has no top-level tenant_id column (planned for v0.4).
	ListDeploymentsByTenant(ctx context.Context, tenant string) ([]*Deployment, error)
	UpdateDeployment(ctx context.Context, d *Deployment) error
	DeleteDeployment(ctx context.Context, id string) error

	// --- API keys ----------------------------------------------------------
	CreateAPIKey(ctx context.Context, k *APIKey) error
	GetAPIKey(ctx context.Context, id string) (*APIKey, error)
	// GetAPIKeyByHash returns the single enabled API key whose key_hash equals
	// keyHash (SHA-256 hex of the raw token). Returns ErrNotFound when no
	// enabled key matches. The single-row indexed query is O(1) vs. the O(n)
	// full-scan that ListAPIKeys + loop would require.
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error)
	ListAPIKeys(ctx context.Context) ([]*APIKey, error)
	// ListAPIKeysByTenant returns API keys scoped to tenant.
	// When tenant is "", all keys are returned (admin view).
	// When tenant is non-empty, only enabled keys belonging to that tenant
	// are returned; disabled keys are hidden from tenant-scoped views.
	ListAPIKeysByTenant(ctx context.Context, tenant string) ([]*APIKey, error)
	UpdateAPIKey(ctx context.Context, k *APIKey) error
	DeleteAPIKey(ctx context.Context, id string) error
	// HasAnyAPIKey returns true when at least one enabled API key exists in the
	// store. Used by rbacMiddleware to detect a bootstrapped (non-dev) deployment
	// and enforce fail-closed authentication when no Bearer token is presented.
	HasAnyAPIKey(ctx context.Context) (bool, error)

	// --- Service Accounts (OAuth2 client_credentials machine auth) ---------
	// CreateServiceAccount generates a client_id and client_secret, stores only
	// the SHA-256 hex hash of the secret, and returns the plaintext secret
	// exactly once. The caller must present secret to POST /auth/token to obtain
	// a short-lived JWT.
	CreateServiceAccount(ctx context.Context, sa *ServiceAccount) (clientSecret string, err error)
	// GetServiceAccountByClientID returns the enabled, non-expired service
	// account with the given client_id. Returns ErrNotFound when no match.
	GetServiceAccountByClientID(ctx context.Context, clientID string) (*ServiceAccount, error)
	// ListServiceAccounts returns all service accounts. When tenant is non-empty,
	// only accounts belonging to that tenant are returned.
	ListServiceAccounts(ctx context.Context, tenant string) ([]*ServiceAccount, error)
	// RevokeServiceAccount soft-deletes a service account (sets enabled=0).
	// Returns ErrNotFound when no account with id exists.
	RevokeServiceAccount(ctx context.Context, id string) error
	// UpdateServiceAccountLastUsed updates the last_used_at timestamp for the
	// given account. Callers should throttle this to avoid write amplification
	// on the token-issuance hot-path.
	UpdateServiceAccountLastUsed(ctx context.Context, id string, at time.Time) error

	// --- Certs (internal PKI) ----------------------------------------------
	CreateCert(ctx context.Context, c *Cert) error
	GetCert(ctx context.Context, serial string) (*Cert, error)
	ListCerts(ctx context.Context) ([]*Cert, error)
	UpdateCert(ctx context.Context, c *Cert) error
	DeleteCert(ctx context.Context, serial string) error

	// --- Audit log ---------------------------------------------------------
	// AppendAudit appends one entry; the assigned ID is written back into e.
	AppendAudit(ctx context.Context, e *AuditEntry) error
	// ListAudit returns the most recent entries, newest first, capped at
	// limit (limit <= 0 means "a sane default").
	ListAudit(ctx context.Context, limit int) ([]*AuditEntry, error)

	// --- Deployment approval queue (AI Act Art.14 human oversight) --------
	// RequestDeploymentApproval inserts a new approval record with status
	// "pending". The ID is written back into approval.
	RequestDeploymentApproval(ctx context.Context, approval *DeploymentApproval) error
	// GetDeploymentApproval returns the approval record for deploymentID.
	// Returns ErrNotFound when no matching record exists.
	GetDeploymentApproval(ctx context.Context, deploymentID string) (*DeploymentApproval, error)
	// ListDeploymentApprovals returns approval records, optionally filtered by
	// status ("pending", "approved", "rejected", or "" for all).
	// Results are newest-first, capped at limit (limit <= 0 → default 50).
	ListDeploymentApprovals(ctx context.Context, status string, limit int) ([]*DeploymentApproval, error)
	// UpdateDeploymentApprovalStatus transitions the approval for deploymentID
	// to the given status, recording the reviewer and optional notes.
	UpdateDeploymentApprovalStatus(ctx context.Context, deploymentID, status, reviewer, notes string) error
	// RecordApprovalVote records a single reviewer's vote on an approval.
	// Returns an error when:
	//   - the reviewer is the same as the approval's requester (self-approval),
	//   - the reviewer has already voted on this approval (duplicate vote), or
	//   - the approval is no longer pending.
	RecordApprovalVote(ctx context.Context, deploymentID, reviewer, vote, notes, ipAddress string) error
	// GetApprovalVotes returns all votes cast for the approval identified by
	// approvalID, ordered by voted_at ascending.
	GetApprovalVotes(ctx context.Context, approvalID int64) ([]ApprovalVote, error)
	// CheckApprovalQuorum reports whether the quorum for deploymentID has been
	// reached: (reached, approvedCount, requiredCount, error).
	CheckApprovalQuorum(ctx context.Context, deploymentID string) (bool, int, int, error)

	// --- Usage log ---------------------------------------------------------
	// RecordUsage records one inference request's token usage.
	RecordUsage(ctx context.Context, apiKeyID, modelID string, inputTokens, outputTokens int64) error
	// GetKeyUsage returns aggregate token usage for a single API key.
	GetKeyUsage(ctx context.Context, apiKeyID string) (*KeyUsageSummary, error)
	// GetUsageSummary returns usage grouped by tenant since the given time.
	// A zero since means "all time".
	GetUsageSummary(ctx context.Context, since time.Time) ([]TenantUsage, error)
	// GetBillingReport returns a chargeback report for the given time window,
	// optionally filtered to a single tenant. An empty tenantID means all tenants.
	GetBillingReport(ctx context.Context, start, end time.Time, tenantID string) (*BillingReport, error)

	// --- Inference audit log (AI Act Art.12) --------------------------------
	// RecordInferenceEvent appends an inference event to the audit log.
	// The operation is idempotent: a duplicate RequestID is silently ignored.
	// A nil event is a no-op and returns nil.
	RecordInferenceEvent(ctx context.Context, event *InferenceEvent) error
	// ListInferenceEvents returns paginated inference audit events matching
	// the filter described in req. Unset filter fields are ignored. The
	// default limit is 100; the maximum is 1000.
	ListInferenceEvents(ctx context.Context, req *ListInferenceEventsRequest) (*ListInferenceEventsResponse, error)
	// VerifyInferenceChain walks the inference_audit_log in seq order and
	// checks that every entry's hash is consistent with its content and the
	// preceding entry's hash. It returns the number of chained rows examined
	// (length), whether the chain is intact (verified), and the seq of the
	// first broken entry (breakSeq — -1 when verified=true). A non-nil error
	// indicates a database or scan failure rather than a chain integrity problem.
	VerifyInferenceChain(ctx context.Context) (length int64, verified bool, breakSeq int64, err error)

	// --- Policies (OPA/Rego) -----------------------------------------------
	// UpsertPolicy inserts or replaces a policy by name. The updated_at
	// timestamp is set by the implementation.
	UpsertPolicy(ctx context.Context, p *Policy) error
	// GetPolicy returns the policy with the given name, or ErrNotFound.
	GetPolicy(ctx context.Context, name string) (*Policy, error)
	// ListPolicies returns all stored policies (enabled and disabled).
	ListPolicies(ctx context.Context) ([]*Policy, error)
	// DeletePolicy removes the policy with the given name. Returns ErrNotFound
	// when no such policy exists.
	DeletePolicy(ctx context.Context, name string) error

	// ==========================================================================
	// Enterprise Wave B methods
	// ==========================================================================

	// --- OIDC Session Store (distributed, HA-safe) ----------------------------
	// CreateOIDCSession persists a new browser session created via OIDC or LDAP
	// login. Returns an error if a session with the same token_hash already exists.
	CreateOIDCSession(ctx context.Context, s *OIDCSession) error
	// GetOIDCSession returns the session whose token_hash matches, or ErrNotFound.
	GetOIDCSession(ctx context.Context, tokenHash string) (*OIDCSession, error)
	// RevokeOIDCSessionsBySubject marks all non-expired sessions for sub as
	// revoked. Returns the number of rows updated.
	RevokeOIDCSessionsBySubject(ctx context.Context, sub string) (int, error)
	// RevokeOIDCSessionByTokenHash revokes the single session identified by
	// tokenHash. Returns ErrNotFound when the session does not exist.
	RevokeOIDCSessionByTokenHash(ctx context.Context, tokenHash string) error
	// DeleteExpiredOIDCSessions removes all sessions whose expires_at is in the
	// past. Returns the number of rows deleted.
	DeleteExpiredOIDCSessions(ctx context.Context) (int, error)

	// --- PKCE State (distributed, consume-once) --------------------------------
	// SetPKCEState stores a PKCE state/verifier pair with the given TTL. The
	// stateHash is SHA-256 of the OAuth2 state parameter.
	SetPKCEState(ctx context.Context, stateHash, verifier string, ttl time.Duration) error
	// ConsumePKCEState atomically retrieves and deletes the verifier for
	// stateHash. ok is false when the state is not found or has expired.
	ConsumePKCEState(ctx context.Context, stateHash string) (verifier string, ok bool, err error)
	// DeleteExpiredPKCEStates removes all rows whose expires_at is in the past.
	// Returns the number of rows deleted.
	DeleteExpiredPKCEStates(ctx context.Context) (int, error)

	// --- API Key Lifecycle -----------------------------------------------------
	// RotateAPIKey atomically marks oldID as rotated (sets rotated_at, enabled=0)
	// and inserts newKey with predecessor_id = oldID. The operation is
	// transactional: either both changes land or neither does.
	RotateAPIKey(ctx context.Context, oldID string, newKey *APIKey) error
	// UpdateAPIKeyLastUsed updates the last_used_at timestamp for the given key.
	// Callers should throttle this to at most once per 5 minutes to limit write
	// amplification on the auth hot-path.
	UpdateAPIKeyLastUsed(ctx context.Context, keyID string, at time.Time) error
	// ListAPIKeysExpiringBefore returns all enabled keys whose expires_at is
	// before the given timestamp (used by the expiry-notification background job).
	ListAPIKeysExpiringBefore(ctx context.Context, before time.Time) ([]*APIKey, error)
	// RecordAPIKeyAccess appends one row to api_key_access_log.
	RecordAPIKeyAccess(ctx context.Context, entry *APIKeyAccessEntry) error
	// HasAnyAPIKey: see declaration above (deduped).

	// --- Model Pricing ---------------------------------------------------------
	// UpsertModelPricing inserts or replaces the pricing row for
	// (model_id, effective_from).
	UpsertModelPricing(ctx context.Context, p *ModelPricing) error
	// GetCurrentModelPricing returns the most-recently effective pricing for
	// modelID, or ErrNotFound when no pricing has been configured.
	GetCurrentModelPricing(ctx context.Context, modelID string) (*ModelPricing, error)

	// --- Tenant Quota ----------------------------------------------------------
	// UpsertTenantQuota inserts or replaces the quota configuration for a tenant.
	UpsertTenantQuota(ctx context.Context, q *TenantQuota) error
	// GetTenantQuota returns the quota configuration for tenantID, or ErrNotFound.
	GetTenantQuota(ctx context.Context, tenantID string) (*TenantQuota, error)
	// IncrementTenantUsage atomically increments the usage counters for tenantID
	// in the given billing period. An upsert is used so the row is created on
	// first use.
	IncrementTenantUsage(ctx context.Context, tenantID string, period time.Time, delta UsageDelta) error
	// GetTenantUsage returns the accumulated usage for tenantID in the given
	// billing period, or ErrNotFound when no usage has been recorded yet.
	GetTenantUsage(ctx context.Context, tenantID string, period time.Time) (*TenantQuotaUsage, error)

	// --- Policy Versioning -----------------------------------------------------
	// GetPolicyVersions returns up to limit historical versions of policyName,
	// newest first. limit <= 0 means "a sane default" (e.g. 50).
	GetPolicyVersions(ctx context.Context, policyName string, limit int) ([]*PolicyVersion, error)
	// SavePolicyVersion appends a new versioned snapshot of a policy's Rego
	// source. Version numbers are assigned by the caller.
	SavePolicyVersion(ctx context.Context, pv *PolicyVersion) error

	// --- GDPR ------------------------------------------------------------------
	// RecordGDPRErasure appends one erasure record to gdpr_erasure_log.
	RecordGDPRErasure(ctx context.Context, log *GDPRErasureLog) error
	// EraseInferenceEventsBySubject replaces identifying fields in
	// inference_audit_log rows whose api_key_hash matches subjectHash with
	// empty/zero values. Returns the number of rows affected.
	EraseInferenceEventsBySubject(ctx context.Context, subjectHash string) (int64, error)
}
