// ---------------------------------------------------------------------------
// Real HTTP implementation of the `PurserApi` seam, targeting the Control Plane
// management surface at `/api/v1/...`. It is the DEFAULT backend selected in
// ./client.ts; the in-memory mock is opt-in (VITE_PURSER_MOCK / PURSER_UI_MOCK).
//
// Wire format: the `purser.v1` proto is snake_case; our TS types are camelCase.
// We convert with a thin, idempotent serializer:
//   - responses  -> camelizeKeys  (snake_case OR already-camel both work, so
//                    this is safe whether the backend emits protojson canonical
//                    camelCase or the original proto field names).
//   - request bodies -> snakeizeKeys (the original proto names are always
//                    accepted by protojson / grpc-gateway parsers).
// Free-form map fields (engineVersions) are passed through verbatim so their
// keys (e.g. "llama.cpp") are never mangled.
//
// Normalizers fill missing/renamed fields with graceful defaults, so a partial
// backend payload degrades instead of throwing (per the "fallback grazioso"
// requirement) — and so composite UI shapes (NodeView, CatalogEntry) can be
// reconstructed if the backend returns the leaner proto messages instead.
// ---------------------------------------------------------------------------
import type {
  AccessLogParams,
  AccessLogResponse,
  ApiKey,
  ApiKeyWithSecret,
  Assignment,
  AuditEntry,
  AuditLog,
  Backend,
  BillingForecastResponse,
  BillingReport,
  BillingSummary,
  CatalogEntry,
  ChainVerifyResponse,
  ClusterCapacity,
  DataPlane,
  DataPlaneWithToken,
  DeployOverrides,
  Deployment,
  DeploymentApproval,
  DeploymentPlan,
  DeploymentState,
  EffectivePermissions,
  EnterpriseStatus,
  FitVerdict,
  ImportSource,
  InferenceAuditParams,
  InferenceAuditResponse,
  JoinInfo,
  JoinTokenResult,
  KeyUsage,
  LinkQuality,
  MetricsSnapshot,
  MetricsStreamHandlers,
  ModelHealth,
  ModelSpec,
  NodeLoadStatus,
  NodePool,
  NodeView,
  Organization,
  PerfEstimate,
  PlatformUser,
  PlanPreviewResult,
  PoliciesResponse,
  Policy,
  PoolTeamQuota,
  ReconcilerStatus,
  Role,
  BillingForecastResponse,
  SloComplianceResponse,

  ServiceAccount,
  ServiceAccountWithSecret,

  SloApiResponse,
  Team,
  TeamMember,
  UsageSummary,
  WhatIfRequest,
  WhatIfResult,
} from './types';
import type { CreateApiKeyInput, CreateDataPlaneInput, CreateServiceAccountInput, PurserApi } from './client';

// --- error type -------------------------------------------------------------

/**
 * A failed HTTP call. `status` is the response code (0 for a network/transport
 * failure). Pages map `status` onto actionable, localized messages via
 * ../lib/errors.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly body: unknown;
  constructor(status: number, message: string, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

// --- case conversion --------------------------------------------------------

type Json = unknown;
/** Map fields whose *values* must not have their keys rewritten. */
const OPAQUE_KEYS = new Set(['engineVersions', 'engine_versions']);

const snakeToCamel = (k: string): string =>
  k.replace(/_([a-z0-9])/g, (_, c: string) => c.toUpperCase());
const camelToSnake = (k: string): string =>
  k.replace(/[A-Z]/g, (c) => `_${c.toLowerCase()}`);

function camelizeKeys(value: Json): Json {
  if (Array.isArray(value)) return value.map(camelizeKeys);
  if (value && typeof value === 'object') {
    const out: Record<string, Json> = {};
    for (const [k, v] of Object.entries(value as Record<string, Json>)) {
      const ck = snakeToCamel(k);
      out[ck] = OPAQUE_KEYS.has(ck) ? v : camelizeKeys(v);
    }
    return out;
  }
  return value;
}

function snakeizeKeys(value: Json): Json {
  if (Array.isArray(value)) return value.map(snakeizeKeys);
  if (value && typeof value === 'object') {
    const out: Record<string, Json> = {};
    for (const [k, v] of Object.entries(value as Record<string, Json>)) {
      out[camelToSnake(k)] = snakeizeKeys(v);
    }
    return out;
  }
  return value;
}

// --- fetch wrapper ----------------------------------------------------------

interface RequestInitLite {
  method?: string;
  body?: unknown;
}

function createClient(baseUrl: string) {
  async function request<T>(path: string, init?: RequestInitLite): Promise<T> {
    const hasBody = init?.body !== undefined;
    let res: Response;
    try {
      res = await fetch(`${baseUrl}${path}`, {
        method: init?.method ?? 'GET',
        headers: {
          Accept: 'application/json',
          ...(hasBody ? { 'Content-Type': 'application/json' } : {}),
        },
        // Same-origin control plane serves the UI; send session cookies.
        credentials: 'same-origin',
        body: hasBody ? JSON.stringify(snakeizeKeys(init!.body)) : undefined,
      });
    } catch (err) {
      throw new ApiError(0, err instanceof Error ? err.message : 'Network error');
    }

    if (!res.ok) {
      let body: unknown;
      let message = `HTTP ${res.status}`;
      try {
        body = camelizeKeys(await res.json());
        const m =
          body && typeof body === 'object'
            ? ((body as Record<string, unknown>).message ??
              (body as Record<string, unknown>).error)
            : undefined;
        if (typeof m === 'string' && m.length > 0) message = m;
      } catch {
        /* non-JSON error body — keep the status message */
      }
      if (res.status === 401) {
        // Lazy import to avoid circular dependency
        const { handleUnauthorized } = await import('./config');
        handleUnauthorized();
      }
      throw new ApiError(res.status, message, body);
    }

    if (res.status === 204) return undefined as T;
    const text = await res.text();
    if (!text) return undefined as T;
    return camelizeKeys(JSON.parse(text)) as T;
  }

  return { request };
}

// --- normalizers (graceful, backend-shape-tolerant) -------------------------

const num = (v: unknown, d = 0): number => (typeof v === 'number' && isFinite(v) ? v : d);
const str = (v: unknown, d = ''): string => (typeof v === 'string' ? v : d);
const bool = (v: unknown, d = false): boolean => (typeof v === 'boolean' ? v : d);

/** Normalize a proto-style UPPER_CASE enum string to its short lowercase form.
 *  e.g. "NODE_STATE_READY" → "ready", "BACKEND_CPU" → "cpu", "OS_LINUX" → "linux".
 *  If the value already matches a known lowercase form it is returned unchanged.
 *  Falls back to returning the whole lowercased string (never throws). */
function normalizeEnumStr(value: unknown, known: readonly string[]): string {
  if (typeof value !== 'string') return '';
  if (known.includes(value)) return value;
  // Already lowercase but with prefix stripped — try direct lower match first.
  const lower = value.toLowerCase();
  if (known.includes(lower)) return lower;
  // Proto enum format: PREFIX_VALUE or PREFIX_TYPE_VALUE
  // Strip leading "word_" segments until we find a known value.
  const parts = lower.split('_');
  for (let i = 1; i < parts.length; i++) {
    const candidate = parts.slice(i).join('_');
    if (known.includes(candidate)) return candidate;
  }
  return lower;
}

function normalizePerf(raw: unknown): PerfEstimate {
  const p = (raw ?? {}) as Record<string, unknown>;
  return {
    // The Go API emits decodeMinTokS/decodeMaxTokS; the proto canonical form is
    // decodeTokSMin/decodeTokSMax. Accept both so the normalizer is shape-tolerant.
    decodeTokSMin: num(p.decodeTokSMin ?? p.decodeMinTokS),
    decodeTokSMax: num(p.decodeTokSMax ?? p.decodeMaxTokS),
    prefillTokSMin: num(p.prefillTokSMin ?? p.prefillMinTokS),
    prefillTokSMax: num(p.prefillTokSMax ?? p.prefillMaxTokS),
    headroomGb: num(p.headroomGb),
  };
}

function normalizeAssignment(raw: unknown): Assignment {
  const a = (raw ?? {}) as Record<string, unknown>;
  return {
    nodeId: str(a.nodeId),
    role: (str(a.role, 'worker') as Role) || 'worker',
    layerStart: num(a.layerStart),
    layerEnd: num(a.layerEnd),
    draft: bool(a.draft),
  };
}

function normalizePlan(raw: unknown): DeploymentPlan {
  const p = (raw ?? {}) as Record<string, unknown>;
  const assignments = Array.isArray(p.assignments)
    ? p.assignments.map(normalizeAssignment)
    : [];
  return {
    planId: str(p.planId),
    modelId: str(p.modelId),
    quantization: str(p.quantization),
    assignments,
    pipelineOrder: Array.isArray(p.pipelineOrder)
      ? (p.pipelineOrder as unknown[]).map((x) => str(x))
      : assignments.map((a) => a.nodeId),
    estimated: normalizePerf(p.estimated),
    cost: num(p.cost),
    explanation: Array.isArray(p.explanation)
      ? (p.explanation as unknown[]).map((x) => str(x))
      : [],
  };
}

const NODE_LOAD_STATES = ['loading', 'ready', 'running', 'degraded'] as const;

function normalizeNodeStatus(raw: unknown): NodeLoadStatus {
  const s = (raw ?? {}) as Record<string, unknown>;
  const rawState = normalizeEnumStr(s.state, NODE_LOAD_STATES);
  const state = (NODE_LOAD_STATES as readonly string[]).includes(rawState)
    ? (rawState as NodeLoadStatus['state'])
    : 'loading';
  return {
    nodeId: str(s.nodeId),
    state,
    progress: num(s.progress),
    detail: str(s.detail),
  };
}

const DEPLOYMENT_STATES = [
  'planned', 'provisioning', 'active', 'rebalancing', 'stopping', 'stopped', 'failed',
] as const;

function normalizeDeploymentState(raw: unknown): DeploymentState {
  const s = normalizeEnumStr(raw, DEPLOYMENT_STATES);
  return (DEPLOYMENT_STATES as readonly string[]).includes(s)
    ? (s as DeploymentState)
    : 'provisioning';
}

/** Accepts a full Deployment, or a bare DeploymentPlan (builds a provisioning
 *  deployment around it — used when POST /deploy returns just the plan). */
function normalizeDeployment(raw: unknown): Deployment {
  const d = (raw ?? {}) as Record<string, unknown>;

  // Go API shape: { id, modelId, planId, state, detail: { modelId, quantization, engines: [...] } }
  // The "detail" key signals this newer shape where per-assignment info lives in engines[].
  if (d.detail && typeof d.detail === 'object') {
    const detail = d.detail as Record<string, unknown>;
    const engines: Array<Record<string, unknown>> = Array.isArray(detail.engines)
      ? (detail.engines as Array<Record<string, unknown>>)
      : [];
    const assignments: Assignment[] = engines.map((eng) => ({
      nodeId: str(eng.nodeId ?? eng.node_id),
      role: (normalizeEnumStr(eng.role, ['host', 'worker']) || 'worker') as Role,
      layerStart: num(eng.layerStart),
      layerEnd: num(eng.layerEnd),
      draft: bool(eng.draft),
    }));
    const plan: DeploymentPlan = {
      planId: str(d.planId),
      modelId: str(d.modelId ?? detail.modelId),
      quantization: str(detail.quantization),
      assignments,
      pipelineOrder: assignments.map((a) => a.nodeId),
      estimated: { decodeTokSMin: 0, decodeTokSMax: 0, prefillTokSMin: 0, prefillTokSMax: 0, headroomGb: 0 },
      cost: 0,
      explanation: [],
    };
    const state = normalizeDeploymentState(d.state);
    const nodeStatus: NodeLoadStatus[] = assignments.map((a) => ({
      nodeId: a.nodeId,
      state: state === 'active' ? ('running' as const) : ('loading' as const),
      progress: state === 'active' ? 1 : 0,
      detail: '',
    }));
    return {
      id: str(d.id, plan.planId),
      plan,
      state,
      nodeStatus,
      createdAt: str(d.createdAt, new Date().toISOString()),
    };
  }

  // Bare plan? (no lifecycle fields, but has assignments/plan-ish shape)
  if (d.plan === undefined && d.state === undefined && d.assignments !== undefined) {
    return deploymentFromPlan(normalizePlan(d));
  }
  const plan = normalizePlan(d.plan ?? d);
  const nodeStatus = Array.isArray(d.nodeStatus)
    ? d.nodeStatus.map(normalizeNodeStatus)
    : plan.assignments.map(
        (a): NodeLoadStatus => ({
          nodeId: a.nodeId,
          state: 'loading',
          progress: 0,
          detail: '',
        }),
      );
  return {
    id: str(d.id, plan.planId),
    plan,
    state: normalizeDeploymentState(d.state),
    nodeStatus,
    createdAt: str(d.createdAt, new Date().toISOString()),
  };
}

function deploymentFromPlan(plan: DeploymentPlan): Deployment {
  return {
    id: plan.planId,
    plan,
    state: 'provisioning',
    createdAt: new Date().toISOString(),
    nodeStatus: plan.assignments.map((a) => ({
      nodeId: a.nodeId,
      state: 'loading' as const,
      progress: 0,
      detail: '',
    })),
  };
}

function normalizeCapacity(raw: unknown): ClusterCapacity {
  const c = (raw ?? {}) as Record<string, unknown>;
  return {
    // GET /cluster/health returns totalNodes/readyNodes; the proto shape uses
    // nodeCount/readyNodeCount. Accept both.
    nodeCount: num(c.nodeCount !== undefined ? c.nodeCount : c.totalNodes),
    readyNodeCount: num(c.readyNodeCount !== undefined ? c.readyNodeCount : c.readyNodes),
    ramTotalGb: num(c.ramTotalGb),
    ramAvailableGb: num(c.ramAvailableGb),
    vramTotalGb: num(c.vramTotalGb),
    vramAvailableGb: num(c.vramAvailableGb),
    gpuCount: num(c.gpuCount),
    backends: Array.isArray(c.backends) ? (c.backends as Backend[]) : [],
    fp4Capable: bool(c.fp4Capable),
    aggregateDecodeTokS: num(c.aggregateDecodeTokS),
  };
}

// Proto enum value sets — used to normalize raw API strings.
const NODE_STATES = [
  'provisioning', 'enrolled', 'ready', 'loading', 'running',
  'degraded', 'draining', 'unreachable', 'decommissioned',
] as const;
const OS_VALUES      = ['linux', 'darwin', 'windows'] as const;
const ARCH_VALUES    = ['x86_64', 'arm64'] as const;
const BACKEND_VALUES = ['cuda', 'metal', 'rocm', 'cpu'] as const;

/** Normalize proto-enum-valued fields in a mutable HardwareProfile record. */
function normalizeProfileEnums(profile: Record<string, unknown>): void {
  profile.state = normalizeEnumStr(profile.state, NODE_STATES) || 'ready';
  profile.os    = normalizeEnumStr(profile.os, OS_VALUES)      || 'linux';
  profile.arch  = normalizeEnumStr(profile.arch, ARCH_VALUES)  || 'x86_64';
  if (Array.isArray(profile.backends)) {
    profile.backends = (profile.backends as unknown[]).map(
      (b) => normalizeEnumStr(b, BACKEND_VALUES) || 'cpu',
    );
  }
}

/**
 * GET /api/v1/nodes returns objects with shape:
 *   { id, hostname, os, state, hardware_profile: {...}, ... }
 * (hardware_profile → hardwareProfile after camelizeKeys).
 * Also accepts the legacy composite NodeView { profile, metrics, ... } shape.
 */
function normalizeNodeView(raw: unknown): NodeView {
  const n = (raw ?? {}) as Record<string, unknown>;

  // Resolve the hardware profile — API may use "profile", "hardwareProfile",
  // or "hardware_profile" (before camelizeKeys) as the field name.
  const profileSrc =
    (n.profile as Record<string, unknown> | undefined) ??
    (n.hardwareProfile as Record<string, unknown> | undefined);

  if (profileSrc && typeof profileSrc === 'object') {
    // Composite shape — shallow-clone and back-fill nodeId from top-level id.
    const profile: Record<string, unknown> = { ...profileSrc };
    if (!profile.nodeId && n.id) profile.nodeId = n.id;
    // Ensure gpus is always an array (absent on CPU-only nodes).
    if (!Array.isArray(profile.gpus)) profile.gpus = [];
    // Normalize proto enum string values to their canonical lowercase forms.
    normalizeProfileEnums(profile);
    return {
      profile: profile as unknown as NodeView['profile'],
      metrics: (n.metrics as NodeView['metrics']) ?? null,
      role: (n.role as Role | null) ?? null,
      linkQuality: (str(n.linkQuality, 'unknown') as LinkQuality) || 'unknown',
      deploymentId: (n.deploymentId as string | null) ?? null,
    };
  }

  // Bare HardwareProfile — wrap it. Back-fill nodeId from top-level id field.
  const profile: Record<string, unknown> = {
    ...n,
    nodeId: n.nodeId ?? n.id,
    gpus: Array.isArray(n.gpus) ? n.gpus : [],
  };
  normalizeProfileEnums(profile);
  return {
    profile: profile as unknown as NodeView['profile'],
    metrics: null,
    role: null,
    linkQuality: 'unknown',
    deploymentId: null,
  };
}

/** GET /api/v1/models: [ModelSpec] (+ optional fit/deployable) -> CatalogEntry. */
function normalizeCatalogEntry(raw: unknown): CatalogEntry {
  const e = (raw ?? {}) as Record<string, unknown>;

  // Shape A: { model, fit } (explicit proto-envelope shape).
  if (e.model && typeof e.model === 'object') {
    return {
      model: e.model as ModelSpec,
      fit: normalizeFit(e.fit, e.model as ModelSpec, e.deployable),
    };
  }

  // Shape C: { id, family, spec: {...ModelSpec...}, fit: {...} }
  // This is the actual Go API shape where the ModelSpec is nested under "spec".
  if (e.spec && typeof e.spec === 'object') {
    const specRaw = e.spec as Record<string, unknown>;
    const model: ModelSpec = {
      ...specRaw,
      // Back-fill modelId from the top-level id if the spec omits it.
      modelId: specRaw.modelId ?? e.id,
      // Guarantee quantizations is always an array so callers never crash on .map().
      quantizations: Array.isArray(specRaw.quantizations) ? specRaw.quantizations : [],
    } as unknown as ModelSpec;
    // In this shape, the deployable flag lives inside the fit object.
    const fitRaw = e.fit as Record<string, unknown> | undefined;
    const deployableFlag = fitRaw?.deployable;
    return { model, fit: normalizeFit(e.fit, model, deployableFlag) };
  }

  // Shape B: a ModelSpec at the top level, possibly carrying fit / deployable alongside.
  // Guard quantizations so downstream callers never hit undefined.map().
  const model = {
    ...(e as Record<string, unknown>),
    quantizations: Array.isArray(e.quantizations) ? e.quantizations : [],
  } as unknown as ModelSpec;
  return { model, fit: normalizeFit(e.fit, model, e.deployable) };
}

function normalizeFit(raw: unknown, model: ModelSpec, deployable: unknown): FitVerdict {
  if (raw && typeof raw === 'object') {
    const f = raw as Record<string, unknown>;
    // The Go API uses "deployable" (bool) rather than "fits"; accept both.
    const fits = bool(
      f.fits !== undefined ? f.fits : f.deployable,
      deployable === undefined ? false : Boolean(deployable),
    );
    return {
      fits,
      quantization: typeof f.quantization === 'string' ? f.quantization : null,
      // Go API uses "nodeCount"; proto shape uses "nodesNeeded"; accept both.
      nodesNeeded: num(f.nodesNeeded !== undefined ? f.nodesNeeded : f.nodeCount),
      estimated: f.estimated ? normalizePerf(f.estimated) : null,
      deficitGb: num(f.deficitGb),
      reasonKey: (str(f.reasonKey, 'fits') as FitVerdict['reasonKey']) || 'fits',
    };
  }
  // No fit provided by the backend: fall back to a neutral verdict driven by
  // the `deployable` flag (if any), so the catalog badge still renders.
  const fits = Boolean(deployable);
  return {
    fits,
    quantization: fits ? (model.quantizations[0]?.name ?? null) : null,
    nodesNeeded: fits ? 1 : 0,
    estimated: null,
    deficitGb: 0,
    reasonKey: fits ? 'fits' : 'not_enough_memory',
  };
}

function normalizeJoinInfo(raw: unknown): JoinInfo {
  const j = (raw ?? {}) as Record<string, unknown>;
  return {
    // API returns "token" in the wire format (camelizeKeys keeps it as "token").
    // Support both "joinToken" (legacy) and "token" (current) for back-compat.
    joinToken: str(j.joinToken ?? j.token),
    controlPlaneUrl: str(j.controlPlaneUrl),
    expiresAt: str(j.expiresAt ?? j.expiresAt),
  };
}

function normalizeJoinTokenResult(raw: unknown): JoinTokenResult {
  const j = (raw ?? {}) as Record<string, unknown>;
  return {
    token: str(j.token),
    clusterId: str(j.clusterId),
    expiresAt: str(j.expiresAt),
  };
}

function normalizeSnapshot(raw: unknown): MetricsSnapshot {
  const s = (raw ?? {}) as Record<string, unknown>;
  const nodes = Array.isArray(s.nodes)
    ? s.nodes.map((n) => {
        const o = (n ?? {}) as Record<string, unknown>;
        const m = (o.metrics ?? {}) as Record<string, unknown>;
        return {
          nodeId: str(o.nodeId),
          metrics: {
            prefillTokS: num(m.prefillTokS),
            decodeTokS: num(m.decodeTokS),
            ramUsedGb: num(m.ramUsedGb),
            vramUsedGb: num(m.vramUsedGb),
            queueDepth: num(m.queueDepth),
            acceptedTokensRatio: num(m.acceptedTokensRatio),
          },
        };
      })
    : [];
  return {
    at: str(s.at, new Date().toISOString()),
    aggregateDecodeTokS:
      typeof s.aggregateDecodeTokS === 'number'
        ? s.aggregateDecodeTokS
        : nodes.reduce((acc, n) => acc + n.metrics.decodeTokS, 0),
    nodes,
  };
}

// --- audit normalizers -------------------------------------------------------

function normalizeAuditEntry(raw: unknown): AuditEntry {
  const e = (raw ?? {}) as Record<string, unknown>;
  // Wire format has time_unix_nano (nanoseconds); camelizeKeys gives timeUnixNano.
  const ns = typeof e.timeUnixNano === 'number' ? e.timeUnixNano : 0;
  const createdAt =
    ns > 0 ? new Date(ns / 1_000_000).toISOString() : str(e.createdAt as unknown);
  return {
    seq: num(e.seq),
    actor: str(e.actor),
    action: str(e.action),
    target: str(e.target),
    details:
      e.details && typeof e.details === 'object'
        ? (e.details as Record<string, string>)
        : undefined,
    prevHash: str(e.prevHash),
    hash: str(e.hash),
    createdAt,
  };
}

function normalizeAuditLog(raw: unknown): AuditLog {
  const r = (raw ?? {}) as Record<string, unknown>;
  const chain = (r.chain ?? {}) as Record<string, unknown>;
  const chainBreak =
    chain.break && typeof chain.break === 'object'
      ? (chain.break as { index: number; seq: number; kind: string; msg: string })
      : undefined;
  return {
    feature: str(r.feature, 'audit'),
    licensee: str(r.licensee),
    entries: Array.isArray(r.entries) ? r.entries.map(normalizeAuditEntry) : [],
    chain: {
      verified: bool(chain.verified, false),
      length: num(chain.length),
      break: chainBreak,
    },
  };
}

// --- the PurserApi HTTP implementation --------------------------------------

const enc = encodeURIComponent;

function normalizeApproval(raw: unknown): DeploymentApproval {
  const a = (raw ?? {}) as Record<string, unknown>;
  return {
    id: typeof a.id === 'number' ? a.id : 0,
    deploymentId: str(a.deploymentId),
    modelId: str(a.modelId),
    requester: str(a.requester),
    requestedAt: str(a.requestedAt, new Date().toISOString()),
    status: (str(a.status, 'pending') as DeploymentApproval['status']) || 'pending',
    reviewer: a.reviewer ? str(a.reviewer) : undefined,
    reviewedAt: a.reviewedAt ? str(a.reviewedAt) : undefined,
    notes: a.notes ? str(a.notes) : undefined,
  };
}

export function createHttpApi(baseUrl: string): PurserApi {
  const { request } = createClient(baseUrl);

  return {
    // --- fleet ---
    // GET /api/v1/cluster/health -> aggregated cluster state.
    getCapacity: () =>
      request<unknown>('/cluster/health').then(normalizeCapacity),

    // GET /api/v1/nodes
    listNodes: () =>
      request<unknown>('/nodes').then((raw) => {
        // API returns { nodes: [...] }; fall back to raw array for backward compat.
        const arr = (raw as any)?.nodes ?? raw;
        return Array.isArray(arr) ? arr.map(normalizeNodeView) : [];
      }),

    // GET /api/v1/nodes/{id}
    getNode: (nodeId) =>
      request<unknown>(`/nodes/${enc(nodeId)}`).then(normalizeNodeView),

    // Node lifecycle actions (conventional REST — not yet frozen in the docs).
    drainNode: (nodeId) =>
      request<unknown>(`/nodes/${enc(nodeId)}/drain`, { method: 'POST' }).then(
        normalizeNodeView,
      ),
    restartNode: (nodeId) =>
      request<unknown>(`/nodes/${enc(nodeId)}/restart`, { method: 'POST' }).then(
        normalizeNodeView,
      ),
    removeNode: (nodeId) => request<void>(`/nodes/${enc(nodeId)}`, { method: 'DELETE' }),

    // --- catalog ---
    // GET /api/v1/models -> [ModelSpec] (+ fit/deployable) -> CatalogEntry[]
    getCatalog: () =>
      request<unknown>('/models').then((raw) => {
        // API returns { models: [...] }; fall back to raw array for backward compat.
        const arr = (raw as any)?.models ?? raw;
        return Array.isArray(arr) ? arr.map(normalizeCatalogEntry) : [];
      }),

    // DELETE /api/v1/models/{id} — guarded delete; 409 when active deployments reference it.
    deleteModel: (modelId) => request<void>(`/models/${enc(modelId)}`, { method: 'DELETE' }),

    // Model detail is derived from the public catalog list (no private route).
    getModel: (modelId) =>
      request<unknown>('/models').then((raw) => {
        const arr = (raw as any)?.models ?? raw;
        const entries = Array.isArray(arr) ? arr.map(normalizeCatalogEntry) : [];
        const found = entries.find((e) => e.model.modelId === modelId);
        if (!found) throw new ApiError(404, `Model ${modelId} is not in the catalog.`);
        return found.model;
      }),

    // POST /api/v1/models/import — register a model from an external registry.
    // The backend fetches metadata from the source and persists a ModelSpec.
    // NOTE: the server field is "source" (not "type") — destructure to rename.
    importModel: (src: ImportSource) => {
      const { type, ...rest } = src;
      return request<unknown>('/models/import', {
        method: 'POST',
        body: { source: type, ...rest },
      }).then((raw) => {
        const r = (raw ?? {}) as Record<string, unknown>;
        // Backend may return the full ModelSpec or just { model_id: "..." }.
        if (r.modelId || r.model) {
          return (r.model ?? raw) as ModelSpec;
        }
        throw new ApiError(500, 'Import returned no model spec');
      });
    },

    // POST /api/v1/models/{id}/plan — dry-run plan, never persisted.
    // A 200 body is always returned: { feasible, reason? } or { feasible, ...planFields }.
    previewModelPlan: (modelId: string) =>
      request<unknown>(`/models/${enc(modelId)}/plan`, { method: 'POST', body: {} })
        .then((raw): PlanPreviewResult => {
          const r = (raw ?? {}) as Record<string, unknown>;
          if (r.feasible === false) {
            return { feasible: false, reason: typeof r.reason === 'string' ? r.reason : 'Model cannot be deployed on this fleet.' };
          }
          // The embedded plan may be in r.plan (protojson blob) or at the top level.
          const inner = r.plan && typeof r.plan === 'object'
            ? (r.plan as Record<string, unknown>)
            : r;
          // Top-level `id` from registry.Plan maps to planId.
          const merged: Record<string, unknown> = {
            ...inner,
            planId: inner.planId ?? r.id,
            modelId: inner.modelId ?? r.modelId,
          };
          return { feasible: true, plan: normalizePlan(merged) };
        }),

    // GET /api/v1/models/{id}/health — operational health of a deployed model.
    getModelHealth: (modelId: string) =>
      request<unknown>(`/models/${enc(modelId)}/health`).then((raw) => raw as ModelHealth),

    // --- deployments ---
    // Dry-run plan (preview). Conventional path alongside POST .../deploy.
    planDeployment: (modelId, overrides: DeployOverrides) =>
      request<unknown>(`/models/${enc(modelId)}/plan`, {
        method: 'POST',
        body: overrides,
      }).then(normalizePlan),

    // POST /api/v1/models/{id}/deploy -> 202 + Deployment (or a bare plan).
    createDeployment: (modelId, overrides: DeployOverrides) =>
      request<unknown>(`/models/${enc(modelId)}/deploy`, {
        method: 'POST',
        body: overrides,
      }).then(normalizeDeployment),

    // GET /api/v1/deployments
    listDeployments: () =>
      request<unknown>('/deployments').then((raw) => {
        // API returns { deployments: [...] }; fall back to raw array for backward compat.
        const arr = (raw as any)?.deployments ?? raw;
        return Array.isArray(arr) ? arr.map(normalizeDeployment) : [];
      }),

    // GET /api/v1/deployments/{id}
    getDeployment: (id) =>
      request<unknown>(`/deployments/${enc(id)}`).then(normalizeDeployment),

    // DELETE /api/v1/deployments/{id} -> stop & undeploy
    undeployDeployment: (id) => request<void>(`/deployments/${enc(id)}`, { method: 'DELETE' }),

    // GET /api/v1/plans/{id} -> plan (with explanation)
    getPlan: (planId) => request<unknown>(`/plans/${enc(planId)}`).then(normalizePlan),

    // --- onboarding (enrollment token) ---
    // The server only has POST /join-token (no GET); auto-issue a 24h token on load.
    getJoinInfo: () =>
      request<unknown>('/join-token', { method: 'POST', body: { ttlSeconds: 86400 } }).then(
        normalizeJoinInfo,
      ),
    rotateJoinToken: () =>
      request<unknown>('/join-token', { method: 'POST', body: { ttlSeconds: 86400 } }).then(
        normalizeJoinInfo,
      ),
    // POST /api/v1/join-token — body {ttl_seconds} (snakeizeKeys converts automatically)
    createJoinToken: (ttlSeconds) =>
      request<unknown>('/join-token', { method: 'POST', body: { ttlSeconds } }).then(
        normalizeJoinTokenResult,
      ),

    // --- settings / api keys ---
    listApiKeys: () =>
      request<unknown>('/apikeys').then((raw) => {
        // API returns { apikeys: [...] }; fall back to raw array for backward compat.
        const arr = (raw as any)?.apikeys ?? raw;
        return Array.isArray(arr) ? (arr as ApiKey[]) : [];
      }),

    // POST /api/v1/apikeys -> ApiKeyWithSecret (full secret shown once)
    createApiKey: (input: CreateApiKeyInput) =>
      request<ApiKeyWithSecret>('/apikeys', { method: 'POST', body: input }),

    revokeApiKey: (id) =>
      request<ApiKey | undefined>(`/apikeys/${enc(id)}`, { method: 'DELETE' }).then(
        (k) =>
          k ?? {
            id,
            name: '',
            team: '',
            prefix: '',
            role: 'admin' as const,
            createdAt: new Date().toISOString(),
            lastUsedAt: null,
            monthlyQuota: null,
            usedThisMonth: 0,
            revoked: true,
          },
      ),

    // --- usage ---
    getKeyUsage: (keyId: string) =>
      request<unknown>(`/apikeys/${enc(keyId)}/usage`).then((raw) => raw as KeyUsage),

    getUsageSummary: () =>
      request<unknown>('/usage/summary').then((raw) => raw as UsageSummary),

    // --- enterprise ---
    getEnterpriseStatus: () =>
      request<unknown>('/enterprise/status').then((raw) => raw as EnterpriseStatus),

    // GET /api/v1/enterprise/audit-log -> AuditLog (402 without valid license)
    getAuditLog: (limit = 100) =>
      request<unknown>(`/enterprise/audit-log?limit=${limit}`).then(normalizeAuditLog),

    // --- deployment approvals (AI Act Art.14) ---
    listDeploymentApprovals: (status?: string, limit = 50) => {
      const params = new URLSearchParams();
      if (status) params.set('status', status);
      params.set('limit', String(limit));
      return request<{ approvals: DeploymentApproval[] }>(`/approvals?${params.toString()}`).then(
        (r) => (r.approvals ?? []).map(normalizeApproval),
      );
    },

    getDeploymentApproval: (deploymentId: string) =>
      request<DeploymentApproval>(`/approvals/${enc(deploymentId)}`).then(normalizeApproval),

    approveDeployment: (deploymentId: string, notes?: string) =>
      request<DeploymentApproval>(`/approvals/${enc(deploymentId)}/approve`, {
        method: 'POST',
        body: { notes: notes ?? '' },
      }).then(normalizeApproval),

    rejectDeployment: (deploymentId: string, notes?: string) =>
      request<DeploymentApproval>(`/approvals/${enc(deploymentId)}/reject`, {
        method: 'POST',
        body: { notes: notes ?? '' },
      }).then(normalizeApproval),

    // --- live metrics (SSE) ---
    // GET /api/v1/metrics -> text/event-stream of MetricsSnapshot frames.
    streamMetrics: (handlers: MetricsStreamHandlers) => {
      const source = new EventSource(`${baseUrl}/metrics`, { withCredentials: true });
      const stop = () => source.close();
      source.onmessage = (ev) => {
        if (!ev.data || ev.data === '[DONE]') return;
        try {
          handlers.onMetrics(normalizeSnapshot(camelizeKeys(JSON.parse(ev.data))));
        } catch (err) {
          handlers.onError?.(err instanceof Error ? err : new Error(String(err)));
        }
      };
      source.onerror = () => handlers.onError?.(new ApiError(0, 'Metrics stream error'));
      handlers.signal?.addEventListener('abort', stop, { once: true });
      return stop;
    },

    // --- reconciler ---
    // GET /api/v1/reconciler/status -> ReconcilerStatus (config + pending tracker).
    getReconcilerStatus: () =>
      request<unknown>('/reconciler/status').then((raw) => raw as ReconcilerStatus),

    // --- billing / chargeback ---
    getBillingReport: (start: string, end: string, tenantId?: string): Promise<BillingReport> => {
      const params = new URLSearchParams({ start, end });
      if (tenantId) params.set('tenant_id', tenantId);
      return request<BillingReport>(`/billing/report?${params.toString()}`);
    },

    getBillingCsvUrl: (start: string, end: string, tenantId?: string): string => {
      const params = new URLSearchParams({ start, end, format: 'csv' });
      if (tenantId) params.set('tenant_id', tenantId);
      return `${baseUrl}/billing/report?${params.toString()}`;
    },

    getBillingXlsxUrl: (start: string, end: string, tenantId?: string): string => {
      const params = new URLSearchParams({ start, end, format: 'xlsx' });
      if (tenantId) params.set('tenant_id', tenantId);
      return `${baseUrl}/billing/report?${params.toString()}`;
    },

    getBillingPdfUrl: (start: string, end: string, tenantId?: string): string => {
      const params = new URLSearchParams({ start, end, format: 'pdf' });
      if (tenantId) params.set('tenant_id', tenantId);
      return `${baseUrl}/billing/report?${params.toString()}`;
    },

    getBillingSummary: (tenantId?: string): Promise<BillingSummary> => {
      const params = new URLSearchParams();
      if (tenantId) params.set('tenant_id', tenantId);
      const qs = params.toString() ? `?${params.toString()}` : '';
      return request<BillingSummary>(`/billing/summary${qs}`);
    },

    // --- v0.4 platform model: organizations ---
    listOrganizations: () =>
      request<{ organizations: Organization[] }>('/platform/orgs'),

    createOrganization: (data) =>
      request<Organization>('/platform/orgs', { method: 'POST', body: data }),

    getOrganization: (id) =>
      request<Organization>(`/platform/orgs/${enc(id)}`),

    deleteOrganization: (id) =>
      request<void>(`/platform/orgs/${enc(id)}`, { method: 'DELETE' }),

    // --- v0.4 platform model: teams ---
    listTeams: (orgId) =>
      request<{ teams: Team[] }>(`/platform/orgs/${enc(orgId)}/teams`),

    createTeam: (orgId, data) =>
      request<Team>(`/platform/orgs/${enc(orgId)}/teams`, { method: 'POST', body: data }),

    getTeam: (id) =>
      request<Team>(`/platform/teams/${enc(id)}`),

    deleteTeam: (id) =>
      request<void>(`/platform/teams/${enc(id)}`, { method: 'DELETE' }),

    // --- v0.4 platform model: team members ---
    listTeamMembers: (teamId) =>
      request<{ members: TeamMember[] }>(`/platform/teams/${enc(teamId)}/members`),

    addTeamMember: (teamId, data) =>
      request<TeamMember>(`/platform/teams/${enc(teamId)}/members`, { method: 'POST', body: data }),

    removeTeamMember: (teamId, userId) =>
      request<void>(`/platform/teams/${enc(teamId)}/members/${enc(userId)}`, { method: 'DELETE' }),

    // --- v0.4 platform model: node pools ---
    listNodePools: () =>
      request<{ pools: NodePool[] }>('/platform/pools'),

    createNodePool: (data) =>
      request<NodePool>('/platform/pools', { method: 'POST', body: data }),

    getNodePool: (id) =>
      request<NodePool>(`/platform/pools/${enc(id)}`),

    listPoolNodes: (poolId) =>
      request<{ node_ids: string[] }>(`/platform/pools/${enc(poolId)}/nodes`),

    assignNodeToPool: (poolId, nodeId) =>
      request<void>(`/platform/pools/${enc(poolId)}/nodes/${enc(nodeId)}`, { method: 'PUT' }),

    removeNodeFromPool: (poolId, nodeId) =>
      request<void>(`/platform/pools/${enc(poolId)}/nodes/${enc(nodeId)}`, { method: 'DELETE' }),

    listPoolQuotas: (poolId) =>
      request<{ quotas: PoolTeamQuota[] }>(`/platform/pools/${enc(poolId)}/quotas`),

    upsertPoolQuota: (poolId, teamId, quota) =>
      request<PoolTeamQuota>(`/platform/pools/${enc(poolId)}/quotas/${enc(teamId)}`, {
        method: 'PUT',
        body: quota,
      }),

    // --- v0.4 platform model: current user ---
    getMe: () =>
      request<{ actor: string; orgs: Organization[]; teams: Team[] }>('/platform/me'),

    getMyTeamPermissions: (teamId) =>
      request<EffectivePermissions>(`/platform/teams/${enc(teamId)}/my-permissions`),

    // --- inference audit ---
    listInferenceAudit: (params: InferenceAuditParams = {}): Promise<InferenceAuditResponse> => {
      const p = new URLSearchParams();
      if (params.limit != null) p.set('limit', String(params.limit));
      if (params.offset != null) p.set('offset', String(params.offset));
      if (params.modelId) p.set('model_id', params.modelId);
      if (params.tenant) p.set('tenant', params.tenant);
      if (params.since) p.set('since', params.since);
      if (params.until) p.set('until', params.until);
      const qs = p.toString() ? `?${p.toString()}` : '';
      return request<InferenceAuditResponse>(`/inference-audit${qs}`);
    },

    verifyAuditChain: (): Promise<ChainVerifyResponse> =>
      request<ChainVerifyResponse>('/inference-audit/verify'),

    listAccessLog: (params: AccessLogParams = {}): Promise<AccessLogResponse> => {
      const p = new URLSearchParams();
      if (params.limit != null) p.set('limit', String(params.limit));
      if (params.apiKeyId) p.set('api_key_id', params.apiKeyId);
      const qs = p.toString() ? `?${p.toString()}` : '';
      return request<AccessLogResponse>(`/logs/access${qs}`);
    },

    // --- what-if planner ---
    whatIfPlan: (body: WhatIfRequest): Promise<WhatIfResult> =>
      request<WhatIfResult>('/planner/what-if', { method: 'POST', body }),

    // --- SLO compliance ---
    getSloCompliance: (windowHours = 24): Promise<SloComplianceResponse> =>
      request<unknown>(`/slo/compliance?window_hours=${windowHours}`).then((raw) => {
        const r = (raw ?? {}) as Record<string, unknown>;
        return {
          models: Array.isArray(r.models) ? r.models as SloComplianceResponse['models'] : [],
          window_hours: typeof r.windowHours === 'number' ? r.windowHours : windowHours,

    // --- SLO compliance (full nested shape, v0.6) ---
    getSloComplianceFull: (windowHours = 24): Promise<SloApiResponse> =>
      request<unknown>(`/slo/compliance?window_hours=${windowHours}`).then((raw) => {
        const r = (raw ?? {}) as Record<string, unknown>;
        return {
          models: Array.isArray(r.models) ? (r.models as SloApiResponse['models']) : [],
          window_hours: typeof r.window_hours === 'number' ? r.window_hours : windowHours,
          generated_at: typeof r.generated_at === 'string' ? r.generated_at : new Date().toISOString(),
        };
      }),

    // --- billing forecast ---
    getBillingForecast: (): Promise<BillingForecastResponse> =>
      request<unknown>('/billing/forecast').then((raw) => {
        const r = (raw ?? {}) as Record<string, unknown>;
        return {
          entries: Array.isArray(r.entries) ? r.entries as BillingForecastResponse['entries'] : [],
        };

    // --- data planes ---
    listDataPlanes: (): Promise<DataPlane[]> =>
      request<unknown>('/platform/dataplanes').then((raw) => {
        const arr = (raw as Record<string, unknown>)?.dataplanes ?? raw;
        return Array.isArray(arr) ? (arr as DataPlane[]) : [];
      }),

    createDataPlane: (input: CreateDataPlaneInput): Promise<DataPlaneWithToken> =>
      request<unknown>('/platform/dataplanes', {
        method: 'POST',
        body: { name: input.name, tier: input.tier, gateway_url: input.gatewayUrl, description: input.description },
      }).then((raw) => {
        const r = (raw ?? {}) as Record<string, unknown>;
        return {
          dataplane: (r.dataplane ?? r) as DataPlane,
          joinToken: String(r.joinToken ?? r.join_token ?? ''),
        };
      }),

    refreshDataPlaneConfig: (id: string): Promise<void> =>
      request<void>(`/platform/dataplanes/${enc(id)}/config/refresh`, { method: 'POST' }),

    // --- service accounts ---
    listServiceAccounts: (): Promise<ServiceAccount[]> =>
      request<unknown>('/service-accounts').then((raw) => {
        const arr = (raw as Record<string, unknown>)?.serviceAccounts ?? raw;
        return Array.isArray(arr) ? (arr as ServiceAccount[]) : [];
      }),

    createServiceAccount: (input: CreateServiceAccountInput): Promise<ServiceAccountWithSecret> =>
      request<unknown>('/service-accounts', {
        method: 'POST',
        body: { name: input.name, team_id: input.teamId, description: input.description, role: input.role },
      }).then((raw) => {
        const r = (raw ?? {}) as Record<string, unknown>;
        return {
          id: String(r.id ?? ''),
          name: String(r.name ?? input.name),
          tenant: String(r.teamId ?? r.team_id ?? r.tenant ?? input.teamId),
          description: input.description ?? '',
          role: String(r.role ?? input.role),
          scopes: Array.isArray(r.scopes) ? (r.scopes as string[]) : [],
          clientId: String(r.clientId ?? r.client_id ?? ''),
          enabled: true,
          lastUsedAt: null,
          createdAt: new Date().toISOString(),
          clientSecret: String(r.clientSecret ?? r.client_secret ?? ''),
        } satisfies ServiceAccountWithSecret;
      }),

    revokeServiceAccount: (id: string): Promise<void> =>
      request<void>(`/service-accounts/${enc(id)}`, { method: 'DELETE' }),

    // --- platform users ---
    listPlatformUsers: (): Promise<PlatformUser[]> =>
      request<unknown>('/platform/users').then((raw) => {
        const arr = (raw as Record<string, unknown>)?.users ?? raw;
        if (!Array.isArray(arr)) return [];
        return arr.map((u: unknown) => {
          const e = (u ?? {}) as Record<string, unknown>;
          // Go returns { user_sub, org_id, role } (camelizeKeys gives userSub, orgId).
          const sub = String(e.userSub ?? e.user_sub ?? e.id ?? '');
          return {
            id: sub,
            email: sub,
            displayName: String(e.displayName ?? e.display_name ?? sub),
            orgId: String(e.orgId ?? e.org_id ?? ''),
            orgName: String(e.orgName ?? e.org_name ?? e.orgId ?? e.org_id ?? ''),
            teams: Array.isArray(e.teams) ? (e.teams as string[]) : [],
            role: String(e.role ?? 'member'),
            lastActiveAt: typeof e.lastActiveAt === 'string' ? e.lastActiveAt : null,
          } satisfies PlatformUser;
        });
      }),

      }),

    // --- policy-as-code (enterprise: policy_engine) ---

    /** Normalise a raw API policy object to the UI Policy shape. */
    listPolicies: (): Promise<PoliciesResponse> =>
      request<unknown>('/policies').then((raw) => {
        const r = (raw ?? {}) as Record<string, unknown>;
        const rows = Array.isArray(r.policies) ? r.policies : [];
        return {
          policies: rows.map((p: unknown) => normPolicy(p as Record<string, unknown>)),
        };
      }),

    upsertPolicy: (name: string, rego: string, enabled = true): Promise<Policy> =>
      request<unknown>(`/policies/${enc(name)}`, {
        method: 'PUT',
        body: { rego, enabled },
      }).then((raw) => normPolicy(raw as Record<string, unknown>)),

    deletePolicy: (name: string): Promise<void> =>
      request<void>(`/policies/${enc(name)}`, { method: 'DELETE' }),
  };
}

// ---------------------------------------------------------------------------
// Policy normalizer — maps the Go registry.Policy JSON fields to the UI Policy
// shape (snake_case -> camelCase, rego -> source, description derived).
// ---------------------------------------------------------------------------

function extractDescription(rego: string): string | undefined {
  for (const line of rego.split('\n')) {
    const trimmed = line.trim();
    if (trimmed.startsWith('#')) {
      const text = trimmed.slice(1).trim();
      if (text.length > 0) return text;
    }
  }
  return undefined;
}

function normPolicy(raw: Record<string, unknown>): Policy {
  const rego = typeof raw.rego === 'string' ? raw.rego : '';
  return {
    id: typeof raw.id === 'number' ? raw.id : 0,
    name: typeof raw.name === 'string' ? raw.name : '',
    source: rego,
    enabled: typeof raw.enabled === 'boolean' ? raw.enabled : true,
    createdAt: typeof raw.created_at === 'string' ? raw.created_at : new Date().toISOString(),
    description: extractDescription(rego),
  };
}
