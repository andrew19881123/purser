// ---------------------------------------------------------------------------
// The single seam between the UI and the backend.
//
// `PurserApi` is the management-plane contract (`/api/v1/...`). It is backed by
// either an in-memory mock (../mock/backend) or the real HTTP client
// (./http `createHttpApi`), chosen ONCE here from ./config:
//   - VITE_PURSER_MOCK unset / not "0" -> mock (default; build & dev work with
//     no server running).
//   - VITE_PURSER_MOCK=0               -> real `fetch('/api/v1/...')` client.
// Every page, hook and component keeps working unchanged across the swap.
// ---------------------------------------------------------------------------
import type {
  ApiKey,
  ApiKeyWithSecret,
  CatalogEntry,
  ClusterCapacity,
  DeployOverrides,
  Deployment,
  DeploymentPlan,
  JoinInfo,
  MetricsStreamHandlers,
  ModelSpec,
  NodeView,
  OpenAIModel,
} from './types';
import { config } from './config';
import { createChatClient, fetchOpenAIModels, makeSseChatTransport, type ChatClient } from './openai';
import { mockChatTransport } from '../mock/chat';
import { mockBackend } from '../mock/backend';
import { createHttpApi } from './http';

export interface CreateApiKeyInput {
  name: string;
  team: string;
  monthlyQuota: number | null;
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

  // --- settings / api keys ---
  listApiKeys(): Promise<ApiKey[]>;
  createApiKey(input: CreateApiKeyInput): Promise<ApiKeyWithSecret>;
  revokeApiKey(id: string): Promise<ApiKey>;

  // --- live metrics (SSE) ---
  /** Subscribe to GET /api/v1/metrics; returns an unsubscribe/close function. */
  streamMetrics(handlers: MetricsStreamHandlers): () => void;
}

/**
 * The active management-plane implementation. Selected from env at load time.
 */
export const api: PurserApi = config.mock ? mockBackend : createHttpApi(config.apiBase);

// ---------------------------------------------------------------------------
// Playground chat client (OpenAI-compatible Gateway, `/v1/...`).
//
// `makeChat(apiKey)` builds a client bound to the configured Gateway base URL
// and the caller-supplied Bearer key. In mock mode it uses the simulated SSE
// transport and lists the models of the active mock deployments; in real mode
// it streams from the Gateway and lists GET /v1/models.
// ---------------------------------------------------------------------------

/** Mock "served models" = the models of the active mock deployments. */
function mockListModels(): Promise<OpenAIModel[]> {
  return mockBackend.listDeployments().then((deps) => {
    const ids = Array.from(
      new Set(deps.filter((d) => d.state === 'active').map((d) => d.plan.modelId)),
    );
    return ids.map((id) => ({ id, object: 'model' as const, ownedBy: 'purser' }));
  });
}

export function makeChat(apiKey?: string): ChatClient {
  if (config.mock) {
    return createChatClient({
      baseUrl: config.gatewayBase,
      apiKey,
      transport: mockChatTransport,
      listModels: mockListModels,
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
