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

// ---------------------------------------------------------------------------
// Settings — API keys (managed by control plane, used as Bearer tokens on /v1).
// ---------------------------------------------------------------------------

export interface ApiKey {
  id: string;
  name: string;
  team: string;
  /** shown prefix only; the full secret is returned exactly once on creation */
  prefix: string;
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
