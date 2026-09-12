// ---------------------------------------------------------------------------
// Purser API contracts (TypeScript mirror of `proto/purser/v1/*.proto`).
//
// These types are the front-end view of the future REST surface:
//   - management plane  -> `/api/v1/...`  (nodes, models, deployments, keys)
//   - inference plane    -> `/v1/...`      (OpenAI-compatible chat/completions)
//
// Convention: proto is snake_case; here we use idiomatic camelCase. The mapping
// is 1:1 so a generated client (Phase 2, from the frozen `purser.v1` module)
// can be adapted with a thin serializer. Enums become string unions for
// ergonomics and readable mock fixtures; each lists its proto counterpart.
// ---------------------------------------------------------------------------

/** proto: enum OS */
export type OS = 'linux' | 'darwin' | 'windows';

/** proto: enum Arch */
export type Arch = 'x86_64' | 'arm64';

/** proto: enum Backend */
export type Backend = 'cuda' | 'metal' | 'rocm' | 'cpu';

/**
 * proto: enum NodeState. The operator-facing states the UI surfaces prominently
 * are: ready / running / degraded / unreachable / draining. The remaining
 * lifecycle states appear during enrollment and decommissioning.
 */
export type NodeState =
  | 'provisioning'
  | 'enrolled'
  | 'ready'
  | 'loading'
  | 'running'
  | 'degraded'
  | 'draining'
  | 'unreachable'
  | 'decommissioned';

/** proto: enum AttentionType */
export type AttentionType = 'mha' | 'gqa' | 'mla' | 'linear';

/** proto: enum Role */
export type Role = 'host' | 'worker';

/** proto: enum DeploymentState */
export type DeploymentState =
  | 'planned'
  | 'provisioning'
  | 'active'
  | 'rebalancing'
  | 'stopping'
  | 'stopped'
  | 'failed';

/** proto: message GpuInfo */
export interface GpuInfo {
  name: string;
  vramGb: number;
  unified: boolean;
  fp4Native: boolean;
  count: number;
}

/** proto: message HardwareProfile */
export interface HardwareProfile {
  nodeId: string;
  hostname: string;
  os: OS;
  arch: Arch;
  backends: Backend[];
  gpus: GpuInfo[];
  ramTotalGb: number;
  ramAvailableGb: number;
  memBandwidthGbs: number;
  diskFreeGb: number;
  engineVersions: Record<string, string>;
  /** proto google.protobuf.Timestamp -> ISO-8601 string on the wire */
  lastSeen: string;
  state: NodeState;
  /** gRPC advertised address for agent control traffic (optional — absent on older agents) */
  advertisedAgentAddr?: string;
  /** gRPC advertised address for inference routing (optional — absent on older agents) */
  advertisedInferenceAddr?: string;
}

/** proto: message LinkMetric */
export interface LinkMetric {
  fromNode: string;
  toNode: string;
  bandwidthGbs: number;
  rttMs: number;
  measuredAt: string;
}

/** proto: message Quantization */
export interface Quantization {
  name: string;
  sizeGb: number;
  requiresFp4: boolean;
  /** 0..1 relative quality score */
  quality: number;
  emulatedFp4: boolean;
}

/** proto: message DraftInfo */
export interface DraftInfo {
  available: boolean;
  type: string;
  tailLayers: number;
}

/** proto: message ModelSpec */
export interface ModelSpec {
  modelId: string;
  family: string;
  architecture: string;
  paramsTotalB: number;
  paramsActiveB: number;
  layers: number;
  hiddenSize: number;
  nKvHeads: number;
  headDim: number;
  attentionType: AttentionType;
  contextMax: number;
  isMoe: boolean;
  draft: DraftInfo;
  quantizations: Quantization[];
  engine: string;
}

/** proto: message Assignment */
export interface Assignment {
  nodeId: string;
  role: Role;
  layerStart: number;
  layerEnd: number;
  draft: boolean;
}

/** proto: message PerfEstimate — deliberately a RANGE, never a single number. */
export interface PerfEstimate {
  decodeTokSMin: number;
  decodeTokSMax: number;
  prefillTokSMin: number;
  prefillTokSMax: number;
  headroomGb: number;
}

/** proto: message DeploymentPlan */
export interface DeploymentPlan {
  planId: string;
  modelId: string;
  quantization: string;
  assignments: Assignment[];
  /** ordered node ids describing the pipeline hop order */
  pipelineOrder: string[];
  estimated: PerfEstimate;
  cost: number;
  /** human-readable, ordered rationale lines from the planner */
  explanation: string[];
}

/** proto: message EngineMetrics */
export interface EngineMetrics {
  prefillTokS: number;
  decodeTokS: number;
  ramUsedGb: number;
  vramUsedGb: number;
  queueDepth: number;
  /** speculative-decoding acceptance ratio, 0..1 */
  acceptedTokensRatio: number;
}

// ---------------------------------------------------------------------------
// Live metrics stream (GET /api/v1/metrics -> Server-Sent Events).
// Each SSE `data:` frame carries one snapshot of the fleet's live engines.
// ---------------------------------------------------------------------------

/** One node's live engine metrics inside a snapshot. */
export interface NodeMetricsSample {
  nodeId: string;
  metrics: EngineMetrics;
}

/** A single SSE frame from GET /api/v1/metrics. */
export interface MetricsSnapshot {
  /** emission time, ISO-8601 */
  at: string;
  /** aggregate decode throughput across the fleet, tok/s */
  aggregateDecodeTokS: number;
  nodes: NodeMetricsSample[];
}

/** Callbacks for a live metrics subscription (SSE). */
export interface MetricsStreamHandlers {
  onMetrics: (snapshot: MetricsSnapshot) => void;
  onError?: (error: Error) => void;
  /** abort/close the stream when this fires */
  signal?: AbortSignal;
}

// ---------------------------------------------------------------------------
// UI-facing composite/read models exposed by the `/api/v1` management surface.
// These are not raw proto messages but the aggregated shapes the control plane
// is expected to return to the dashboard (proto pieces + live runtime state).
// ---------------------------------------------------------------------------

/** Network link quality bucket, derived by the control plane from LinkMetric. */
export type LinkQuality = 'excellent' | 'good' | 'fair' | 'poor' | 'unknown';

/**
 * A node as the fleet view consumes it: static hardware profile + live metrics
 * + its role/contribution in the current deployment. GET /api/v1/nodes
 */
export interface NodeView {
  profile: HardwareProfile;
  /** live metrics when the node is running an engine, else null */
  metrics: EngineMetrics | null;
  /** contribution to the active deployment, if any */
  role: Role | null;
  linkQuality: LinkQuality;
  /** id of the deployment this node currently serves, if any */
  deploymentId: string | null;
}

/** Aggregate cluster capacity card. GET /api/v1/cluster/capacity */
export interface ClusterCapacity {
  nodeCount: number;
  readyNodeCount: number;
  ramTotalGb: number;
  ramAvailableGb: number;
  vramTotalGb: number;
  vramAvailableGb: number;
  gpuCount: number;
  /** union of backends present across the fleet */
  backends: Backend[];
  fp4Capable: boolean;
  /** aggregate decode throughput currently being produced, tok/s */
  aggregateDecodeTokS: number;
}

/** Fit verdict computed by the planner for a model against the live cluster. */
export interface FitVerdict {
  fits: boolean;
  /** chosen quantization name when fits === true */
  quantization: string | null;
  nodesNeeded: number;
  estimated: PerfEstimate | null;
  /** GB missing when fits === false */
  deficitGb: number;
  /** actionable, human-readable reason (already localized upstream is future; here we localize in UI) */
  reasonKey: FitReasonKey;
}

export type FitReasonKey =
  | 'fits'
  | 'fits_tight'
  | 'not_enough_memory'
  | 'needs_fp4'
  | 'no_ready_nodes';

/** Catalog entry = model spec + its computed fit badge. GET /api/v1/catalog */
export interface CatalogEntry {
  model: ModelSpec;
  fit: FitVerdict;
}

/** Per-node loading progress during a deployment rollout. */
export interface NodeLoadStatus {
  nodeId: string;
  state: Extract<NodeState, 'loading' | 'ready' | 'running' | 'degraded'>;
  /** 0..1 load progress for the LOADING phase */
  progress: number;
  detail: string;
}

/**
 * A live deployment as the deploy view consumes it: the plan + lifecycle state
 * + per-node rollout status. GET /api/v1/deployments/:id
 */
export interface Deployment {
  id: string;
  plan: DeploymentPlan;
  state: DeploymentState;
  nodeStatus: NodeLoadStatus[];
  createdAt: string;
}

/** Options an operator can override before/at deploy time. */
export interface DeployOverrides {
  /** force a specific node count instead of the planner's choice */
  forceNodeCount: number | null;
  /** bias the planner: quality-first vs speed-first */
  preference: 'quality' | 'balanced' | 'speed';
}

// ---------------------------------------------------------------------------
// Enrollment / onboarding — mirrors registration.proto JoinRequest/JoinReply.
// ---------------------------------------------------------------------------

export interface JoinInfo {
  /** the join token embedded in the enrollment command (registration.proto) */
  joinToken: string;
  /** control-plane address agents should register against */
  controlPlaneUrl: string;
  /** token expiry ISO-8601 */
  expiresAt: string;
}

/**
 * Result of POST /api/v1/join-token — an ephemeral, TTL-scoped enrollment
 * token generated on demand. Shown to the operator exactly once.
 */
export interface JoinTokenResult {
  /** The raw join token string (PURSER_JOIN_TOKEN). */
  token: string;
  /** Cluster identifier the agent should join (PURSER_CLUSTER_ID). */
  clusterId: string;
  /** ISO-8601 expiry timestamp. */
  expiresAt: string;
}

// ---------------------------------------------------------------------------
// Settings — API keys (managed by control plane, used as Bearer tokens on /v1).
// ---------------------------------------------------------------------------

/** RBAC role for an API key. */
export type ApiKeyRole = 'admin' | 'viewer' | 'inference';

export interface ApiKey {
  id: string;
  name: string;
  team: string;
  /** shown prefix only; the full secret is returned exactly once on creation */
  prefix: string;
  /**
   * RBAC role: "admin" = full CP access, "viewer" = GET-only on /api/v1,
   * "inference" = gateway /v1 only. Defaults to "admin" for backward compat.
   */
  role: ApiKeyRole;
  createdAt: string;
  lastUsedAt: string | null;
  /** monthly request quota; null = unlimited */
  monthlyQuota: number | null;
  usedThisMonth: number;
  revoked: boolean;
}

/** Returned exactly once at creation: contains the full secret. */
export interface ApiKeyWithSecret extends ApiKey {
  secret: string;
}

// ---------------------------------------------------------------------------
// OpenAI-compatible inference surface (`/v1/...`) used by the playground.
// Mirrors the subset of the OpenAI schema the Gateway implements.
// ---------------------------------------------------------------------------

export type ChatRole = 'system' | 'user' | 'assistant';

export interface ChatMessage {
  role: ChatRole;
  content: string;
}

export interface ChatCompletionRequest {
  model: string;
  messages: ChatMessage[];
  stream?: boolean;
  temperature?: number;
}

/** One SSE delta chunk of a streamed chat completion. */
export interface ChatCompletionChunk {
  id: string;
  model: string;
  delta: string;
  /** present on the final chunk */
  finishReason: 'stop' | 'length' | null;
}

/** GET /v1/models entry (OpenAI shape). */
export interface OpenAIModel {
  id: string;
  object: 'model';
  ownedBy: string;
}

// ---------------------------------------------------------------------------
// Model Studio — import sources and plan-preview response.
// These are the UI-side contracts for POST /api/v1/models/import
// (future backend endpoint) and POST /api/v1/models/{id}/plan.
// ---------------------------------------------------------------------------

export type ImportSourceType =
  | 'huggingface'
  | 'object_storage'
  | 'sagemaker'
  | 'vertexai'
  | 'azure_ml'
  | 'catalog';

export interface HuggingFaceSource {
  type: 'huggingface';
  /** Repository identifier, e.g. "meta-llama/Llama-3.1-8B-Instruct". */
  repo: string;
  revision?: string;
  filenamePattern?: string;
}

export interface ObjectStorageSource {
  type: 'object_storage';
  /** Full URI: s3://, gs://, or az://. */
  uri: string;
  name: string;
  family: string;
}

export interface SageMakerSource {
  type: 'sagemaker';
  modelGroup: string;
  version?: string;
}

export interface VertexAISource {
  type: 'vertexai';
  modelPath: string;
  version?: string;
}

export interface AzureMLSource {
  type: 'azure_ml';
  workspace: string;
  modelName: string;
  version?: string;
}

/** Union of all external import sources (the 'catalog' source is handled
 *  separately — it reuses the existing GET /api/v1/models list). */
export type ImportSource =
  | HuggingFaceSource
  | ObjectStorageSource
  | SageMakerSource
  | VertexAISource
  | AzureMLSource;

/**
 * Response shape for POST /api/v1/models/{id}/plan.
 * When feasible === false the plan is absent and reason describes why.
 * When feasible === true the plan carries all assignments/pipeline info.
 */
export interface PlanPreviewResult {
  feasible: boolean;
  reason?: string;
  plan?: DeploymentPlan;
}

// --- model health ---
export type ModelHealthStatus = 'healthy' | 'degraded' | 'unavailable';

/** Response shape for GET /api/v1/models/{id}/health */
export interface ModelHealth {
  modelId: string;
  status: ModelHealthStatus;
  deploymentId: string;
  deploymentState: string;
  nodeCount: number;
  errorMessage?: string;
}

// ---------------------------------------------------------------------------
// Enterprise audit log — GET /api/v1/enterprise/audit-log.
// Gated on a valid license with the "audit" feature entitlement (402 without).
// ---------------------------------------------------------------------------

/** One entry in the tamper-evident audit chain. */
export interface AuditEntry {
  seq: number;
  actor: string;
  action: string;
  target: string;
  details?: Record<string, string>;
  prevHash: string;
  hash: string;
  /** ISO-8601 timestamp, normalised from the wire's `time_unix_nano`. */
  createdAt: string;
}

/** Chain integrity summary returned alongside entries. */
export interface AuditChainVerification {
  verified: boolean;
  length: number;
  break?: { index: number; seq: number; kind: string; msg: string };
}

/** Full response shape for GET /api/v1/enterprise/audit-log. */
export interface AuditLog {
  feature: string;
  licensee: string;
  entries: AuditEntry[];
  chain: AuditChainVerification;
}

// ---------------------------------------------------------------------------
// Compliance — AI Act technical documentation, GDPR record of processing, and
// GDPR Art.17 right-to-erasure. All enterprise-gated (402 license_required).
// ---------------------------------------------------------------------------

/** Body for POST /api/v1/gdpr/erasure. */
export interface GdprErasureInput {
  /** Subject class. The backend currently supports only "api_key". */
  subjectType: string;
  /** SHA-256 hex of the subject's API key. */
  subjectIdentifier: string;
  /** Free-text reason, recorded in the immutable erasure log for accountability. */
  reason: string;
}

/** Response from POST /api/v1/gdpr/erasure. */
export interface GdprErasureResult {
  /** Number of inference-audit rows pseudonymised for the subject. */
  erasedEvents: number;
  erasureType: string;
  /** ISO-8601 completion timestamp. */
  completedAt: string;
  /** Truncated subject prefix (never the full hash) — safe to display. */
  subjectPrefix: string;
}

/** One row of GET /api/v1/gdpr/erasure-log (the backend stub currently returns []). */
export interface GdprErasureLogEntry {
  id: number;
  subjectHash: string;
  erasedAt: string;
  erasedBy: string;
  reason: string;
  eventsErased: number;
  erasureType: string;
}

// --- reconciler ---

/** Per-event-type summary inside ReconcilerStatus.tracker. */
export interface ReconcilerEventSummary {
  tracked: number;
  oldestAgeS: number;
}

/** Snapshot of the reconciler's active config knobs. */
export interface ReconcilerConfigSnapshot {
  intervalS: number;
  nodeTimeoutS: number;
  hysteresisS: number;
  actionCooldownS: number;
}

/** GET /api/v1/reconciler/status response shape. */
export interface ReconcilerStatus {
  config: ReconcilerConfigSnapshot;
  /** Keyed by event type (e.g. "node_down", "engine_down"). Only entries with
   *  tracked > 0 represent pending approval events. */
  tracker: Record<string, ReconcilerEventSummary>;
}

// ---------------------------------------------------------------------------
// Usage accounting — mirrors GET /api/v1/apikeys/{id}/usage and
// GET /api/v1/usage/summary (reported by the Gateway to the Control Plane
// via POST /api/v1/usage after each inference call).
// ---------------------------------------------------------------------------

/** Token usage for a single API key. GET /api/v1/apikeys/{id}/usage */
export interface KeyUsage {
  apiKeyId: string;
  totalRequests: number;
  inputTokens: number;
  outputTokens: number;
}

/** Aggregate usage for one tenant (team). */
export interface TenantUsage {
  tenant: string;
  totalRequests: number;
  inputTokens: number;
  outputTokens: number;
}

/** Cross-tenant usage summary. GET /api/v1/usage/summary */
export interface UsageSummary {
  tenants: TenantUsage[];
}

// ---------------------------------------------------------------------------
// Enterprise — GET /api/v1/enterprise/status.
// ---------------------------------------------------------------------------

/**
 * License and edition status returned by the control plane.
 * edition === 'community' means no license key is loaded.
 */
export interface EnterpriseStatus {
  edition: 'community' | 'enterprise';
  licensee: string;
  features: string[];
  /** ISO-8601 expiry timestamp; absent on community edition. */
  expires?: string;
}

// ---------------------------------------------------------------------------
// Deployment approval gates — GET /api/v1/approvals (AI Act Art.14).
// Enterprise-gated: requires the "deployment_approvals" feature.
// ---------------------------------------------------------------------------

/** Progress of a multi-person approval quorum (AI Act Art.14 dual-control). */
export interface ApprovalQuorumStatus {
  /** Number of approvals required before the deployment is released. */
  required: number;
  /** Number of qualifying approvals received so far. */
  received: number;
  /** Number of approvals still needed (required - received, clamped to 0). */
  remaining: number;
  /** Ordered list of reviewers who have already approved. */
  approvers?: Array<{
    /** SHA-256 hash of the reviewer's API key token. */
    actor: string;
    /** When this reviewer approved. */
    approved_at: string; // ISO8601
  }>;
}

/**
 * One row from the deployment approval queue (AI Act Art.14 human oversight).
 * Enterprise-gated: requires the "deployment_approvals" feature.
 *
 * When returned by GET /api/v1/approvals/{id} the `quorum` field is always
 * present and shows the current vote progress.
 */
export interface DeploymentApproval {
  id: number;
  deploymentId: string;
  modelId: string;
  requester: string;   // api_key_hash
  requestedAt: string; // ISO8601
  status: 'pending' | 'approved' | 'rejected';
  reviewer?: string;
  reviewedAt?: string;
  notes?: string;
  /** Quorum progress — present on GET /approvals/{id}, absent on list responses. */
  quorum?: ApprovalQuorumStatus;
}

/** Response shape for GET /api/v1/approvals */
export interface DeploymentApprovalsResponse {
  approvals: DeploymentApproval[];
}

// ---------------------------------------------------------------------------
// Billing / chargeback — GET /api/v1/billing/report.
// Enterprise-gated: requires the "billing" feature (402 without).
// GET /api/v1/billing/summary is not gated and used by the Settings-page stats.
// ---------------------------------------------------------------------------

/** Aggregate inference activity for one tenant+model pair in a billing window. */
export interface BillingTenantUsage {
  tenant_id: string;
  model_id: string;
  request_count: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  avg_latency_ms: number;
  period_start: string; // ISO-8601
  period_end: string;   // ISO-8601
}

/**
 * SLA compliance rate for one tenant over the billing window. Present in a
 * BillingReport only when the caller passes a `sla_threshold_ms` query param;
 * `sla_compliance_rate` is the fraction (0.0–1.0) of the tenant's requests
 * whose latency was below the requested threshold.
 */
export interface TenantSLAStat {
  tenant_id: string;
  sla_compliance_rate: number; // 0.0–1.0
  sla_threshold_ms: number;
}

/** Full chargeback report for a configurable time window. */
export interface BillingReport {
  period_start: string;  // ISO-8601
  period_end: string;    // ISO-8601
  tenants: BillingTenantUsage[];
  total_requests: number;
  total_tokens: number;
  /** Present only when a sla_threshold_ms was requested. */
  sla_stats?: TenantSLAStat[];
}

/**
 * Per-team billing rollup — GET /api/v1/platform/teams/{teamId}/billing.
 * Enterprise-gated (billing feature). `by_model` breaks the totals down by model.
 */
export interface TeamBillingReport {
  team_id: string;
  team_name?: string;
  org_id?: string;
  period_start: string;
  period_end: string;
  total_requests: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  total_cost_usd: number;
  by_model?: BillingTenantUsage[];
}

/**
 * Per-organization billing rollup — GET /api/v1/platform/orgs/{orgId}/billing.
 * Enterprise-gated (billing feature). Sums every team discovered under the org.
 */
export interface OrgBillingReport {
  org_id: string;
  org_name?: string;
  period_start: string;
  period_end: string;
  total_cost_usd: number;
  total_tokens: number;
  teams: TeamBillingReport[];
}

/** Quick billing summary (non-gated) for the Settings QuickStatsBar. */
export interface BillingSummary {
  period_start: string;
  period_end: string;
  total_requests: number;
  total_tokens: number;
  active_tenants: number;
}

// ---------------------------------------------------------------------------
// v0.4 Platform model — Organizations, Teams, Node Pools, RBAC.
// ---------------------------------------------------------------------------

export interface Organization {
  id: string;
  name: string;
  slug: string;
  description?: string;
  created_at: string;
  updated_at: string;
}

export interface Team {
  id: string;
  org_id: string;
  name: string;
  slug: string;
  description?: string;
  created_at: string;
  updated_at: string;
}

export interface TeamMember {
  id: number;
  team_id: string;
  user_id: string;
  role_id: string;
  created_at: string;
  user?: { email: string; display_name: string };
  role?: { name: string; permissions: string[] };
}

export interface NodePool {
  id: string;
  name: string;
  description?: string;
  owner_type: 'platform' | 'org' | 'team';
  owner_id: string;
  policy: 'exclusive' | 'shared';
  node_ids?: string[];
  created_at: string;
  updated_at: string;
}

export interface PoolTeamQuota {
  pool_id: string;
  team_id: string;
  max_deployments: number;
  max_gpu_nodes: number;
  priority: number;
}

/**
 * Mutable fields accepted by PUT /api/v1/platform/pools/{id}. All optional;
 * only provided keys are changed server-side (policy must stay shared|exclusive).
 */
export interface UpdateNodePoolInput {
  name?: string;
  description?: string;
  policy?: NodePool['policy'];
}

/**
 * A platform user as returned by GET /api/v1/platform/users.
 * The Go API currently surfaces user_sub, org_id, role from the org_members
 * table. displayName and team membership require OIDC/LDAP (Wave 3).
 */
export interface PlatformUser {
  id: string;
  email: string;
  displayName: string;
  orgId: string;
  orgName: string;
  teams: string[];
  role: string;
  lastActiveAt: string | null;
}

export interface EffectivePermissions {
  user_id: string;
  team_id: string;
  org_id: string;
  permissions: string[];
  is_org_admin: boolean;
}

// ---------------------------------------------------------------------------
// RBAC custom roles — GET/POST /api/v1/platform/orgs/{orgId}/roles and
// GET/PUT/DELETE .../roles/{id}. A role is a named bundle of permission
// strings, scoped to one org. Built-in ("system") roles are read-only.
// The Go type is registry.CustomRole; camelizeKeys maps its snake_case wire
// fields (org_id, is_system, created_at, updated_at) onto these camelCase names.
// ---------------------------------------------------------------------------

export interface CustomRole {
  id: string;
  /** Owning org id; empty for platform built-in roles. */
  orgId?: string;
  name: string;
  description?: string;
  /** Fine-grained permission keys, e.g. ["team:models:deploy"]. */
  permissions: string[];
  /** Built-in platform roles cannot be edited or deleted. */
  isSystem: boolean;
  createdAt: string;
  updatedAt: string;
}

/** Response shape for GET /api/v1/platform/orgs/{orgId}/roles. */
export interface RolesResponse {
  roles: CustomRole[];
}

/** The scope buckets the permission catalog is grouped into for display. */
export type PermissionScope = 'platform' | 'org' | 'team' | 'inference';

/**
 * One entry of the permission catalog — GET /api/v1/platform/permissions.
 * Every key here is a string the enforcement layer actually checks, so a role
 * built from these keys genuinely grants access.
 */
export interface PermissionDescriptor {
  key: string;
  description: string;
  /** "platform" | "org" | "team" | "inference" */
  scope: string;
}

/** Response shape for GET /api/v1/platform/permissions. */
export interface PermissionsResponse {
  permissions: PermissionDescriptor[];
}

// ---------------------------------------------------------------------------
// Inference Audit Log — GET /api/v1/inference-audit
// Per-request tamper-evident log; distinct from the administrative audit log.
// ---------------------------------------------------------------------------

export interface InferenceAuditEvent {
  seq: number;
  modelId: string;
  modelRevision: string;
  modelQuantization: string;
  tenant: string;
  apiKeyId: string;
  nodeId: string;
  inferenceEngine: string;
  inputTokens: number;
  outputTokens: number;
  latencyMs: number;
  status: string;
  createdAt: string;
  hash: string;
  prevHash: string;
}

export interface InferenceAuditParams {
  limit?: number;
  offset?: number;
  modelId?: string;
  tenant?: string;
  since?: string;
  until?: string;
}

export interface InferenceAuditResponse {
  events: InferenceAuditEvent[];
  total: number;
}

/** Response from GET /api/v1/inference-audit/verify */
export interface ChainVerifyResponse {
  verified: boolean;
  blockCount: number;
  lastVerifiedAt: string;
  brokenAtSeq: number | null;
}

// ---------------------------------------------------------------------------
// Access Log — GET /api/v1/logs/access
// Gateway access log entries for observability and security review.
// ---------------------------------------------------------------------------

export interface AccessLogEntry {
  id: number;
  apiKeyId: string;
  method: string;
  path: string;
  ipPrefix: string;
  userAgent: string;
  statusCode: number;
  requestAt: string;
}

export interface AccessLogParams {
  limit?: number;
  apiKeyId?: string;
}

export interface AccessLogResponse {
  entries: AccessLogEntry[];
  count: number;
}

// ---------------------------------------------------------------------------
// What-if Hardware ROI Planner — POST /api/v1/planner/what-if
// ---------------------------------------------------------------------------

export interface WhatIfNode {
  node_id: string;
  gpu_vram_gb: number;
  gpu_count: number;
  net_bandwidth_gbps: number;
}

export interface WhatIfRequest {
  model_id: string;
  hypothetical_nodes: WhatIfNode[];
  include_existing_nodes: boolean;
}

export interface WhatIfAssignment {
  node_id: string;
  layer_start: number;
  layer_end: number;
}

export interface WhatIfResult {
  feasible: boolean;
  assignments?: WhatIfAssignment[];
  estimated_decode_tok_s_min?: number;
  estimated_decode_tok_s_max?: number;
  current_plan?: { feasible: boolean };
  improvement_delta?: number;
  reason?: string;
}

// ---------------------------------------------------------------------------
// SLO Compliance — GET /api/v1/slo/compliance
// ---------------------------------------------------------------------------


// SloModelCompliance is a legacy flat shape; SloModelEntry mirrors the actual
// nested shape returned by slo.go (v0.6).
// ---------------------------------------------------------------------------

/** Legacy flat shape used by the FleetPage SloStatusCard. */
export interface SloModelCompliance {
  model_id: string;
  ttft_target_ms: number;
  ttft_actual_compliance_pct: number;
  status: 'met' | 'breached' | 'insufficient_data';
}


/** Legacy wrapper. */
export interface SloComplianceResponse {
  models: SloModelCompliance[];
  window_hours: number;
}


/** SLO contract parameters (per model or global default). */
export interface SloContractConfig {
  ttft_ms: number;
  tbt_ms: number;
  target_compliance: number;
}

/** Measured compliance data for one model in a query window. */
export interface SloActualData {
  ttft_compliance: number | null;
  tbt_compliance: number | null;
  request_count: number;
  period_start: string;
}

/** One model entry in the full nested compliance response (slo.go). */
export interface SloModelEntry {
  model_id: string;
  slo: SloContractConfig;
  actual: SloActualData;
  status: 'met' | 'breached' | 'insufficient_data';
}

/** Full compliance API response (GET /api/v1/slo/compliance). */
export interface SloApiResponse {
  window_hours: number;
  generated_at: string;
  models: SloModelEntry[];
}

/** Camelised view of one model's compliance data (derived from SloModelEntry). */
export interface SloComplianceModel {
  modelId: string;
  status: 'met' | 'breached' | 'insufficient_data';
  ttftTargetMs: number;
  ttftCompliance: number | null;
  tbtTargetMs: number;
  tbtCompliance: number | null;
  requestCount: number;
}

// ---------------------------------------------------------------------------
// Billing Forecast — GET /api/v1/billing/forecast
// Enterprise-gated: requires the "billing" feature (402 without).
// ---------------------------------------------------------------------------

export interface BillingForecastEntry {
  org_id: string;
  team_id: string;
  burn_rate_daily_usd: number;
  projected_monthly_usd: number;
  budget_monthly_usd: number;
  days_until_exhaustion: number | null;
}

export interface BillingForecastResponse {
  entries: BillingForecastEntry[];
}

// ---------------------------------------------------------------------------
// Model adoption time-series — GET /api/v1/billing/models/adoption
// Enterprise-gated: requires the "billing" feature (402 without).
// ---------------------------------------------------------------------------

/** One time bucket (day or ISO week) in a model-adoption series. */
export interface ModelAdoptionBucket {
  date: string;        // YYYY-MM-DD (day) or ISO-week start
  requests: number;
  tokens_out: number;
}

/** Request/token time-series for a single model. */
export interface ModelAdoptionSeries {
  model_id: string;
  buckets: ModelAdoptionBucket[];
}

/** Response of GET /api/v1/billing/models/adoption (top 10 models). */
export interface ModelAdoptionResponse {
  window: 'daily' | 'weekly';
  days: number;
  series: ModelAdoptionSeries[];
}

// (WhatIf types are already defined above in types.ts)

// ---------------------------------------------------------------------------
// Data Planes — GET/POST /api/v1/platform/dataplanes (v0.5+)
// ---------------------------------------------------------------------------

/**
 * A registered Data Plane: a named inference cluster (GPU nodes + Gateway)
 * connected to the Control Plane. GET /api/v1/platform/dataplanes
 */
export interface DataPlane {
  id: string;
  name: string;
  description?: string;
  /** 'production' | 'staging' | 'development' — operator-defined tier */
  tier: string;
  /** Gateway endpoint used by inference clients */
  gatewayUrl: string;
  /** 'registering' | 'active' | 'degraded' | 'offline' */
  status: string;
  /** Latest config snapshot from the CP push (routing table, auth bundle). */
  configSnapshot?: Record<string, unknown> | null;
  /** ISO-8601 last heartbeat from the DP gateway; null before first contact. */
  lastHeartbeat: string | null;
  nodeCount: number;
  createdAt: string;
  updatedAt: string;
}

/**
 * Returned exactly once on DP creation:
 * { dataplane: DataPlane, join_token: "dp_…" } — token shown once only.
 */
export interface DataPlaneWithToken {
  dataplane: DataPlane;
  joinToken: string;
}

/**
 * A fleet node as returned by GET /api/v1/platform/dataplanes/{id}/nodes.
 * A lean projection of the control-plane Node row (the operator only needs
 * enough to identify the node and see its lifecycle state here).
 */
export interface DataPlaneNode {
  id: string;
  hostname: string;
  state: string;
  os?: string;
  arch?: string;
}

/**
 * Mutable fields accepted by PUT /api/v1/platform/dataplanes/{id}. All optional;
 * only the provided keys are changed server-side.
 */
export interface UpdateDataPlaneInput {
  name?: string;
  description?: string;
  tier?: string;
  gatewayUrl?: string;
  status?: string;
}

// ---------------------------------------------------------------------------
// Service Accounts — GET/POST/DELETE /api/v1/service-accounts (v0.5+)
// Machine identities for CI/CD pipelines and automation.
// ---------------------------------------------------------------------------

/**
 * A machine identity used for OAuth2 client_credentials auth.
 * GET /api/v1/service-accounts
 */
export interface ServiceAccount {
  id: string;
  name: string;
  /** Team slug (stored as "tenant" in Go for routing compatibility). */
  tenant: string;
  description: string;
  /** 'admin' | 'inference' | 'viewer' */
  role: string;
  scopes: string[];
  /** OAuth2 client_id (public identifier). */
  clientId: string;
  enabled: boolean;
  lastUsedAt: string | null;
  createdAt: string;
}

/**
 * Returned exactly once on creation — includes the client_secret.
 * POST /api/v1/service-accounts
 */
export interface ServiceAccountWithSecret extends ServiceAccount {
  /** OAuth2 client_secret — shown once; never stored in cleartext. */
  clientSecret: string;

}

// ---------------------------------------------------------------------------
// Policy-as-Code — GET/PUT/DELETE /api/v1/policies (enterprise, policy_engine).
// The server stores Rego source as `rego`; the UI surface exposes it as `source`.
// Description is derived client-side: first `#`-comment line in the Rego source.
// ---------------------------------------------------------------------------

export interface Policy {
  id: number;
  name: string;
  /** The Rego source text (maps from the `rego` JSON field). */
  source: string;
  enabled: boolean;
  createdAt: string;
  /** Derived from first `# ...` comment line in source; absent when no comment. */
  description?: string;
}

export interface PoliciesResponse {
  policies: Policy[];
}

// ---------------------------------------------------------------------------
// Config-as-code — GET /config/export, POST /config/diff, POST /config/apply.
// The wire exchanges a raw purser.yaml document (export returns YAML text; diff
// and apply take a raw YAML body). Diff/apply responses are JSON, camelized by
// the HTTP client. Object arrays (models/deployments/quotas) are kept opaque
// (`unknown[]`) — the viewer surfaces counts and identifiers, not full specs.
// ---------------------------------------------------------------------------

export interface ConfigDiff {
  /** Model specs the apply would create. */
  modelsToAdd: unknown[];
  /** Model IDs the apply would remove. */
  modelsToRemove: string[];
  /** Deployment specs the apply would create. */
  deploymentsToAdd: unknown[];
  /** Deployment IDs the apply would remove. */
  deploymentsToRemove: string[];
  /** Quota specs the apply would upsert. */
  quotasToUpsert: unknown[];
}

/** Counts returned by POST /config/apply (from the server's `applied` object). */
export interface ConfigApplyResult {
  modelsAdded: number;
  deploymentsAdded: number;
  quotasUpserted: number;
  orgsAdded: number;
  nodePoolsAdded: number;
  slosUpserted: number;
}

// ---------------------------------------------------------------------------
// HA / Raft cluster status — GET /api/v1/cluster/status (UNauthenticated).
// Standalone (no Raft) responds { mode: "standalone", isLeader: true }. In Raft
// mode the response also carries the leader address, the Raft state string, and
// the opaque hashicorp/raft stats map (keys camelized by the HTTP client, e.g.
// `num_peers` -> `numPeers`).
// ---------------------------------------------------------------------------

export interface ClusterStatus {
  mode: 'standalone' | 'raft';
  isLeader: boolean;
  /** Leader raft address (raft mode only). */
  leader?: string;
  /** Raft state string, e.g. "Leader", "Follower", "Candidate" (raft mode only). */
  state?: string;
  /** Opaque hashicorp/raft stats map (raft mode only); keys are camelized. */
  stats?: Record<string, string>;
}
