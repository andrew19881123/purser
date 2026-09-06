// ---------------------------------------------------------------------------
// In-memory mock implementation of the PurserApi (`/api/v1`) management plane.
// Holds mutable fixture state so operator actions (drain/restart/remove,
// deploy, key create/revoke) visibly take effect. Deployment rollouts advance
// on real wall-clock time so the deploy view animates LOADING -> READY when
// React Query polls. No network, no external calls — pure offline simulation.
// ---------------------------------------------------------------------------
import type {
  ApiKey,
  ApiKeyWithSecret,
  CatalogEntry,
  ClusterCapacity,
  Deployment,
  DeploymentPlan,
  EnterpriseStatus,
  ImportSource,
  JoinInfo,
  JoinTokenResult,
  KeyUsage,
  MetricsSnapshot,
  MetricsStreamHandlers,
  ModelSpec,
  NodeView,
  PlanPreviewResult,
  UsageSummary,
} from '../api/types';
import type { CreateApiKeyInput, PurserApi } from '../api/client';
import { clamp } from '../lib/format';
import {
  mockApiKeys,
  mockJoinInfo,
  mockModels,
  mockNodes,
} from './data';
import {
  buildCatalog,
  buildPlan,
  computeCapacity,
  planToDeployment,
} from './planner';
import { cannedModelForSource, mockPreviewPlan } from './studio';

// --- mutable store ----------------------------------------------------------

let nodes: NodeView[] = structuredClone(mockNodes);
let apiKeys: ApiKey[] = structuredClone(mockApiKeys);
let joinInfo: JoinInfo = structuredClone(mockJoinInfo);
const deployments = new Map<string, Deployment>();
/** Plans indexed by planId so GET /api/v1/plans/{id} can be served. */
const plans = new Map<string, DeploymentPlan>();
/** Models imported through the Model Studio (added at runtime). */
let importedModels: ModelSpec[] = [];

/** Returns seed catalog + any runtime-imported models. */
function allModels(): ModelSpec[] {
  return [...mockModels, ...importedModels];
}

/** Record a plan so it is retrievable by id (mirror of GET /plans/{id}). */
function rememberPlan(plan: DeploymentPlan): DeploymentPlan {
  plans.set(plan.planId, structuredClone(plan));
  return plan;
}

// Seed one already-active deployment so the fleet has something running.
(() => {
  const model = allModels().find((m) => m.modelId === 'qwen3-moe-235b')!;
  const plan = rememberPlan(buildPlan(model, nodes, { forceNodeCount: 2, preference: 'balanced' }));
  const dep = planToDeployment(plan);
  dep.id = 'dep-qwen3-moe';
  dep.state = 'active';
  dep.createdAt = new Date(Date.now() - 120_000).toISOString();
  dep.nodeStatus = dep.nodeStatus.map((s) => ({
    ...s,
    state: 'running',
    progress: 1,
    detail: 'Serving',
  }));
  deployments.set(dep.id, dep);
})();

// --- helpers ----------------------------------------------------------------

/** Simulated network latency so loading/spinner states are exercised. */
function delay<T>(value: T, ms = 260): Promise<T> {
  return new Promise((resolve) => window.setTimeout(() => resolve(value), ms));
}

function findNode(nodeId: string): NodeView {
  const n = nodes.find((x) => x.profile.nodeId === nodeId);
  if (!n) throw new NotFoundError(`Node ${nodeId} is not in the fleet.`);
  return n;
}

export class NotFoundError extends Error {}

/** Advance a deployment's per-node rollout based on elapsed wall-clock time. */
function advanceDeployment(dep: Deployment): Deployment {
  if (dep.state === 'active') return dep;
  const elapsed = Date.now() - new Date(dep.createdAt).getTime();
  const perNodeMs = 5200;
  const staggerMs = 1300;

  const nodeStatus = dep.nodeStatus.map((s, i) => {
    const start = i * staggerMs;
    const progress = clamp((elapsed - start) / perNodeMs, 0, 1);
    let detail = 'Queued…';
    if (progress > 0 && progress < 0.4) detail = 'Downloading & sharding weights…';
    else if (progress >= 0.4 && progress < 0.85) detail = 'Loading layers into memory…';
    else if (progress >= 0.85 && progress < 1) detail = 'Warming up KV cache…';
    else if (progress >= 1) detail = 'Serving';
    return {
      ...s,
      progress,
      state: progress >= 1 ? ('running' as const) : ('loading' as const),
      detail,
    };
  });

  const allReady = nodeStatus.every((s) => s.progress >= 1);
  return {
    ...dep,
    nodeStatus,
    state: allReady ? 'active' : 'provisioning',
  };
}

// --- implementation ---------------------------------------------------------

export const mockBackend: PurserApi = {
  getCapacity(): Promise<ClusterCapacity> {
    return delay(computeCapacity(nodes));
  },

  listNodes(): Promise<NodeView[]> {
    return delay(structuredClone(nodes));
  },

  getNode(nodeId): Promise<NodeView> {
    return delay(structuredClone(findNode(nodeId)), 180);
  },

  drainNode(nodeId): Promise<NodeView> {
    const n = findNode(nodeId);
    n.profile.state = 'draining';
    n.role = null;
    return delay(structuredClone(n), 450);
  },

  restartNode(nodeId): Promise<NodeView> {
    const n = findNode(nodeId);
    n.profile.state = 'ready';
    n.metrics = null;
    n.role = null;
    n.deploymentId = null;
    return delay(structuredClone(n), 600);
  },

  removeNode(nodeId): Promise<void> {
    findNode(nodeId);
    nodes = nodes.filter((x) => x.profile.nodeId !== nodeId);
    return delay(undefined, 400);
  },

  getCatalog(): Promise<CatalogEntry[]> {
    return delay(buildCatalog(allModels(), nodes));
  },

  getModel(modelId): Promise<ModelSpec> {
    const m = allModels().find((x) => x.modelId === modelId);
    if (!m) throw new NotFoundError(`Model ${modelId} is not in the catalog.`);
    return delay(structuredClone(m));
  },

  importModel(source: ImportSource): Promise<ModelSpec> {
    const spec = cannedModelForSource(source);
    // Avoid duplicates — return the existing entry if already imported.
    if (!importedModels.some((m) => m.modelId === spec.modelId)) {
      importedModels.push(structuredClone(spec));
    }
    return delay(structuredClone(spec), 700);
  },

  previewModelPlan(modelId: string): Promise<PlanPreviewResult> {
    const m = allModels().find((x) => x.modelId === modelId);
    if (!m) throw new NotFoundError(`Model ${modelId} is not in the catalog.`);
    return delay(mockPreviewPlan(m, nodes), 600);
  },

  planDeployment(modelId, overrides): Promise<DeploymentPlan> {
    const m = allModels().find((x) => x.modelId === modelId);
    if (!m) throw new NotFoundError(`Model ${modelId} is not in the catalog.`);
    return delay(rememberPlan(buildPlan(m, nodes, overrides)), 500);
  },

  createDeployment(modelId, overrides): Promise<Deployment> {
    const m = allModels().find((x) => x.modelId === modelId);
    if (!m) throw new NotFoundError(`Model ${modelId} is not in the catalog.`);
    const plan = rememberPlan(buildPlan(m, nodes, overrides));
    const dep = planToDeployment(plan);
    deployments.set(dep.id, dep);
    return delay(structuredClone(dep), 500);
  },

  listDeployments(): Promise<Deployment[]> {
    const list = Array.from(deployments.values()).map(advanceDeployment);
    list.forEach((d) => deployments.set(d.id, d));
    return delay(structuredClone(list));
  },

  getDeployment(id): Promise<Deployment> {
    const dep = deployments.get(id);
    if (!dep) throw new NotFoundError(`Deployment ${id} was not found.`);
    const advanced = advanceDeployment(dep);
    deployments.set(id, advanced);
    return delay(structuredClone(advanced), 180);
  },

  undeployDeployment(id): Promise<void> {
    const dep = deployments.get(id);
    if (!dep) throw new NotFoundError(`Deployment ${id} was not found.`);
    deployments.delete(id);
    // Release the nodes it was holding.
    nodes = nodes.map((n) =>
      n.deploymentId === id ? { ...n, role: null, deploymentId: null, metrics: null } : n,
    );
    return delay(undefined, 450);
  },

  getPlan(planId): Promise<DeploymentPlan> {
    const stored = plans.get(planId);
    if (stored) return delay(structuredClone(stored), 180);
    // Fall back to a plan embedded in a live deployment.
    for (const dep of deployments.values()) {
      if (dep.plan.planId === planId) return delay(structuredClone(dep.plan), 180);
    }
    throw new NotFoundError(`Plan ${planId} was not found.`);
  },

  getJoinInfo(): Promise<JoinInfo> {
    return delay(structuredClone(joinInfo));
  },

  rotateJoinToken(): Promise<JoinInfo> {
    const rand = Array.from(crypto.getRandomValues(new Uint8Array(24)))
      .map((b) => 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz0123456789'[b % 58])
      .join('');
    joinInfo = {
      ...joinInfo,
      joinToken: `prsr_join_${rand}`,
      expiresAt: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
    };
    return delay(structuredClone(joinInfo), 350);
  },

  createJoinToken(ttlSeconds: number): Promise<JoinTokenResult> {
    const rand = Array.from(crypto.getRandomValues(new Uint8Array(24)))
      .map((b) => 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz0123456789'[b % 58])
      .join('');
    return delay(
      {
        token: `prsr_join_${rand}`,
        clusterId: 'cluster-mock-001',
        expiresAt: new Date(Date.now() + ttlSeconds * 1000).toISOString(),
      },
      350,
    );
  },

  listApiKeys(): Promise<ApiKey[]> {
    return delay(structuredClone(apiKeys));
  },

  createApiKey(input: CreateApiKeyInput): Promise<ApiKeyWithSecret> {
    const rand = Array.from(crypto.getRandomValues(new Uint8Array(24)))
      .map((b) => 'abcdefghijklmnopqrstuvwxyz0123456789'[b % 36])
      .join('');
    const prefix = `sk-purser-${rand.slice(0, 4)}`;
    const key: ApiKey = {
      id: `key_${rand.slice(0, 8)}`,
      name: input.name,
      team: input.team,
      prefix,
      role: input.role ?? 'admin',
      createdAt: new Date().toISOString(),
      lastUsedAt: null,
      monthlyQuota: input.monthlyQuota,
      usedThisMonth: 0,
      revoked: false,
    };
    apiKeys = [key, ...apiKeys];
    return delay({ ...key, secret: `${prefix}-${rand}` }, 450);
  },

  revokeApiKey(id): Promise<ApiKey> {
    const key = apiKeys.find((k) => k.id === id);
    if (!key) throw new NotFoundError(`API key ${id} was not found.`);
    key.revoked = true;
    return delay(structuredClone(key), 350);
  },

  getKeyUsage(keyId: string): Promise<KeyUsage> {
    const key = apiKeys.find((k) => k.id === keyId);
    if (!key) throw new NotFoundError(`API key ${keyId} was not found.`);
    // Deterministic fake counts derived from the key id.
    const seed = keyId.split('').reduce((acc, c) => acc + c.charCodeAt(0), 0);
    return delay(
      {
        apiKeyId: keyId,
        totalRequests: (seed * 37) % 50_000,
        inputTokens: (seed * 1_301) % 5_000_000,
        outputTokens: (seed * 613) % 2_000_000,
      },
      200,
    );
  },

  getUsageSummary(): Promise<UsageSummary> {
    const teams = Array.from(new Set(apiKeys.map((k) => k.team)));
    return delay(
      {
        tenants: teams.map((team) => {
          const seed = team.split('').reduce((acc, c) => acc + c.charCodeAt(0), 0);
          return {
            tenant: team,
            totalRequests: (seed * 41) % 100_000,
            inputTokens: (seed * 1_409) % 10_000_000,
            outputTokens: (seed * 709) % 4_000_000,
          };
        }),
      },
      260,
    );
  },

  getEnterpriseStatus(): Promise<EnterpriseStatus> {
    return delay({ edition: 'community', licensee: 'community', features: [] }, 180);
  },

  streamMetrics(handlers: MetricsStreamHandlers): () => void {
    const emit = () => {
      const samples = nodes
        .filter((n) => n.metrics)
        .map((n) => ({
          nodeId: n.profile.nodeId,
          // Jitter the live numbers a little so the UI visibly updates.
          metrics: {
            ...n.metrics!,
            decodeTokS: Math.max(0, n.metrics!.decodeTokS + (Math.random() - 0.5) * 4),
            queueDepth: n.metrics!.queueDepth,
          },
        }));
      const snapshot: MetricsSnapshot = {
        at: new Date().toISOString(),
        aggregateDecodeTokS: samples.reduce((s, x) => s + x.metrics.decodeTokS, 0),
        nodes: samples,
      };
      handlers.onMetrics(snapshot);
    };

    emit();
    const timer = window.setInterval(emit, 1500);
    const stop = () => window.clearInterval(timer);
    handlers.signal?.addEventListener('abort', stop, { once: true });
    return stop;
  },
};
