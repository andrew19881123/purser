// ---------------------------------------------------------------------------
// In-memory mock implementation of the PurserApi (`/api/v1`) management plane.
// Holds mutable fixture state so operator actions (drain/restart/remove,
// deploy, key create/revoke) visibly take effect. Deployment rollouts advance
// on real wall-clock time so the deploy view animates LOADING -> READY when
// React Query polls. No network, no external calls — pure offline simulation.
// ---------------------------------------------------------------------------
import type {
  AccessLogResponse,
  ApiKey,
  ApiKeyWithSecret,
  AuditEntry,
  AuditLog,
  BillingReport,
  BillingSummary,
  CatalogEntry,
  ChainVerifyResponse,
  ClusterCapacity,
  Deployment,
  DeploymentPlan,
  EffectivePermissions,
  EnterpriseStatus,
  ImportSource,
  InferenceAuditResponse,
  JoinInfo,
  JoinTokenResult,
  KeyUsage,
  MetricsSnapshot,
  MetricsStreamHandlers,
  ModelHealth,
  ModelHealthStatus,
  ModelSpec,
  NodePool,
  NodeView,
  Organization,
  PlanPreviewResult,
  PoolTeamQuota,
  ReconcilerStatus,
  Team,
  TeamMember,
  UsageSummary,
} from '../api/types';
import type { CreateApiKeyInput, PurserApi } from '../api/client';
import { ApiError } from '../api/http';
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

  getModelHealth(modelId: string): Promise<ModelHealth> {
    // Find the most-recent deployment for this model.
    let latest: Deployment | undefined;
    for (const dep of deployments.values()) {
      if (dep.plan.modelId === modelId) {
        if (!latest || new Date(dep.createdAt) > new Date(latest.createdAt)) {
          latest = dep;
        }
      }
    }
    if (!latest) {
      return delay<ModelHealth>({
        modelId,
        status: 'unavailable',
        deploymentId: '',
        deploymentState: '',
        nodeCount: 0,
        errorMessage: 'no deployment found for this model',
      });
    }
    let status: ModelHealthStatus = 'unavailable';
    if (latest.state === 'active') status = 'healthy';
    else if (latest.state === 'provisioning' || latest.state === 'stopping') status = 'degraded';
    return delay<ModelHealth>({
      modelId,
      status,
      deploymentId: latest.id,
      deploymentState: latest.state,
      nodeCount: latest.nodeStatus.length,
    });
  },

  previewModelPlan(modelId: string): Promise<PlanPreviewResult> {
    const m = allModels().find((x) => x.modelId === modelId);
    if (!m) throw new NotFoundError(`Model ${modelId} is not in the catalog.`);
    return delay(mockPreviewPlan(m, nodes), 600);
  },

  deleteModel(modelId: string): Promise<void> {
    const m = allModels().find((x) => x.modelId === modelId);
    if (!m) throw new NotFoundError(`Model ${modelId} is not in the catalog.`);
    // Refuse deletion if any active (non-terminal) deployment references this model.
    for (const dep of deployments.values()) {
      if (
        dep.plan.modelId === modelId &&
        dep.state !== 'stopped' &&
        dep.state !== 'failed'
      ) {
        return Promise.reject(
          new ApiError(
            409,
            'model is referenced by one or more active deployments; tear them down first',
          ),
        );
      }
    }
    importedModels = importedModels.filter((x) => x.modelId !== modelId);
    return delay(undefined, 350);
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

  // --- usage ---

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

  // --- enterprise ---

  getAuditLog: async (limit = 100): Promise<AuditLog> => {
    const now = Date.now();
    const mockEntries: AuditEntry[] = [
      {
        seq: 1,
        actor: 'api',
        action: 'join_token.minted',
        target: 'default',
        prevHash: '0'.repeat(64),
        hash: 'a3f8c2d1e5b4a7f9c2d1e5b4a7f9c2d1e5b4a7f9c2d1e5b4a7f9c2d1e5b4a7f9',
        createdAt: new Date(now - 7_200_000).toISOString(),
      },
      {
        seq: 2,
        actor: 'api',
        action: 'model.created',
        target: 'llama-3.1-8b',
        details: { source: 'huggingface' },
        prevHash: 'a3f8c2d1e5b4a7f9c2d1e5b4a7f9c2d1e5b4a7f9c2d1e5b4a7f9c2d1e5b4a7f9',
        hash: '9c21b3a4d7f2e8c5b1a4d7f2e8c5b1a4d7f2e8c5b1a4d7f2e8c5b1a4d7f2e8c5',
        createdAt: new Date(now - 3_600_000).toISOString(),
      },
      {
        seq: 3,
        actor: 'api',
        action: 'apikey.created',
        target: 'dev-key-1',
        details: { team: 'engineering' },
        prevHash: '9c21b3a4d7f2e8c5b1a4d7f2e8c5b1a4d7f2e8c5b1a4d7f2e8c5b1a4d7f2e8c5',
        hash: '7b44e2a5f9c3d6a8e2a5f9c3d6a8e2a5f9c3d6a8e2a5f9c3d6a8e2a5f9c3d6a8',
        createdAt: new Date(now - 1_800_000).toISOString(),
      },
      {
        seq: 4,
        actor: 'api',
        action: 'fleet.node.draining',
        target: 'node-02',
        prevHash: '7b44e2a5f9c3d6a8e2a5f9c3d6a8e2a5f9c3d6a8e2a5f9c3d6a8e2a5f9c3d6a8',
        hash: '2e99c1a3b8f4d7e2c1a3b8f4d7e2c1a3b8f4d7e2c1a3b8f4d7e2c1a3b8f4d7e2',
        createdAt: new Date(now - 600_000).toISOString(),
      },
    ];
    const entries = mockEntries.slice(0, limit);
    return {
      feature: 'audit',
      licensee: 'Demo Corp (Mock Mode)',
      entries,
      chain: { verified: true, length: entries.length },
    };
  },

  // --- reconciler ---

  getReconcilerStatus(): Promise<ReconcilerStatus> {
    return delay<ReconcilerStatus>({
      config: { intervalS: 10, nodeTimeoutS: 45, hysteresisS: 30, actionCooldownS: 120 },
      tracker: {},
    }, 150);
  },

  // --- deployment approvals (mock: no enterprise license in mock mode) ---
  listDeploymentApprovals(): Promise<import('../api/types').DeploymentApproval[]> {
    return Promise.reject(
      Object.assign(new Error('Enterprise license required'), { status: 402 }),
    );
  },
  getDeploymentApproval(): Promise<import('../api/types').DeploymentApproval> {
    return Promise.reject(
      Object.assign(new Error('Enterprise license required'), { status: 402 }),
    );
  },
  approveDeployment(): Promise<import('../api/types').DeploymentApproval> {
    return Promise.reject(
      Object.assign(new Error('Enterprise license required'), { status: 402 }),
    );
  },
  rejectDeployment(): Promise<import('../api/types').DeploymentApproval> {
    return Promise.reject(
      Object.assign(new Error('Enterprise license required'), { status: 402 }),
    );
  },

  // --- billing / chargeback (mock: returns empty report — no enterprise in mock) ---
  getBillingReport(): Promise<BillingReport> {
    return Promise.reject(
      Object.assign(new Error('Enterprise license required'), { status: 402 }),
    );
  },

  getBillingCsvUrl(): string {
    // In mock mode there is no real CSV endpoint; return an empty data URL.
    return 'data:text/csv;charset=utf-8,tenant_id%2Cmodel_id%2Crequest_count%0A';
  },

  getBillingSummary(): Promise<BillingSummary> {
    const now = new Date().toISOString();
    return Promise.resolve({
      period_start: now,
      period_end: now,
      total_requests: 0,
      total_tokens: 0,
      active_tenants: 0,
    });
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

  // --- v0.4 platform model stubs -------------------------------------------

  listOrganizations(): Promise<{ organizations: Organization[] }> {
    return delay({ organizations: [] });
  },

  createOrganization(data): Promise<Organization> {
    return delay({
      id: `org-${Math.random().toString(36).slice(2, 10)}`,
      name: data.name,
      slug: data.slug,
      description: data.description,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }, 400);
  },

  getOrganization(id): Promise<Organization> {
    return delay({
      id,
      name: 'Mock Org',
      slug: 'mock-org',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }, 200);
  },

  deleteOrganization(): Promise<void> {
    return delay(undefined, 350);
  },

  listTeams(): Promise<{ teams: Team[] }> {
    return delay({ teams: [] });
  },

  createTeam(orgId, data): Promise<Team> {
    return delay({
      id: `team-${Math.random().toString(36).slice(2, 10)}`,
      org_id: orgId,
      name: data.name,
      slug: data.slug,
      description: data.description,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }, 400);
  },

  getTeam(id): Promise<Team> {
    return delay({
      id,
      org_id: 'mock-org',
      name: 'Mock Team',
      slug: 'mock-team',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }, 200);
  },

  deleteTeam(): Promise<void> {
    return delay(undefined, 350);
  },

  listTeamMembers(): Promise<{ members: TeamMember[] }> {
    return delay({ members: [] });
  },

  addTeamMember(teamId, data): Promise<TeamMember> {
    return delay({
      id: Math.floor(Math.random() * 10000),
      team_id: teamId,
      user_id: data.user_id,
      role_id: data.role_id,
      created_at: new Date().toISOString(),
    }, 400);
  },

  removeTeamMember(): Promise<void> {
    return delay(undefined, 350);
  },

  listNodePools(): Promise<{ pools: NodePool[] }> {
    return delay({ pools: [] });
  },

  createNodePool(data): Promise<NodePool> {
    return delay({
      id: `pool-${Math.random().toString(36).slice(2, 10)}`,
      name: data.name ?? 'Mock Pool',
      description: data.description,
      owner_type: data.owner_type ?? 'platform',
      owner_id: data.owner_id ?? 'platform',
      policy: data.policy ?? 'shared',
      node_ids: [],
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }, 400);
  },

  getNodePool(id): Promise<NodePool> {
    return delay({
      id,
      name: 'Mock Pool',
      owner_type: 'platform',
      owner_id: 'platform',
      policy: 'shared',
      node_ids: [],
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }, 200);
  },

  listPoolNodes(): Promise<{ node_ids: string[] }> {
    return delay({ node_ids: [] });
  },

  assignNodeToPool(): Promise<void> {
    return delay(undefined, 300);
  },

  removeNodeFromPool(): Promise<void> {
    return delay(undefined, 300);
  },

  listPoolQuotas(): Promise<{ quotas: PoolTeamQuota[] }> {
    return delay({ quotas: [] });
  },

  upsertPoolQuota(poolId, teamId, quota): Promise<PoolTeamQuota> {
    return delay({
      pool_id: poolId,
      team_id: teamId,
      max_deployments: quota.max_deployments ?? 10,
      max_gpu_nodes: quota.max_gpu_nodes ?? 4,
      priority: quota.priority ?? 1,
    }, 350);
  },

  getMe(): Promise<{ actor: string; orgs: Organization[]; teams: Team[] }> {
    return delay({ actor: 'mock-user', orgs: [], teams: [] }, 200);
  },

  getMyTeamPermissions(teamId): Promise<EffectivePermissions> {
    return delay({
      user_id: 'mock-user',
      team_id: teamId,
      org_id: 'mock-org',
      permissions: ['read', 'deploy'],
      is_org_admin: false,
    }, 200);
  },

  // --- inference audit ---

  listInferenceAudit(params = {}): Promise<InferenceAuditResponse> {
    const now = Date.now();
    const allEvents = [
      { seq: 1, modelId: 'llama3-8b', modelRevision: 'main', modelQuantization: 'Q4_K_M', tenant: 'acme', apiKeyId: 'key-abc123', nodeId: 'node-1', inferenceEngine: 'llamacpp', inputTokens: 512, outputTokens: 128, latencyMs: 1240, status: 'ok', createdAt: new Date(now - 3600000).toISOString(), hash: 'abc123', prevHash: '000000' },
      { seq: 2, modelId: 'qwen3-235b', modelRevision: 'main', modelQuantization: 'Q8_0', tenant: 'beta', apiKeyId: 'key-def456', nodeId: 'node-2', inferenceEngine: 'llamacpp', inputTokens: 1024, outputTokens: 256, latencyMs: 3800, status: 'ok', createdAt: new Date(now - 1800000).toISOString(), hash: 'def456', prevHash: 'abc123' },
      { seq: 3, modelId: 'llama3-8b', modelRevision: 'main', modelQuantization: 'Q4_K_M', tenant: 'acme', apiKeyId: 'key-abc123', nodeId: 'node-1', inferenceEngine: 'llamacpp', inputTokens: 200, outputTokens: 50, latencyMs: 640, status: 'error', createdAt: new Date(now - 600000).toISOString(), hash: 'ghi789', prevHash: 'def456' },
    ];
    const { limit = 50, offset = 0 } = params;
    const filtered = allEvents.filter((e) => {
      if (params.modelId && e.modelId !== params.modelId) return false;
      if (params.tenant && e.tenant !== params.tenant) return false;
      return true;
    });
    return delay({ events: filtered.slice(offset, offset + limit), total: filtered.length });
  },

  verifyAuditChain(): Promise<ChainVerifyResponse> {
    return delay({
      verified: true,
      blockCount: 1420,
      lastVerifiedAt: new Date().toISOString(),
      brokenAtSeq: null,
    });
  },

  listAccessLog(params = {}): Promise<AccessLogResponse> {
    const now = Date.now();
    const allEntries = [
      { id: 4812, apiKeyId: 'key-abc123', method: 'POST', path: '/v1/chat/completions', ipPrefix: '10.0.1.0/24', userAgent: 'python-httpx/0.27.2', statusCode: 200, requestAt: new Date(now - 3600000).toISOString() },
      { id: 4813, apiKeyId: 'key-def456', method: 'POST', path: '/v1/chat/completions', ipPrefix: '192.168.0.0/24', userAgent: 'curl/8.7.1', statusCode: 401, requestAt: new Date(now - 1800000).toISOString() },
    ];
    const { limit = 50, apiKeyId } = params;
    const filtered = apiKeyId ? allEntries.filter((e) => e.apiKeyId === apiKeyId) : allEntries;
    return delay({ entries: filtered.slice(0, limit), count: filtered.length });
  },
};
