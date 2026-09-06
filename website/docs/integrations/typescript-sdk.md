# TypeScript / Node.js SDK

The `@purser/sdk` package provides a fully-typed TypeScript client for the Purser
control-plane management API.  It covers every management endpoint — nodes, models,
deployments, plans, API keys, cluster health, and enterprise features — using only the
built-in `fetch` API (Node 18+ / modern browsers).  No runtime dependencies.

## Installation

```bash
npm install @purser/sdk
```

Requires **Node 18+** (for built-in `fetch` and `AbortSignal.timeout`).

## Quickstart

### Connect to the cluster

```typescript
import { PurserClient } from '@purser/sdk';

// Without authentication (development / single-node)
const client = new PurserClient('http://localhost:8080');

// With an API key
const client = new PurserClient('http://localhost:8080', 'psk_...');

// Custom timeout (milliseconds, default 30 000)
const client = new PurserClient('http://localhost:8080', 'psk_...', 60_000);
```

### List nodes

```typescript
const nodes = await client.listNodes();
for (const node of nodes) {
  console.log(node.hostname, node.state, `${node.vram_gb} GB VRAM`);
}
```

### Register and deploy a model

```typescript
import { PurserClient, ModelSpec } from '@purser/sdk';

const client = new PurserClient('http://localhost:8080', 'psk_...');

// Register the model in the catalog
const spec: ModelSpec = {
  modelId: 'llama-3-8b',
  family: 'llama',
  architecture: 'llama',
  paramsTotalB: 8.0,
  engine: 'vllm',
};
const model = await client.createModel(spec);
console.log('Registered:', model.id);

// Optional: dry-run the planner before committing
const preview = await client.previewPlan(model.id);
if (preview.feasible) {
  console.log('Plan fits the fleet; cost score:', preview.cost);
} else {
  console.log('Model does not fit:', preview.reason);
}

// Deploy (auto-plan — the server generates a plan from the current fleet)
const deploy = await client.deployModel(model.id);
console.log('Deployment accepted:', deploy.deployment_id);
```

### Stream live metrics

```typescript
for await (const snapshot of client.streamMetrics()) {
  console.log(snapshot);
  // break when done to close the stream
}
```

`streamMetrics()` returns an `AsyncGenerator` that yields one JSON object per
Server-Sent Event.  Stop iterating (e.g. `break`) to close the connection.

## Error handling

All API errors throw a typed subclass of `PurserError`:

| HTTP status | Exception class       | When                                         |
|-------------|-----------------------|----------------------------------------------|
| 404         | `NotFoundError`       | Resource not found                           |
| 409         | `ConflictError`       | Resource in use (e.g. node has deployments)  |
| 402         | `LicenseRequiredError`| Enterprise feature accessed without license  |
| other       | `PurserError`         | Base class; carries `statusCode` and `errorType` |

```typescript
import { PurserClient, NotFoundError, ConflictError, LicenseRequiredError } from '@purser/sdk';

try {
  await client.deleteNode('node-xyz');
} catch (err) {
  if (err instanceof ConflictError) {
    console.error('Node still has active deployments:', err.message);
  } else if (err instanceof NotFoundError) {
    console.error('Node not found');
  } else {
    throw err;
  }
}

try {
  const log = await client.auditLog();
} catch (err) {
  if (err instanceof LicenseRequiredError) {
    console.error(`Enterprise feature "${err.feature}" requires a license`);
  }
}
```

## Full API reference

### `PurserClient(baseUrl, apiKey?, timeout?)`

| Parameter | Type     | Default    | Description                                |
|-----------|----------|------------|--------------------------------------------|
| `baseUrl` | `string` | —          | Control-plane URL, e.g. `http://host:8080` |
| `apiKey`  | `string` | `undefined`| API key sent as `Authorization: Bearer`    |
| `timeout` | `number` | `30_000`   | Request timeout in milliseconds            |

### Nodes

| Method                          | Returns          | Description                                        |
|---------------------------------|------------------|----------------------------------------------------|
| `listNodes()`                   | `Promise<Node[]>`| All enrolled nodes                                 |
| `getNode(nodeId)`               | `Promise<Node>`  | Single node by ID                                  |
| `drainNode(nodeId)`             | `Promise<void>`  | Mark node DRAINING (no new deployments scheduled)  |
| `restartNode(nodeId)`           | `Promise<void>`  | Tear down and re-provision node's deployments      |
| `deleteNode(nodeId)`            | `Promise<void>`  | Decommission node (must have no active deployments)|

### Models

| Method                              | Returns              | Description                                 |
|-------------------------------------|----------------------|---------------------------------------------|
| `listModels()`                      | `Promise<Model[]>`   | All catalog entries (with optional fit data)|
| `createModel(spec)`                 | `Promise<Model>`     | Register a new model                        |
| `getModel(modelId)`                 | `Promise<Model>`     | Single model by ID                          |
| `deleteModel(modelId)`              | `Promise<void>`      | Remove from catalog (guards active deploys) |
| `previewPlan(modelId)`              | `Promise<PlanPreview>`| Dry-run plan (not persisted, not deployed) |
| `deployModel(modelId, planId?)`     | `Promise<DeployResponse>` | Deploy a model to the fleet          |
| `getModelHealth(modelId)`           | `Promise<ModelHealth>` | Health info for a deployed model          |

### Deployments

| Method                              | Returns                  | Description                   |
|-------------------------------------|--------------------------|-------------------------------|
| `listDeployments()`                 | `Promise<Deployment[]>`  | All deployments (inc. history)|
| `deleteDeployment(deploymentId)`    | `Promise<void>`          | Tear down a deployment        |
| `getPlan(planId)`                   | `Promise<Plan>`          | Fetch a stored plan by ID     |

### API keys

| Method                                       | Returns            | Description                                    |
|----------------------------------------------|--------------------|------------------------------------------------|
| `listApiKeys()`                              | `Promise<APIKey[]>`| All keys (metadata only, no plaintext)         |
| `createApiKey(name, options?)`               | `Promise<APIKey>`  | Mint a new key (plaintext returned once only)  |
| `deleteApiKey(keyId)`                        | `Promise<void>`    | Permanently revoke a key                       |
| `getKeyUsage(keyId)`                         | `Promise<KeyUsage>`| Usage statistics for a key                     |

`createApiKey` options:

| Option   | Type     | Description                      |
|----------|----------|----------------------------------|
| `tenant` | `string` | Tenant tag for multi-tenant setups |
| `quota`  | `number` | Request quota limit (0 = unlimited) |
| `role`   | `string` | RBAC role (enterprise only)       |

### Cluster

| Method                          | Returns               | Description                             |
|---------------------------------|-----------------------|-----------------------------------------|
| `createJoinToken(ttlSeconds?)`  | `Promise<JoinToken>`  | Mint a single-use node join token       |
| `clusterHealth()`               | `Promise<ClusterHealth>`| DB reachability + node count summary  |

### Enterprise

| Method                    | Returns                    | Description                                        |
|---------------------------|----------------------------|----------------------------------------------------|
| `enterpriseStatus()`      | `Promise<EnterpriseStatus>`| Edition and license info (always returns, no 402)  |
| `auditLog(limit?)`        | `Promise<AuditLog>`        | Tamper-evident audit log with chain verification   |

### Streaming

| Method             | Returns                  | Description                              |
|--------------------|--------------------------|------------------------------------------|
| `streamMetrics()`  | `AsyncGenerator<unknown>`| Live cluster metrics via Server-Sent Events |

## Type reference

```typescript
// Nodes
interface Node {
  id: string; hostname: string; state: NodeState;
  os?: string; arch?: string; ram_gb?: number; vram_gb?: number;
  advertised_agent_addr?: string; advertised_inference_addr?: string;
  last_seen?: string; hardware_profile?: Record<string, unknown>;
  created_at?: string; updated_at?: string;
}
type NodeState = 'NODE_STATE_ENROLLED' | 'NODE_STATE_READY' | 'NODE_STATE_RUNNING'
               | 'NODE_STATE_DRAINING' | 'NODE_STATE_DECOMMISSIONED' | string;

// Models
interface ModelSpec { modelId: string; family?: string; architecture?: string;
  paramsTotalB?: number; engine?: string; [key: string]: unknown; }
interface Model { id: string; family: string; architecture?: string;
  params_total_b?: number; engine?: string; spec?: Record<string, unknown>;
  created_at?: string; updated_at?: string; }
type PlanPreview = PlanPreviewFeasible | PlanPreviewInfeasible;
interface PlanPreviewFeasible extends Plan { feasible: true; }
interface PlanPreviewInfeasible { feasible: false; reason: string; }

// Plans
interface Plan { id: string; model_id: string; quantization?: string;
  cost?: number; plan?: Record<string, unknown>; created_at?: string; }

// Deployments
interface Deployment { id: string; model_id: string; state: DeploymentState;
  plan_id?: string; detail?: Record<string, unknown>;
  created_at?: string; updated_at?: string; }
type DeploymentState = 'DEPLOYMENT_STATE_PLANNED' | 'DEPLOYMENT_STATE_PROVISIONING'
  | 'DEPLOYMENT_STATE_ACTIVE' | 'DEPLOYMENT_STATE_REBALANCING'
  | 'DEPLOYMENT_STATE_STOPPING' | 'DEPLOYMENT_STATE_STOPPED'
  | 'DEPLOYMENT_STATE_FAILED' | string;

// API keys
interface APIKey { id: string; name: string; tenant?: string; quota?: number;
  enabled?: boolean; key?: string; created_at?: string; updated_at?: string; }

// Cluster
interface JoinToken { token: string; expires_at: string; cluster_id: string; }
interface ClusterHealth { status: string; total_nodes: number;
  ready_nodes: number; checked_at: string; }

// Enterprise
interface EnterpriseStatus { edition: string; licensee: string;
  features: string[]; expires?: string; }
interface AuditEntry { seq: number; time_unix_nano: number;
  actor: string; action: string; target: string;
  details?: Record<string, string>; prev_hash: string; hash: string; }
interface AuditLog { feature: string; licensee: string;
  entries: AuditEntry[]; chain: ChainVerification; }
interface ChainVerification { verified: boolean; length: number; break?: ChainBreak; }
```
