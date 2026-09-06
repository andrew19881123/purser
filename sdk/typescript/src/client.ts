/**
 * PurserClient — fetch-based TypeScript client for the Purser management API.
 *
 * Uses the built-in `fetch` (Node 18+ / browser) — no runtime dependencies.
 */

import {
  APIKey,
  AuditLog,
  ClusterHealth,
  Deployment,
  DeployResponse,
  EnterpriseStatus,
  JoinToken,
  KeyUsage,
  Model,
  ModelHealth,
  ModelSpec,
  Plan,
  PlanPreview,
  Node,
} from './types';
import { ConflictError, LicenseRequiredError, NotFoundError, PurserError } from './errors';

/** Options for createApiKey(). */
export interface CreateApiKeyOptions {
  tenant?: string;
  quota?: number;
  role?: string;
}

export class PurserClient {
  private readonly baseUrl: string;

  /**
   * Create a new PurserClient.
   *
   * @param baseUrl - Base URL of the Purser control plane, e.g. `"http://localhost:8080"`.
   *                  Trailing slashes are stripped.
   * @param apiKey  - Optional API key sent as `Authorization: Bearer <key>`.
   * @param timeout - Request timeout in milliseconds (default 30 000).
   */
  constructor(
    baseUrl: string,
    private readonly apiKey?: string,
    private readonly timeout = 30_000,
  ) {
    this.baseUrl = baseUrl.replace(/\/+$/, '');
  }

  // -------------------------------------------------------------------------
  // Internal helpers
  // -------------------------------------------------------------------------

  private headers(): Record<string, string> {
    const h: Record<string, string> = { 'Content-Type': 'application/json' };
    if (this.apiKey) {
      h['Authorization'] = `Bearer ${this.apiKey}`;
    }
    return h;
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
  ): Promise<T> {
    const url = `${this.baseUrl}/api/v1${path}`;
    const init: RequestInit = {
      method,
      headers: this.headers(),
      signal: AbortSignal.timeout(this.timeout),
    };
    if (body !== undefined) {
      init.body = JSON.stringify(body);
    }
    const response = await fetch(url, init);
    return this.handleResponse<T>(response);
  }

  private async handleResponse<T>(response: Response): Promise<T> {
    if (response.status === 204) {
      return undefined as unknown as T;
    }

    if (response.ok) {
      return (await response.json()) as T;
    }

    // Parse the error payload.
    let message: string = response.statusText;
    let errorType: string | undefined;
    let feature: string | undefined;

    try {
      const data = (await response.json()) as Record<string, unknown>;
      const errorField = data['error'];

      if (errorField !== null && typeof errorField === 'object') {
        // 402 shape: {"error": {"message": ..., "feature": ..., "type": ...}}
        const errObj = errorField as Record<string, string>;
        message = errObj['message'] ?? message;
        errorType = errObj['type'];
        feature = errObj['feature'];
      } else {
        // Normal shape: {"error": "kind", "message": "..."}
        errorType = typeof errorField === 'string' ? errorField : undefined;
        const msgField = data['message'];
        if (typeof msgField === 'string') message = msgField;
      }
    } catch {
      // Fall back to status text if JSON parsing fails.
    }

    if (response.status === 404) throw new NotFoundError(message);
    if (response.status === 409) throw new ConflictError(message);
    if (response.status === 402) throw new LicenseRequiredError(message, feature);
    throw new PurserError(response.status, message, errorType);
  }

  // -------------------------------------------------------------------------
  // Nodes
  // -------------------------------------------------------------------------

  /** Return all nodes known to the cluster. */
  async listNodes(): Promise<Node[]> {
    const data = await this.request<{ nodes: Node[] }>('GET', '/nodes');
    return data.nodes ?? [];
  }

  /**
   * Return a single node by ID.
   * @throws {NotFoundError} if no node with that ID exists.
   */
  async getNode(nodeId: string): Promise<Node> {
    return this.request<Node>('GET', `/nodes/${nodeId}`);
  }

  /**
   * Cordon a node (mark it DRAINING) so no new deployments are scheduled onto it.
   * Existing workloads on the node are NOT migrated.
   * @throws {NotFoundError} if no node with that ID exists.
   */
  async drainNode(nodeId: string): Promise<void> {
    await this.request<void>('POST', `/nodes/${nodeId}/drain`);
  }

  /**
   * Tear down all active deployments on a node and let the reconciler re-provision them.
   * The node process itself is not rebooted.
   * @throws {NotFoundError} if no node with that ID exists.
   * @throws {ConflictError} if there are no active deployments to restart.
   */
  async restartNode(nodeId: string): Promise<void> {
    await this.request<void>('POST', `/nodes/${nodeId}/restart`);
  }

  /**
   * Decommission a node (lifecycle transition to DECOMMISSIONED).
   * The node must have no active deployments.
   * @throws {NotFoundError} if no node with that ID exists.
   * @throws {ConflictError} if the node still hosts active deployments.
   */
  async deleteNode(nodeId: string): Promise<void> {
    await this.request<void>('DELETE', `/nodes/${nodeId}`);
  }

  // -------------------------------------------------------------------------
  // Models
  // -------------------------------------------------------------------------

  /** Return all models in the catalog, each optionally annotated with a planner fit verdict. */
  async listModels(): Promise<Model[]> {
    const data = await this.request<{ models: Model[] }>('GET', '/models');
    return data.models ?? [];
  }

  /**
   * Register a new model in the catalog.
   * On success the server returns `{"model_id": "..."}` (201); this method
   * performs a follow-up GET to return the full model object.
   * @throws {ConflictError} if a model with the same ID already exists.
   */
  async createModel(spec: ModelSpec): Promise<Model> {
    const data = await this.request<{ model_id: string }>('POST', '/models', spec);
    const modelId = data.model_id ?? (spec.modelId as string);
    return this.getModel(modelId);
  }

  /**
   * Return a single model by ID.
   * @throws {NotFoundError} if no model with that ID exists.
   */
  async getModel(modelId: string): Promise<Model> {
    return this.request<Model>('GET', `/models/${modelId}`);
  }

  /**
   * Remove a model from the catalog.
   * The model must have no active deployments referencing it.
   * @throws {NotFoundError} if no model with that ID exists.
   * @throws {ConflictError} if active deployments still reference the model.
   */
  async deleteModel(modelId: string): Promise<void> {
    await this.request<void>('DELETE', `/models/${modelId}`);
  }

  /**
   * Dry-run plan preview — compute a deployment plan without persisting or deploying.
   * Returns `{ feasible: true, ...plan }` when the model fits the fleet, or
   * `{ feasible: false, reason: string }` when it does not.
   * @throws {NotFoundError} if no model with that ID exists.
   */
  async previewPlan(modelId: string): Promise<PlanPreview> {
    const data = await this.request<Record<string, unknown>>(
      'POST',
      `/models/${modelId}/plan`,
    );
    if (data['feasible'] === true) {
      return { feasible: true, ...(data as unknown as Omit<PlanPreview & { feasible: true }, 'feasible'>) } as PlanPreview;
    }
    return {
      feasible: false,
      reason: (data['reason'] as string) ?? '',
    };
  }

  /**
   * Deploy a model to the fleet.
   * Provide `planId` to use a stored plan, or omit to let the server generate one.
   * @throws {NotFoundError} if the model or plan ID does not exist.
   */
  async deployModel(modelId: string, planId?: string): Promise<DeployResponse> {
    const body: Record<string, string> = {};
    if (planId) body['plan_id'] = planId;
    return this.request<DeployResponse>('POST', `/models/${modelId}/deploy`, body);
  }

  /**
   * Return health information for a deployed model.
   * @throws {NotFoundError} if no model with that ID exists.
   */
  async getModelHealth(modelId: string): Promise<ModelHealth> {
    return this.request<ModelHealth>('GET', `/models/${modelId}/health`);
  }

  // -------------------------------------------------------------------------
  // Deployments
  // -------------------------------------------------------------------------

  /** Return all deployments (active and historical). */
  async listDeployments(): Promise<Deployment[]> {
    const data = await this.request<{ deployments: Deployment[] }>(
      'GET',
      '/deployments',
    );
    return data.deployments ?? [];
  }

  /**
   * Tear down a deployment (transitions to STOPPING asynchronously).
   * @throws {NotFoundError} if no deployment with that ID exists.
   */
  async deleteDeployment(deploymentId: string): Promise<void> {
    await this.request<void>('DELETE', `/deployments/${deploymentId}`);
  }

  /**
   * Return a stored deployment plan by ID.
   * @throws {NotFoundError} if no plan with that ID exists (note: preview plans are ephemeral).
   */
  async getPlan(planId: string): Promise<Plan> {
    return this.request<Plan>('GET', `/plans/${planId}`);
  }

  // -------------------------------------------------------------------------
  // API keys
  // -------------------------------------------------------------------------

  /** Return all API keys (metadata only — plaintext keys are never re-exposed). */
  async listApiKeys(): Promise<APIKey[]> {
    const data = await this.request<{ apikeys: APIKey[] }>('GET', '/apikeys');
    return data.apikeys ?? [];
  }

  /**
   * Mint a new gateway API key.
   * The plaintext key (`key` field on the returned object) is returned exactly once —
   * save it immediately as the server never re-exposes it.
   *
   * @param name    - Human-readable name for the key.
   * @param options - Optional `tenant`, `quota` (0 = unlimited), and `role` fields.
   */
  async createApiKey(name: string, options: CreateApiKeyOptions = {}): Promise<APIKey> {
    const body: Record<string, unknown> = { name };
    if (options.tenant !== undefined) body['tenant'] = options.tenant;
    if (options.quota !== undefined) body['quota'] = options.quota;
    if (options.role !== undefined) body['role'] = options.role;
    return this.request<APIKey>('POST', '/apikeys', body);
  }

  /**
   * Permanently revoke an API key.
   * @throws {NotFoundError} if no key with that ID exists.
   */
  async deleteApiKey(keyId: string): Promise<void> {
    await this.request<void>('DELETE', `/apikeys/${keyId}`);
  }

  /**
   * Return token usage statistics for an API key.
   * @throws {NotFoundError} if no key with that ID exists.
   */
  async getKeyUsage(keyId: string): Promise<KeyUsage> {
    return this.request<KeyUsage>('GET', `/apikeys/${keyId}/usage`);
  }

  // -------------------------------------------------------------------------
  // Cluster
  // -------------------------------------------------------------------------

  /**
   * Mint a single-use, expiring cluster join token.
   * Pass the returned token to a new machine via the `PURSER_JOIN_TOKEN` env var.
   *
   * @param ttlSeconds - Token lifetime in seconds (default 3 600 = 1 h).
   *                     Omit or set <= 0 to use the fleet default.
   */
  async createJoinToken(ttlSeconds?: number): Promise<JoinToken> {
    const body: Record<string, number> = {};
    if (ttlSeconds !== undefined && ttlSeconds > 0) {
      body['ttl_seconds'] = ttlSeconds;
    }
    return this.request<JoinToken>('POST', '/join-token', body);
  }

  /** Return a coarse cluster health summary (DB reachability + node counts). */
  async clusterHealth(): Promise<ClusterHealth> {
    return this.request<ClusterHealth>('GET', '/cluster/health');
  }

  // -------------------------------------------------------------------------
  // Enterprise (requires a valid license for audit-log)
  // -------------------------------------------------------------------------

  /**
   * Return the active edition and license information.
   * Always returns 200 — reports `"community"` when no valid license is present.
   */
  async enterpriseStatus(): Promise<EnterpriseStatus> {
    return this.request<EnterpriseStatus>('GET', '/enterprise/status');
  }

  /**
   * Return the most recent audit-log entries with hash-chain verification.
   * @param limit - Maximum number of entries to return (default 100).
   * @throws {LicenseRequiredError} if no valid enterprise license with the "audit" feature is active.
   */
  async auditLog(limit = 100): Promise<AuditLog> {
    const url = `${this.baseUrl}/api/v1/enterprise/audit-log?limit=${limit}`;
    const response = await fetch(url, {
      method: 'GET',
      headers: this.headers(),
      signal: AbortSignal.timeout(this.timeout),
    });
    return this.handleResponse<AuditLog>(response);
  }

  // -------------------------------------------------------------------------
  // Streaming metrics
  // -------------------------------------------------------------------------

  /**
   * Stream live cluster metrics as an async generator over Server-Sent Events.
   * Each yielded value is a parsed JSON metric snapshot.
   *
   * The stream runs until the caller stops iterating (e.g. `break`) or the
   * server closes the connection.
   *
   * @example
   * ```ts
   * for await (const snapshot of client.streamMetrics()) {
   *   console.log(snapshot);
   *   break; // stop after the first snapshot
   * }
   * ```
   */
  async *streamMetrics(): AsyncGenerator<unknown> {
    const response = await fetch(`${this.baseUrl}/api/v1/metrics`, {
      headers: this.headers(),
      signal: AbortSignal.timeout(this.timeout),
    });

    if (!response.ok || !response.body) {
      await this.handleResponse<never>(response);
      return;
    }

    // response.body is a ReadableStream<Uint8Array>; in Node 18+ it is async-iterable.
    const body = response.body as unknown as AsyncIterable<Uint8Array>;
    const decoder = new TextDecoder();
    let buffer = '';

    for await (const chunk of body) {
      buffer += decoder.decode(chunk, { stream: true });
      const lines = buffer.split('\n');
      // Keep the last (potentially incomplete) line in the buffer.
      buffer = lines.pop() ?? '';
      for (const line of lines) {
        if (line.startsWith('data:')) {
          try {
            yield JSON.parse(line.slice(5).trim());
          } catch {
            // Skip malformed SSE lines.
          }
        }
      }
    }

    // Flush any remaining data.
    if (buffer.startsWith('data:')) {
      try {
        yield JSON.parse(buffer.slice(5).trim());
      } catch {
        // Skip.
      }
    }
  }
}
