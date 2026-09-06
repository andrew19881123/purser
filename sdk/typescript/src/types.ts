/**
 * TypeScript types for the Purser control-plane management API.
 * Field names match the server's JSON serialisation exactly.
 */

// ---------------------------------------------------------------------------
// Node types
// ---------------------------------------------------------------------------

/** Lifecycle states a node can be in. */
export type NodeState =
  | 'NODE_STATE_ENROLLED'
  | 'NODE_STATE_READY'
  | 'NODE_STATE_RUNNING'
  | 'NODE_STATE_DRAINING'
  | 'NODE_STATE_DECOMMISSIONED'
  | string;

/** A single enrolled machine in the Purser fleet. */
export interface Node {
  id: string;
  hostname: string;
  os?: string;
  arch?: string;
  ram_gb?: number;
  vram_gb?: number;
  state: NodeState;
  advertised_agent_addr?: string;
  advertised_inference_addr?: string;
  last_seen?: string;
  hardware_profile?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

// ---------------------------------------------------------------------------
// Model types
// ---------------------------------------------------------------------------

/**
 * Specification for registering a new model in the catalog.
 * Field names follow the protojson lowerCamelCase convention.
 */
export interface ModelSpec {
  modelId: string;
  family?: string;
  architecture?: string;
  paramsTotalB?: number;
  engine?: string;
  [key: string]: unknown;
}

/** A catalog entry describing a deployable model. */
export interface Model {
  id: string;
  family: string;
  architecture?: string;
  params_total_b?: number;
  engine?: string;
  spec?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

/** Estimated throughput range for a deployable model. */
export interface EstimateRange {
  decode_min_tok_s?: number;
  decode_max_tok_s?: number;
  prefill_min_tok_s?: number;
  prefill_max_tok_s?: number;
  headroom_gb?: number;
}

/** Deployability verdict for a model against the current fleet. */
export interface Fit {
  model_id?: string;
  deployable?: boolean;
  node_count?: number;
  quantization?: string;
  estimated?: EstimateRange;
  deficit_gb?: number;
  reason?: string;
  suggestions?: string[];
}

/** Model annotated with optional planner fit verdict (from listModels). */
export interface ModelWithFit extends Model {
  fit?: Fit;
}

/**
 * Health information for a deployed model.
 * Reserved for a future server endpoint — the server does not yet expose this route.
 */
export interface ModelHealth {
  model_id?: string;
  status?: string;
  [key: string]: unknown;
}

// ---------------------------------------------------------------------------
// Plan types
// ---------------------------------------------------------------------------

/** A stored deployment plan produced by the Purser planner. */
export interface Plan {
  id: string;
  model_id: string;
  quantization?: string;
  cost?: number;
  plan?: Record<string, unknown>;
  created_at?: string;
}

/** Preview result when the model fits the current fleet. Plan is ephemeral (not persisted). */
export interface PlanPreviewFeasible extends Plan {
  feasible: true;
}

/** Preview result when the model does not fit the current fleet. */
export interface PlanPreviewInfeasible {
  feasible: false;
  reason: string;
}

/** Discriminated union result from previewPlan(). Discriminated by the `feasible` field. */
export type PlanPreview = PlanPreviewFeasible | PlanPreviewInfeasible;

/** Response from POST /models/{id}/deploy on success. */
export interface DeployResponse {
  deployment_id: string;
  model_id: string;
  plan_id?: string;
}

// ---------------------------------------------------------------------------
// Deployment types
// ---------------------------------------------------------------------------

/** Lifecycle states a deployment can be in. */
export type DeploymentState =
  | 'DEPLOYMENT_STATE_PLANNED'
  | 'DEPLOYMENT_STATE_PROVISIONING'
  | 'DEPLOYMENT_STATE_ACTIVE'
  | 'DEPLOYMENT_STATE_REBALANCING'
  | 'DEPLOYMENT_STATE_STOPPING'
  | 'DEPLOYMENT_STATE_STOPPED'
  | 'DEPLOYMENT_STATE_FAILED'
  | string;

/** An active or historical deployment of a model. */
export interface Deployment {
  id: string;
  model_id: string;
  plan_id?: string;
  state: DeploymentState;
  detail?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

// ---------------------------------------------------------------------------
// API key types
// ---------------------------------------------------------------------------

/**
 * A gateway credential for authenticating inference requests.
 * The plaintext `key` field is only populated when the key was just created;
 * subsequent listApiKeys() calls omit it — the server never re-exposes the plaintext.
 */
export interface APIKey {
  id: string;
  name: string;
  tenant?: string;
  quota?: number;
  enabled?: boolean;
  /** Present only on the create response — returned exactly once. */
  key?: string;
  created_at?: string;
  updated_at?: string;
}

/**
 * Usage statistics for an API key.
 * Reserved for a future server endpoint.
 */
export interface KeyUsage {
  key_id?: string;
  [key: string]: unknown;
}

// ---------------------------------------------------------------------------
// Cluster types
// ---------------------------------------------------------------------------

/** A single-use, expiring token for enrolling a new node. */
export interface JoinToken {
  token: string;
  expires_at: string;
  cluster_id: string;
}

/** Coarse cluster health summary. */
export interface ClusterHealth {
  /** "ok" | "empty" | "degraded" | "unavailable" */
  status: string;
  total_nodes: number;
  ready_nodes: number;
  checked_at: string;
}

// ---------------------------------------------------------------------------
// Enterprise / audit types
// ---------------------------------------------------------------------------

/** License and feature information for the active edition. */
export interface EnterpriseStatus {
  /** "community" or "enterprise" */
  edition: string;
  licensee: string;
  features: string[];
  /** ISO 8601 expiry timestamp. Present on enterprise installations only. */
  expires?: string;
}

/** Details about the first broken link in the audit hash chain. */
export interface ChainBreak {
  /** 0-based index of the offending entry in the entries array. */
  index?: number;
  seq?: number;
  /** "seq" | "link" | "hash" */
  kind: string;
  msg: string;
}

/** Result of the end-to-end hash-chain verification over a returned window. */
export interface ChainVerification {
  verified: boolean;
  length: number;
  break?: ChainBreak;
}

/** One entry in the tamper-evident audit hash chain. */
export interface AuditEntry {
  /** 1-based, gap-free position in the chain. */
  seq: number;
  /** Wall-clock time as Unix nanoseconds (integer). */
  time_unix_nano: number;
  actor: string;
  action: string;
  target: string;
  details?: Record<string, string>;
  prev_hash: string;
  hash: string;
}

/** Response from GET /enterprise/audit-log. */
export interface AuditLog {
  feature: string;
  licensee: string;
  entries: AuditEntry[];
  chain: ChainVerification;
}
