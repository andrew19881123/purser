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

/**
 * One row from the deployment approval queue (AI Act Art.14 human oversight).
 * Enterprise-gated: requires the "deployment_approvals" feature.
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

/** Full chargeback report for a configurable time window. */
export interface BillingReport {
  period_start: string;  // ISO-8601
  period_end: string;    // ISO-8601
  tenants: BillingTenantUsage[];
  total_requests: number;
  total_tokens: number;
}

/** Quick billing summary (non-gated) for the Settings QuickStatsBar. */
export interface BillingSummary {
  period_start: string;
  period_end: string;
  total_requests: number;
  total_tokens: number;
  active_tenants: number;
}
