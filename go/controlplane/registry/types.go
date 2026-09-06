// Package registry is the persistent source of truth for the Purser control
// plane. It stores the state of the whole fleet — nodes, links, models, plans,
// deployments, API keys, sessions, the audit log and the internal PKI — behind
// a storage-agnostic [Registry] interface so the backend can be swapped
// (SQLite for single-node MVP, a replicated store for HA later).
//
// The domain types defined here are deliberately storage-friendly: rich,
// nested structures (hardware profiles, model specs, deployment plans) are
// carried as opaque JSON blobs ([json.RawMessage]) so the schema stays flat
// and the generated protobuf types remain the single source of shape.
package registry

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned by Get/Update/Delete operations when the requested
// entity does not exist.
var ErrNotFound = errors.New("registry: not found")

// Node is a single enrolled machine in the fleet. The full, evolving hardware
// and liveness detail lives in HardwareProfile (a JSON-encoded
// purserv1.HardwareProfile); the promoted columns exist for cheap querying and
// indexing.
type Node struct {
	ID       string  `json:"id"`
	Hostname string  `json:"hostname"`
	OS       string  `json:"os"`
	Arch     string  `json:"arch"`
	RAMGB    float64 `json:"ram_gb"`
	VRAMGB   float64 `json:"vram_gb"`
	// State is the lifecycle state (e.g. NODE_STATE_READY).
	State string `json:"state"`
	// AdvertisedAgentAddr is the "host:port" of this node's AgentService as the
	// agent advertised it at Join time. Empty when the agent did not advertise
	// one, in which case callers fall back to the hostname + well-known-port
	// convention. Promoted to its own column so the orchestrator's resolver can
	// read it without decoding the hardware profile.
	AdvertisedAgentAddr string `json:"advertised_agent_addr,omitempty"`
	// AdvertisedInferenceAddr is the "host:port" where this node serves the
	// OpenAI-compatible inference API, as advertised at Join time. Empty when not
	// advertised (fall back to hostname + well-known inference port).
	AdvertisedInferenceAddr string `json:"advertised_inference_addr,omitempty"`
	// LastSeen is the timestamp of the most recent heartbeat; zero if never.
	LastSeen time.Time `json:"last_seen"`
	// HardwareProfile is the full purserv1.HardwareProfile encoded as JSON.
	HardwareProfile json.RawMessage `json:"hardware_profile,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// Link is a measured network edge between two nodes (from the connectivity
// benchmarks the planner consumes).
type Link struct {
	FromNode     string    `json:"from_node"`
	ToNode       string    `json:"to_node"`
	BandwidthGBs float64   `json:"bandwidth_gbs"`
	RTTMs        float64   `json:"rtt_ms"`
	MeasuredAt   time.Time `json:"measured_at"`
}

// Model is a catalog entry. Spec carries the full purserv1.ModelSpec as JSON
// (architecture, quantizations, draft info, ...).
//
// Convention for the Type field:
//
//   - "llm"       — autoregressive language model (chat/completions endpoints).
//   - "embedding" — encoder model served via the /v1/embeddings endpoint.
//
// The default is "llm". The Planner treats all models identically regardless of
// Type (it is informational only at this stage); the field exists so the catalog
// and the UI can label and filter models by their primary capability.
//
// Source carries the import provenance (HuggingFace Hub, s3://, gs://, az://) as
// a JSON blob; it is omitted for models registered directly via POST /api/v1/models.
// The agent reads DownloadURL from Source to fetch the model weights at deploy time.
type Model struct {
	ID           string  `json:"id"`
	Family       string  `json:"family"`
	Architecture string  `json:"architecture"`
	ParamsTotalB float64 `json:"params_total_b"`
	Engine       string  `json:"engine"`
	// Type distinguishes the model's primary serving mode: "llm" or "embedding".
	// Defaults to "llm" when not supplied by the caller.
	Type string          `json:"type,omitempty"`
	Spec json.RawMessage `json:"spec,omitempty"`
	// Source is the import provenance stored as an opaque JSON blob. The shape
	// depends on the import source type (e.g. {"type":"huggingface","repo":"..."} or
	// {"type":"s3","bucket":"...","download_url":"..."}).
	Source    json.RawMessage `json:"source,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Plan is a DeploymentPlan produced by the planner. Plan carries the full
// purserv1.DeploymentPlan as JSON (assignments, pipeline order, estimates).
type Plan struct {
	ID           string          `json:"id"`
	ModelID      string          `json:"model_id"`
	Quantization string          `json:"quantization"`
	Cost         float64         `json:"cost"`
	Plan         json.RawMessage `json:"plan,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

// Deployment is a (possibly active) instantiation of a plan for a model.
type Deployment struct {
	ID        string          `json:"id"`
	ModelID   string          `json:"model_id"`
	PlanID    string          `json:"plan_id"`
	State     string          `json:"state"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// APIKey is a gateway credential. Only a hash of the key is ever persisted.
type APIKey struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	KeyHash string `json:"-"`
	Tenant  string `json:"tenant"`
	// Role controls what the key may do: "admin" (full CP access), "viewer"
	// (GET-only on /api/v1), or "inference" (gateway /v1 only — cannot call
	// the CP management surface directly). Defaults to "admin" so keys created
	// before RBAC was introduced retain full access.
	Role      string    `json:"role"`
	Quota     int64     `json:"quota"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Session records an inference session for metrics/attribution.
type Session struct {
	ID        string          `json:"id"`
	APIKeyID  string          `json:"api_key_id"`
	ModelID   string          `json:"model_id"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// AuditEntry is one row of the append-only administrative audit log
// (who-did-what-when). ID is assigned by the store.
//
// Seq, PrevHash and Hash are the tamper-evident hash-chain fields assigned by
// AppendAudit (see the audit package). Seq is the 1-based, gap-free position in
// the chain; PrevHash links to the preceding entry's Hash; Hash is this entry's
// chain digest. Rows written before the hash chain existed carry the zero
// values (Seq==0) and are not part of the verifiable chain.
type AuditEntry struct {
	ID        int64           `json:"id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Details   json.RawMessage `json:"details,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Seq       uint64          `json:"seq"`
	PrevHash  string          `json:"prev_hash"`
	Hash      string          `json:"hash"`
}

// KeyUsageSummary is the aggregate token usage for a single API key.
type KeyUsageSummary struct {
	APIKeyID      string `json:"api_key_id"`
	TotalRequests int64  `json:"total_requests"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
}

// TenantUsage is aggregate token usage grouped by tenant, returned by
// GetUsageSummary.
type TenantUsage struct {
	Tenant        string `json:"tenant"`
	TotalRequests int64  `json:"total_requests"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
}

// DeploymentApproval is one row of the human-oversight approval queue
// (AI Act Art.14). When the "deployment_approvals" enterprise feature is
// enabled, every deploy request creates a pending record here; the real
// rollout is held until an admin approves via the REST API.
type DeploymentApproval struct {
	ID           int64      `json:"id"`
	DeploymentID string     `json:"deployment_id"`
	ModelID      string     `json:"model_id"`
	Requester    string     `json:"requester"` // api_key_hash
	RequestedAt  time.Time  `json:"requested_at"`
	Status       string     `json:"status"` // "pending" | "approved" | "rejected"
	Reviewer     string     `json:"reviewer,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	Notes        string     `json:"notes,omitempty"`
}

// BillingTenantUsage aggregates inference activity for a single tenant+model
// pair inside a billing window. It is the row type inside BillingReport.
type BillingTenantUsage struct {
	TenantID         string    `json:"tenant_id"`
	ModelID          string    `json:"model_id"`
	RequestCount     int64     `json:"request_count"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	AvgLatencyMs     float64   `json:"avg_latency_ms"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
}

// BillingReport is the full chargeback report for a configurable time window.
// It is returned by GetBillingReport and served by GET /api/v1/billing/report.
type BillingReport struct {
	PeriodStart   time.Time            `json:"period_start"`
	PeriodEnd     time.Time            `json:"period_end"`
	Tenants       []BillingTenantUsage `json:"tenants"`
	TotalRequests int64                `json:"total_requests"`
	TotalTokens   int64                `json:"total_tokens"`
}

// Cert tracks a certificate issued by the internal CA (see package pki).
type Cert struct {
	Serial    string    `json:"serial"`
	Subject   string    `json:"subject"`
	Role      string    `json:"role"`
	PEM       string    `json:"-"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

// InferenceEvent is one row of the append-only inference audit log.
// It satisfies AI Act Art.12: who requested what, when, using which model.
// Prompt content is NEVER stored (GDPR Article 5 data minimisation).
// RequestId carries a gateway-generated UUID v4; INSERT OR IGNORE on it
// makes RecordInferenceEvent idempotent against duplicate submissions.
type InferenceEvent struct {
	ID               int64     `json:"id"`
	RequestID        string    `json:"request_id"`
	APIKeyHash       string    `json:"api_key_hash"`
	ModelID          string    `json:"model_id"`
	TenantID         string    `json:"tenant_id"`
	Timestamp        time.Time `json:"timestamp"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	// Endpoint is the inference protocol used: "openai", "anthropic", or "embeddings".
	Endpoint string `json:"endpoint"`
	// ClientIPPrefix is the CIDR /24 prefix of the caller — the full IP is
	// never stored (GDPR data minimisation).
	ClientIPPrefix string  `json:"client_ip_prefix"`
	LatencyMs      float64 `json:"latency_ms"`
	// FinishReason is "stop", "length", or "error".
	FinishReason string `json:"finish_reason"`

	// AI Act Art.12(1)(a): version tracking. Empty for pre-feature events.
	ModelRevision     string `json:"model_revision,omitempty"`
	ModelQuantization string `json:"model_quantization,omitempty"`
	NodeID            string `json:"node_id,omitempty"`
	InferenceEngine   string `json:"inference_engine,omitempty"`
}

// ListInferenceEventsRequest is the filter and pagination spec for
// ListInferenceEvents. All filter fields are optional (zero value = no filter).
type ListInferenceEventsRequest struct {
	APIKeyHash string    `json:"api_key_hash"` // filter by key hash
	ModelID    string    `json:"model_id"`     // filter by model
	TenantID   string    `json:"tenant_id"`    // filter by tenant
	After      time.Time `json:"after"`        // exclusive lower bound on timestamp
	Before     time.Time `json:"before"`       // exclusive upper bound on timestamp
	Limit      int32     `json:"limit"`        // default 100, max 1000
	PageToken  string    `json:"page_token"`   // cursor (opaque, decimal row id)
}

// ListInferenceEventsResponse is the paged result of ListInferenceEvents.
type ListInferenceEventsResponse struct {
	Events        []*InferenceEvent `json:"events"`
	NextPageToken string            `json:"next_page_token,omitempty"`
}

// Policy is a Rego policy document stored in the registry and evaluated by the
// embedded OPA engine. Only enabled policies are loaded into the engine; the
// `name` field serves as the human-readable identifier and upsert key.
type Policy struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Rego      string    `json:"rego"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
