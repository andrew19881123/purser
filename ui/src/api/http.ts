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
  ApiKey,
  ApiKeyWithSecret,
  Assignment,
  Backend,
  CatalogEntry,
  ClusterCapacity,
  DeployOverrides,
  Deployment,
  DeploymentPlan,
  DeploymentState,
  FitVerdict,
  ImportSource,
  JoinInfo,
  JoinTokenResult,
  LinkQuality,
  MetricsSnapshot,
  MetricsStreamHandlers,
  ModelSpec,
  NodeLoadStatus,
  NodeView,
  PerfEstimate,
  PlanPreviewResult,
  Role,
} from './types';
import type { CreateApiKeyInput, PurserApi } from './client';

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

function normalizePerf(raw: unknown): PerfEstimate {
  const p = (raw ?? {}) as Record<string, unknown>;
  return {
    decodeTokSMin: num(p.decodeTokSMin),
    decodeTokSMax: num(p.decodeTokSMax),
    prefillTokSMin: num(p.prefillTokSMin),
    prefillTokSMax: num(p.prefillTokSMax),
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

function normalizeNodeStatus(raw: unknown): NodeLoadStatus {
  const s = (raw ?? {}) as Record<string, unknown>;
  const state = str(s.state, 'loading') as NodeLoadStatus['state'];
  return {
    nodeId: str(s.nodeId),
    state,
    progress: num(s.progress),
    detail: str(s.detail),
  };
}

/** Accepts a full Deployment, or a bare DeploymentPlan (builds a provisioning
 *  deployment around it — used when POST /deploy returns just the plan). */
function normalizeDeployment(raw: unknown): Deployment {
  const d = (raw ?? {}) as Record<string, unknown>;
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
    state: (str(d.state, 'provisioning') as DeploymentState) || 'provisioning',
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
    nodeCount: num(c.nodeCount),
    readyNodeCount: num(c.readyNodeCount),
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

/** GET /api/v1/nodes may return NodeView (composite) or bare HardwareProfile. */
function normalizeNodeView(raw: unknown): NodeView {
  const n = (raw ?? {}) as Record<string, unknown>;
  // Already a composite NodeView.
  if (n.profile && typeof n.profile === 'object') {
    return {
      profile: n.profile as NodeView['profile'],
      metrics: (n.metrics as NodeView['metrics']) ?? null,
      role: (n.role as Role | null) ?? null,
      linkQuality: (str(n.linkQuality, 'unknown') as LinkQuality) || 'unknown',
      deploymentId: (n.deploymentId as string | null) ?? null,
    };
  }
  // Bare HardwareProfile — wrap it.
  return {
    profile: n as unknown as NodeView['profile'],
    metrics: null,
    role: null,
    linkQuality: 'unknown',
    deploymentId: null,
  };
}

/** GET /api/v1/models: [ModelSpec] (+ optional fit/deployable) -> CatalogEntry. */
function normalizeCatalogEntry(raw: unknown): CatalogEntry {
  const e = (raw ?? {}) as Record<string, unknown>;
  // Shape A: { model, fit }
  if (e.model && typeof e.model === 'object') {
    return {
      model: e.model as ModelSpec,
      fit: normalizeFit(e.fit, e.model as ModelSpec, e.deployable),
    };
  }
  // Shape B: a ModelSpec, possibly carrying `fit` / `deployable` alongside.
  const model = e as unknown as ModelSpec;
  return { model, fit: normalizeFit(e.fit, model, e.deployable) };
}

function normalizeFit(raw: unknown, model: ModelSpec, deployable: unknown): FitVerdict {
  if (raw && typeof raw === 'object') {
    const f = raw as Record<string, unknown>;
    return {
      fits: bool(f.fits, deployable === undefined ? false : Boolean(deployable)),
      quantization: typeof f.quantization === 'string' ? f.quantization : null,
      nodesNeeded: num(f.nodesNeeded),
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
    joinToken: str(j.joinToken),
    controlPlaneUrl: str(j.controlPlaneUrl),
    expiresAt: str(j.expiresAt),
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

// --- the PurserApi HTTP implementation --------------------------------------

const enc = encodeURIComponent;

export function createHttpApi(baseUrl: string): PurserApi {
  const { request } = createClient(baseUrl);

  return {
    // --- fleet ---
    // GET /api/v1/cluster/health -> aggregated cluster state.
    getCapacity: () =>
      request<unknown>('/cluster/health').then(normalizeCapacity),

    // GET /api/v1/nodes
    listNodes: () =>
      request<unknown>('/nodes').then((raw) =>
        Array.isArray(raw) ? raw.map(normalizeNodeView) : [],
      ),

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
      request<unknown>('/models').then((raw) =>
        Array.isArray(raw) ? raw.map(normalizeCatalogEntry) : [],
      ),

    // Model detail is derived from the public catalog list (no private route).
    getModel: (modelId) =>
      request<unknown>('/models').then((raw) => {
        const entries = Array.isArray(raw) ? raw.map(normalizeCatalogEntry) : [];
        const found = entries.find((e) => e.model.modelId === modelId);
        if (!found) throw new ApiError(404, `Model ${modelId} is not in the catalog.`);
        return found.model;
      }),

    // POST /api/v1/models/import — register a model from an external registry.
    // The backend fetches metadata from the source and persists a ModelSpec.
    importModel: (source: ImportSource) =>
      request<unknown>('/models/import', {
        method: 'POST',
        body: source,
      }).then((raw) => {
        const r = (raw ?? {}) as Record<string, unknown>;
        // Backend may return the full ModelSpec or just { model_id: "..." }.
        if (r.modelId || r.model) {
          return (r.model ?? raw) as ModelSpec;
        }
        throw new ApiError(500, 'Import returned no model spec');
      }),

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
      request<unknown>('/deployments').then((raw) =>
        Array.isArray(raw) ? raw.map(normalizeDeployment) : [],
      ),

    // GET /api/v1/deployments/{id}
    getDeployment: (id) =>
      request<unknown>(`/deployments/${enc(id)}`).then(normalizeDeployment),

    // DELETE /api/v1/deployments/{id} -> stop & undeploy
    undeployDeployment: (id) => request<void>(`/deployments/${enc(id)}`, { method: 'DELETE' }),

    // GET /api/v1/plans/{id} -> plan (with explanation)
    getPlan: (planId) => request<unknown>(`/plans/${enc(planId)}`).then(normalizePlan),

    // --- onboarding (enrollment token; path not yet frozen in the docs) ---
    getJoinInfo: () => request<unknown>('/join-token').then(normalizeJoinInfo),
    rotateJoinToken: () =>
      request<unknown>('/join-token/rotate', { method: 'POST' }).then(normalizeJoinInfo),
    // POST /api/v1/join-token — body {ttl_seconds} (snakeizeKeys converts automatically)
    createJoinToken: (ttlSeconds) =>
      request<unknown>('/join-token', { method: 'POST', body: { ttlSeconds } }).then(
        normalizeJoinTokenResult,
      ),

    // --- settings / api keys ---
    listApiKeys: () =>
      request<unknown>('/apikeys').then((raw) => (Array.isArray(raw) ? (raw as ApiKey[]) : [])),

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
  };
}
