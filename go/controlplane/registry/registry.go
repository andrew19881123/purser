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

	// --- Usage log ---------------------------------------------------------
	// RecordUsage records one inference request's token usage.
	RecordUsage(ctx context.Context, apiKeyID, modelID string, inputTokens, outputTokens int64) error
	// GetKeyUsage returns aggregate token usage for a single API key.
	GetKeyUsage(ctx context.Context, apiKeyID string) (*KeyUsageSummary, error)
	// GetUsageSummary returns usage grouped by tenant since the given time.
	// A zero since means "all time".
	GetUsageSummary(ctx context.Context, since time.Time) ([]TenantUsage, error)
}
