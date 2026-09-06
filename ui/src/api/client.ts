// ---------------------------------------------------------------------------
// The single seam between the UI and the backend.
//
// `PurserApi` is the management-plane contract (`/api/v1/...`). It is backed by
// either the real HTTP client (./http `createHttpApi`, the DEFAULT) or an
// in-memory mock (../mock/wiring), chosen ONCE here from ./config:
//   - default / VITE_PURSER_MOCK unset -> real `fetch('/api/v1/...')` client.
//   - mock explicitly opted in         -> in-memory fixtures (dev/offline demo).
// Mock is strictly opt-in, so a shipped image talks to the real control plane
// unless an operator asks for it (see ./config for the flags).
//
// The mock is pulled in with a DYNAMIC `import()` gated on the opt-in flag, so
// its fixtures land in a separate chunk that a default (real) build never
// loads. The `await` runs at module init, before any importer touches `api` /
// `makeChat`, so both stay synchronous for callers.
// Every page, hook and component keeps working unchanged across the swap.
// ---------------------------------------------------------------------------
import type {
  ApiKey,
  ApiKeyWithSecret,
  AuditLog,
  BillingReport,
  BillingSummary,
  CatalogEntry,
  ClusterCapacity,
  DeployOverrides,
  Deployment,
  DeploymentApproval,
  DeploymentPlan,
  EnterpriseStatus,
  ImportSource,
  JoinInfo,
  JoinTokenResult,
  KeyUsage,
  MetricsStreamHandlers,
  ModelHealth,
  ModelSpec,
  NodeView,
  PlanPreviewResult,
  ReconcilerStatus,
  UsageSummary,
} from './types';
import { config } from './config';
import { createChatClient, fetchOpenAIModels, makeSseChatTransport, type ChatClient } from './openai';
import { createHttpApi } from './http';

export interface CreateApiKeyInput {
  name: string;
  team: string;
  monthlyQuota: number | null;
  /** RBAC role; defaults to "admin" on the server if omitted. */
  role?: 'admin' | 'viewer' | 'inference';
}

export interface PurserApi {
  // --- fleet ---
  getCapacity(): Promise<ClusterCapacity>;
  listNodes(): Promise<NodeView[]>;
  getNode(nodeId: string): Promise<NodeView>;
  drainNode(nodeId: string): Promise<NodeView>;
  restartNode(nodeId: string): Promise<NodeView>;
  removeNode(nodeId: string): Promise<void>;

  // --- catalog ---
  getCatalog(): Promise<CatalogEntry[]>;
  getModel(modelId: string): Promise<ModelSpec>;
  /** POST /api/v1/models/import — inspect and register a model from an external registry. */
  importModel(source: ImportSource): Promise<ModelSpec>;
  /** POST /api/v1/models/{id}/plan — dry-run plan; returns feasibility + split diagram. */
  previewModelPlan(modelId: string): Promise<PlanPreviewResult>;
  /** GET /api/v1/models/{id}/health — operational health of a deployed model. */
  getModelHealth(modelId: string): Promise<ModelHealth>;
  /** DELETE /api/v1/models/{id} — remove a model from the catalog. 409 if it has active deployments. */
  deleteModel(modelId: string): Promise<void>;

  // --- deployments ---
  planDeployment(modelId: string, overrides: DeployOverrides): Promise<DeploymentPlan>;
  createDeployment(modelId: string, overrides: DeployOverrides): Promise<Deployment>;
  listDeployments(): Promise<Deployment[]>;
  getDeployment(id: string): Promise<Deployment>;
  undeployDeployment(id: string): Promise<void>;
  getPlan(planId: string): Promise<DeploymentPlan>;

  // --- onboarding ---
  getJoinInfo(): Promise<JoinInfo>;
  rotateJoinToken(): Promise<JoinInfo>;
  /** POST /api/v1/join-token — generate a new TTL-scoped join token on demand. */
  createJoinToken(ttlSeconds: number): Promise<JoinTokenResult>;

  // --- settings / api keys ---
  listApiKeys(): Promise<ApiKey[]>;
  createApiKey(input: CreateApiKeyInput): Promise<ApiKeyWithSecret>;
  revokeApiKey(id: string): Promise<ApiKey>;

  // --- usage ---
  /** GET /api/v1/apikeys/{id}/usage — token and request counters for one key. */
  getKeyUsage(keyId: string): Promise<KeyUsage>;
  /** GET /api/v1/usage/summary — cross-tenant usage totals. */
  getUsageSummary(): Promise<UsageSummary>;

  // --- enterprise ---
  /** GET /api/v1/enterprise/status — edition, licensee, features, expiry. */
  getEnterpriseStatus(): Promise<EnterpriseStatus>;

  // --- live metrics (SSE) ---
  /** Subscribe to GET /api/v1/metrics; returns an unsubscribe/close function. */
  streamMetrics(handlers: MetricsStreamHandlers): () => void;

  // --- enterprise ---
  /** GET /api/v1/enterprise/audit-log — 402 without a valid license. */
  getAuditLog(limit?: number): Promise<AuditLog>;

  // --- reconciler ---
  /** GET /api/v1/reconciler/status — live reconciler config + pending event tracker. */
  getReconcilerStatus(): Promise<ReconcilerStatus>;

  // --- deployment approvals (AI Act Art.14) ---
  /** GET /api/v1/approvals — 402 without deployment_approvals feature. */
  listDeploymentApprovals(status?: string, limit?: number): Promise<DeploymentApproval[]>;
  /** GET /api/v1/approvals/{id} — single approval record. */
  getDeploymentApproval(deploymentId: string): Promise<DeploymentApproval>;
  /** POST /api/v1/approvals/{id}/approve — admin-only. */
  approveDeployment(deploymentId: string, notes?: string): Promise<DeploymentApproval>;
  /** POST /api/v1/approvals/{id}/reject — admin-only. */
  rejectDeployment(deploymentId: string, notes?: string): Promise<DeploymentApproval>;

  // --- billing / chargeback ---
  /**
   * GET /api/v1/billing/report — 402 without the "billing" feature.
   * Returns chargeback report grouped by tenant+model for the given window.
   */
  getBillingReport(start: string, end: string, tenantId?: string): Promise<BillingReport>;
  /**
   * Returns the URL for CSV download (format=csv). Callers create a link and
   * navigate to it directly — no fetch needed.
   */
  getBillingCsvUrl(start: string, end: string, tenantId?: string): string;
  /** GET /api/v1/billing/summary — quick stats, not enterprise-gated. */
  getBillingSummary(tenantId?: string): Promise<BillingSummary>;
}

// The mock fixtures live behind a dynamic import so they are code-split out of
// the default (real) bundle. `mock` is null unless mock mode was opted into.
const mock = config.mock ? await import('../mock/wiring') : null;

/**
 * The active management-plane implementation. Selected from config at load time
 * (real HTTP client by default; the in-memory mock only when opted in).
 */
export const api: PurserApi = mock ? mock.mockBackend : createHttpApi(config.apiBase);

// ---------------------------------------------------------------------------
// Playground chat client (OpenAI-compatible Gateway, `/v1/...`).
//
// `makeChat(apiKey)` builds a client bound to the configured Gateway base URL
// and the caller-supplied Bearer key. In mock mode it uses the simulated SSE
// transport and lists the models of the active mock deployments; in real mode
// it streams from the Gateway and lists GET /v1/models.
// ---------------------------------------------------------------------------

export function makeChat(apiKey?: string): ChatClient {
  if (mock) {
    return createChatClient({
      baseUrl: config.gatewayBase,
      apiKey,
      transport: mock.mockChatTransport,
      listModels: mock.mockListModels,
    });
  }
  return createChatClient({
    baseUrl: config.gatewayBase,
    apiKey,
    transport: makeSseChatTransport({ baseUrl: config.gatewayBase, apiKey }),
    listModels: () => fetchOpenAIModels(config.gatewayBase, apiKey),
  });
}

/** Default keyless chat client (used for its `baseUrl`; Playground builds its own). */
export const chat = makeChat();
