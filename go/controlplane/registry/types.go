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

// ErrConflict is returned by Create operations when a unique constraint is
// violated (e.g. duplicate (org_id, name) on a custom role).
var ErrConflict = errors.New("registry: conflict")

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
	// Enterprise lifecycle fields (Wave B).
	// ExpiresAt is nil when the key never expires.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// LastUsedAt is nil when the key has never been used. Updates are throttled
	// (at most once per 5 minutes) to minimise write amplification on the hot
	// auth path.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// PredecessorID links this key to the key it replaced (rotation chain).
	// Empty string when this key was not created via RotateAPIKey.
	PredecessorID string `json:"predecessor_id,omitempty"`
	// RotatedAt is set when this key has been superseded by a successor.
	// Nil means the key is still active (or was deleted rather than rotated).
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
	// Scopes is a JSON-backed list of fine-grained permission strings.
	// An empty slice means the key's permissions are governed by Role alone.
	Scopes []string `json:"scopes,omitempty"`
	// CreatedBy is the actor (OIDC sub or API key fingerprint) who created this
	// key. Empty for keys created before this field was introduced (v0.5).
	CreatedBy string `json:"created_by,omitempty"`
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

// ApprovalVote represents a single reviewer's vote on a deployment approval
// (AI Act Art.14 dual-control). Each reviewer casts exactly one vote per
// approval; a unique index on (approval_id, reviewer) prevents duplicates.
type ApprovalVote struct {
	ID         int64     `json:"id"`
	ApprovalID int64     `json:"approval_id"`
	Reviewer   string    `json:"reviewer"`
	VotedAt    time.Time `json:"voted_at"`
	Vote       string    `json:"vote"` // "approved" | "rejected"
	Notes      string    `json:"notes,omitempty"`
	IPAddress  string    `json:"ip_address,omitempty"`
}

// DeploymentApproval is one row of the human-oversight approval queue
// (AI Act Art.14). When the "deployment_approvals" enterprise feature is
// enabled, every deploy request creates a pending record here; the real
// rollout is held until an admin approves via the REST API.
//
// RequiredApprovals controls how many distinct admins must vote "approved"
// before the deployment is released (default 1; set 2 for dual control).
// ExpiresAt, if non-nil, is the UTC deadline after which the approval
// request is no longer actionable (handlers return 410 Gone).
// Votes carries the individual votes when loaded by GetApprovalVotes.
type DeploymentApproval struct {
	ID                int64          `json:"id"`
	DeploymentID      string         `json:"deployment_id"`
	ModelID           string         `json:"model_id"`
	Requester         string         `json:"requester"` // api_key_hash
	RequestedAt       time.Time      `json:"requested_at"`
	Status            string         `json:"status"` // "pending" | "approved" | "rejected"
	Reviewer          string         `json:"reviewer,omitempty"`
	ReviewedAt        *time.Time     `json:"reviewed_at,omitempty"`
	Notes             string         `json:"notes,omitempty"`
	RequiredApprovals int            `json:"required_approvals"`
	ExpiresAt         *time.Time     `json:"expires_at,omitempty"`
	Votes             []ApprovalVote `json:"votes,omitempty"`
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

// =============================================================================
// Enterprise Wave B types
// =============================================================================

// OIDCSession represents a persisted browser session created via OIDC or LDAP
// login. Stored in the oidc_sessions table for HA support (cross-node session
// sharing). TokenHash and RefreshTokenEnc are never exposed in JSON responses.
type OIDCSession struct {
	TokenHash         string     `json:"-"` // SHA-256 hex of the cookie value
	Sub               string     `json:"sub"`
	Email             string     `json:"email"`
	IDPIssuer         string     `json:"idp_issuer"`
	AuthMethod        string     `json:"auth_method"` // "oidc" | "ldap" | "service_account"
	CreatedAt         time.Time  `json:"created_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	Revoked           bool       `json:"revoked"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	RefreshTokenEnc   string     `json:"-"` // AES-256-GCM encrypted; never in JSON
	AccessTokenExpiry *time.Time `json:"access_token_expiry,omitempty"`
}

// PKCEState is a consume-once PKCE code verifier stored during the OIDC auth
// flow. StateHash and Verifier are never exposed in JSON responses.
type PKCEState struct {
	StateHash string    `json:"-"` // SHA-256 of the OAuth2 state parameter
	Verifier  string    `json:"-"` // code_verifier; deleted on consumption
	ExpiresAt time.Time `json:"expires_at"`
}

// APIKeyAccessEntry records a single authenticated API request for audit and
// anomaly detection. IPPrefix stores only the /24 CIDR prefix (GDPR Art.5
// data minimisation — the full client IP is never persisted).
type APIKeyAccessEntry struct {
	ID         int64     `json:"id"`
	APIKeyID   string    `json:"api_key_id"`
	KeyHash    string    `json:"-"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	IPPrefix   string    `json:"ip_prefix"` // /24 CIDR
	UserAgent  string    `json:"user_agent"`
	StatusCode int       `json:"status_code"`
	RequestAt  time.Time `json:"request_at"`
}

// ModelPricing defines the cost schedule for a model, effective from a given
// timestamp. The most-recent row per model wins. Tiers applies volume
// discounts above token thresholds.
type ModelPricing struct {
	ModelID          string        `json:"model_id"`
	EffectiveFrom    time.Time     `json:"effective_from"`
	InputPricePer1K  float64       `json:"input_price_per_1k"`  // USD per 1000 input tokens
	OutputPricePer1K float64       `json:"output_price_per_1k"` // USD per 1000 output tokens
	Tiers            []PricingTier `json:"tiers,omitempty"`
}

// PricingTier defines a volume discount threshold. Above ThresholdTokens the
// effective price is multiplied by Multiplier (e.g. 0.8 = 20% discount).
type PricingTier struct {
	ThresholdTokens int64   `json:"threshold_tokens"`
	Multiplier      float64 `json:"multiplier"`
}

// TenantQuota configures multi-dimensional usage limits for a tenant.
// A zero value for any counter means unlimited for that dimension.
// InheritFromParent propagates limits down from a parent tenant hierarchy.
type TenantQuota struct {
	TenantID            string    `json:"tenant_id"`
	MonthlyRequests     int64     `json:"monthly_requests"`
	MaxConcurrent       int32     `json:"max_concurrent"`
	RequestsPerSec      float64   `json:"requests_per_sec"`
	InputTPM            int64     `json:"input_tpm"`  // input tokens/min
	OutputTPM           int64     `json:"output_tpm"` // output tokens/min
	MonthlyInputTokens  int64     `json:"monthly_input_tokens"`
	MonthlyOutputTokens int64     `json:"monthly_output_tokens"`
	MonthlyCostBudget   float64   `json:"monthly_cost_budget"` // USD; 0 = unlimited
	AlertAtPercent      int       `json:"alert_at_percent"`
	InheritFromParent   bool      `json:"inherit_from_parent"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// TenantQuotaUsage accumulates usage for a tenant within a billing period.
// PeriodStart is the first second of the billing month (UTC).
type TenantQuotaUsage struct {
	TenantID         string    `json:"tenant_id"`
	PeriodStart      time.Time `json:"period_start"`
	RequestsUsed     int64     `json:"requests_used"`
	InputTokensUsed  int64     `json:"input_tokens_used"`
	OutputTokensUsed int64     `json:"output_tokens_used"`
	CostUsedUSD      float64   `json:"cost_used_usd"`
	LastUpdated      time.Time `json:"last_updated"`
}

// UsageDelta is an atomic increment applied to TenantQuotaUsage by
// IncrementTenantUsage. Counters are applied with an atomic upsert so
// concurrent increments from multiple gateway nodes do not race.
type UsageDelta struct {
	Requests     int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// PolicyVersion records the full source history of a Rego policy.
// A new row is appended on every PUT; the highest Version per PolicyName is
// the currently active revision.
type PolicyVersion struct {
	ID         int64     `json:"id"`
	PolicyName string    `json:"policy_name"`
	Version    int       `json:"version"`
	Rego       string    `json:"rego"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by"`
}

// TeamBillingReport is the billing aggregated for a team.
// The team is identified by its tenant_id (the Tenant field of its API keys).
// OrgID is set when the report is produced as part of an OrgBillingReport; it
// is empty when the team report is requested directly via the team endpoint.
// ByModel holds one BillingTenantUsage row per distinct model used by the team.
type TeamBillingReport struct {
	TeamID        string               `json:"team_id"`
	TeamName      string               `json:"team_name,omitempty"`
	OrgID         string               `json:"org_id,omitempty"`
	PeriodStart   time.Time            `json:"period_start"`
	PeriodEnd     time.Time            `json:"period_end"`
	TotalRequests int64                `json:"total_requests"`
	InputTokens   int64                `json:"input_tokens"`
	OutputTokens  int64                `json:"output_tokens"`
	TotalTokens   int64                `json:"total_tokens"`
	TotalCostUSD  float64              `json:"total_cost_usd"`
	ByModel       []BillingTenantUsage `json:"by_model,omitempty"`
}

// OrgBillingReport is the billing aggregated for an entire organization.
// Teams are discovered from inference_audit_log using the naming convention
// "<orgID>/<teamSlug>" for tenant_id values; the org report sums all team totals.
type OrgBillingReport struct {
	OrgID        string              `json:"org_id"`
	OrgName      string              `json:"org_name,omitempty"`
	PeriodStart  time.Time           `json:"period_start"`
	PeriodEnd    time.Time           `json:"period_end"`
	TotalCostUSD float64             `json:"total_cost_usd"`
	TotalTokens  int64               `json:"total_tokens"`
	Teams        []TeamBillingReport `json:"teams"`
}

// GDPRErasureLog records one GDPR Art.17 right-to-erasure operation.
// SubjectHash is SHA-256 of the subject identifier so the log itself holds
// no PII. ErasureType identifies which table was scrubbed (e.g.
// "inference_audit").
type GDPRErasureLog struct {
	ID           int64     `json:"id"`
	SubjectHash  string    `json:"subject_hash"`
	ErasedAt     time.Time `json:"erased_at"`
	ErasedBy     string    `json:"erased_by"`
	Reason       string    `json:"reason"`
	EventsErased int64     `json:"events_erased"`
	ErasureType  string    `json:"erasure_type"`
}

// ServiceAccount is a machine identity for CI/CD and automation use cases.
// It participates in the OAuth2 client_credentials grant: the client_secret
// is exchanged for a short-lived (15 min) HMAC-signed JWT via POST /auth/token
// so the secret never travels on subsequent API requests.
// ClientSecretHash is never exposed in JSON responses (json:"-").
//
// Tenant holds the team_id to which this service account belongs. Service
// accounts are team-level credentials — they are not associated with an
// individual user. This aligns with LiteLLM and proxy auth patterns where
// a single service account provides machine-to-machine access for a team.
type ServiceAccount struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Tenant           string     `json:"tenant"` // team_id (stored as tenant for routing compat)
	Description      string     `json:"description,omitempty"`
	Role             string     `json:"role"`
	Scopes           []string   `json:"scopes,omitempty"`
	ClientID         string     `json:"client_id"`
	ClientSecretHash string     `json:"-"`
	Enabled          bool       `json:"enabled"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// =============================================================================
// Platform multi-tenant types (v0.4)
// =============================================================================

// Organization is a top-level tenant on the Purser platform.
// It includes a slug for human-friendly URL segments and is managed via the
// full org CRUD API (POST/GET/PUT/DELETE /api/v1/platform/orgs).
type Organization struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PlatformOrg is a lightweight org record used by the roles/users subsystem.
// It is created automatically (via UpsertPlatformOrg) when org-scoped role
// endpoints are first called. Wave 3 will unify this with Organization.
type PlatformOrg struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Team is a sub-unit of an Organization.
type Team struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PlatformTeam is a lightweight team record used by the roles/permissions
// subsystem (cross-referenced by CustomRole and TeamMember).
// Wave 3 will unify this with Team.
type PlatformTeam struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PlatformUser is a user identity on the Purser platform.
type PlatformUser struct {
	ID          string     `json:"id"` // OIDC sub or LDAP DN
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name,omitempty"`
	AuthMethod  string     `json:"auth_method"` // "oidc" | "ldap"
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

// OrgMember links a user to an organization with a role.
//
// UserSub is the canonical stable identity string ("oidc:<sub>" or
// "apikey:<hash8>"). UserID is a read-alias that contains the same value and
// is retained for backward-compatibility with v0.4 org handlers.
// JoinedAt is the canonical timestamp; CreatedAt mirrors it for compat.
type OrgMember struct {
	OrgID     string    `json:"org_id"`
	UserSub   string    `json:"user_sub"`          // canonical identity
	UserID    string    `json:"user_id,omitempty"` // alias for UserSub (compat)
	Role      string    `json:"role"`              // "org_admin" | "member"
	InvitedBy string    `json:"invited_by,omitempty"`
	JoinedAt  time.Time `json:"joined_at"`
	CreatedAt time.Time `json:"created_at,omitempty"` // alias for JoinedAt (compat)
}

// TeamMember links a user to a team with a custom role.
//
// UserSub is canonical; UserID is a backward-compat alias. JoinedAt is
// canonical; CreatedAt mirrors it.
type TeamMember struct {
	TeamID    string    `json:"team_id"`
	UserSub   string    `json:"user_sub"`          // canonical identity
	UserID    string    `json:"user_id,omitempty"` // alias for UserSub (compat)
	RoleID    string    `json:"role_id"`
	InvitedBy string    `json:"invited_by,omitempty"`
	JoinedAt  time.Time `json:"joined_at"`
	CreatedAt time.Time `json:"created_at,omitempty"` // alias for JoinedAt (compat)
	// Resolved fields (not stored, populated on read)
	User *PlatformUser `json:"user,omitempty"`
	Role *CustomRole   `json:"role,omitempty"`
}

// CustomRole defines a named set of permission strings for an organization.
// System roles (IsSystem=true) are seeded at startup and cannot be modified
// or deleted. Custom roles are created by org_admin users.
type CustomRole struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id,omitempty"` // empty = platform built-in
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"` // e.g. ["team:models:deploy"]
	IsSystem    bool      `json:"is_system"`   // platform built-ins cannot be deleted
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NodePool is a named group of GPU nodes with an access policy.
type NodePool struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	OwnerType   string    `json:"owner_type"` // "platform" | "org" | "team"
	OwnerID     string    `json:"owner_id"`   // org_id or team_id
	Policy      string    `json:"policy"`     // "exclusive" | "shared"
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Resolved: nodes in this pool
	NodeIDs []string `json:"node_ids,omitempty"`
}

// PoolTeamQuota defines per-team limits on a shared pool.
type PoolTeamQuota struct {
	PoolID         string    `json:"pool_id"`
	TeamID         string    `json:"team_id"`
	MaxDeployments int       `json:"max_deployments"` // 0 = unlimited
	MaxGPUNodes    int       `json:"max_gpu_nodes"`   // 0 = unlimited
	Priority       int       `json:"priority"`        // lower = higher precedence
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// EffectivePermissions is the resolved permission set for a user in a team
// context. It merges fields from both the org-CRUD subsystem (UserID,
// OrgID, IsOrgAdmin) and the roles subsystem (UserSub, RoleID, RoleName).
type EffectivePermissions struct {
	TeamID      string   `json:"team_id"`
	OrgID       string   `json:"org_id,omitempty"`
	UserID      string   `json:"user_id,omitempty"`  // compat alias
	UserSub     string   `json:"user_sub,omitempty"` // canonical
	RoleID      string   `json:"role_id,omitempty"`
	RoleName    string   `json:"role_name,omitempty"`
	Permissions []string `json:"permissions"`
	IsOrgAdmin  bool     `json:"is_org_admin,omitempty"`
}

// =============================================================================
// Data Plane types (v0.5 CP/DP architectural separation)
// =============================================================================

// DataPlane represents a named inference cluster registered to this Control Plane.
// Analogous to a "Gateway Service" in IBM API Connect.
type DataPlane struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	Tier           string         `json:"tier"`        // "production" | "development" | etc.
	GatewayURL     string         `json:"gateway_url"` // endpoint clients use for inference
	Status         string         `json:"status"`      // "active" | "registering" | "degraded" | "offline"
	JoinTokenHash  string         `json:"-"`           // not exposed in API responses
	ConfigSnapshot map[string]any `json:"config_snapshot,omitempty"`
	LastHeartbeat  *time.Time     `json:"last_heartbeat,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	// Computed fields (not stored)
	NodeCount int `json:"node_count,omitempty"`
}

// DataPlaneHeartbeat is sent by a DP gateway to report its health.
type DataPlaneHeartbeat struct {
	DataPlaneID  string   `json:"dataplane_id"`
	Status       string   `json:"status"`
	NodeCount    int      `json:"node_count"`
	ActiveModels []string `json:"active_models,omitempty"`
}

// DataPlaneConfigSnapshot is the config the CP pushes to a DP.
type DataPlaneConfigSnapshot struct {
	// RoutingTable maps model_id → deployment details.
	RoutingTable map[string]any `json:"routing_table"`
	// AuthBundle: API key hashes + quota limits for this DP.
	AuthBundle map[string]any `json:"auth_bundle"`
	// PolicyBundle: OPA policies active for this DP.
	PolicyBundle []string `json:"policy_bundle"`
	// GeneratedAt: when this snapshot was generated.
	GeneratedAt time.Time `json:"generated_at"`
}

// Platform-level permission strings (all capabilities).
// Fine-grained RBAC: callers check Has(perm) against EffectivePermissions.
const (
	// Platform-scope
	PermPlatformOrgsCreate  = "platform:orgs:create"
	PermPlatformOrgsDelete  = "platform:orgs:delete"
	PermPlatformPoolsManage = "platform:pools:manage"
	PermPlatformUsersInvite = "platform:users:invite"

	// Org-scope
	PermOrgTeamsCreate   = "org:teams:create"
	PermOrgTeamsDelete   = "org:teams:delete"
	PermOrgMembersInvite = "org:members:invite"
	PermOrgMembersRemove = "org:members:remove"
	PermOrgRolesCreate   = "org:roles:create"
	PermOrgRolesDelete   = "org:roles:delete"
	PermOrgPoolsRequest  = "org:pools:request"

	// Team-scope
	PermTeamModelsDeploy    = "team:models:deploy"
	PermTeamModelsUndeploy  = "team:models:undeploy"
	PermTeamKeysCreate      = "team:keys:create"
	PermTeamKeysRevoke      = "team:keys:revoke"
	PermTeamMembersView     = "team:members:view"
	PermTeamMembersInvite   = "team:members:invite"
	PermTeamMembersRemove   = "team:members:remove"
	PermTeamMetricsView     = "team:metrics:view"
	PermTeamApprovalsView   = "team:approvals:view"
	PermTeamApprovalsReview = "team:approvals:review"

	// Inference-scope
	PermInferenceCall = "inference:call"
)
