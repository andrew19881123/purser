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
	UpdateAPIKey(ctx context.Context, k *APIKey) error
	DeleteAPIKey(ctx context.Context, id string) error

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
}
