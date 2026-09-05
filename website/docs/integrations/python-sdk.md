# Python SDK

The `purser-sdk` package provides a typed, synchronous Python client for the
Purser control-plane management API.  It covers every management endpoint —
nodes, models, deployments, plans, API keys, cluster health, and enterprise
features — using only the Python standard library plus
[httpx](https://www.python-httpx.org/).

## Installation

```bash
pip install purser-sdk
```

Requires Python **3.10+**.

## Quickstart

### Connect to the cluster

```python
from purser import PurserClient

# Without authentication (development / single-node)
client = PurserClient("http://localhost:8080")

# With an API key
client = PurserClient("http://localhost:8080", api_key="psk_...")

# As a context manager (recommended — closes the connection pool on exit)
with PurserClient("http://localhost:8080", api_key="psk_...") as client:
    health = client.cluster_health()
    print(health.status)          # "ok" | "degraded" | "empty" | "unavailable"
    print(health.ready_nodes)     # int
```

### List nodes

```python
nodes = client.list_nodes()
for node in nodes:
    print(node.hostname, node.state, f"{node.vram_gb} GB VRAM")
```

### Register a model

```python
from purser import ModelSpec

spec = ModelSpec(
    model_id="llama3-8b",
    family="llama",
    architecture="transformer",
    params_total_b=8.0,
    engine="llama.cpp",
)
model = client.create_model(spec)
print("Registered:", model.id)
```

### Preview a deployment plan (dry run)

```python
preview = client.preview_plan("llama3-8b")
if preview.feasible:
    print("Plan:", preview.plan.id, "cost:", preview.plan.cost)
else:
    print("Model does not fit:", preview.reason)
```

### Deploy a model

```python
# Let the planner choose the plan automatically
deployment = client.deploy_model("llama3-8b")
print(deployment.id, deployment.state)   # "provisioning"

# Or supply a specific plan ID
deployment = client.deploy_model("llama3-8b", plan_id="plan-001-ab12")
```

### Call the OpenAI-compatible inference endpoint

Once the deployment is active, forward requests through the Purser gateway
using any OpenAI-compatible client.  Pass the gateway URL as `base_url` and a
Purser API key as the bearer token:

```python
from openai import OpenAI

# Mint a gateway key
key = client.create_api_key("my-app", tenant="acme")
print("Save this key — it will not be shown again:", key.key)

# Use it with the OpenAI client
openai_client = OpenAI(
    base_url="http://localhost:9090/v1",   # Purser gateway
    api_key=key.key,
)
response = openai_client.chat.completions.create(
    model="llama3-8b",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(response.choices[0].message.content)
```

See the [LiteLLM integration](./litellm.md) guide for routing through LiteLLM
as a proxy layer.

## Full API reference

### `PurserClient`

```python
PurserClient(
    base_url: str,
    api_key: str | None = None,
    timeout: float = 30.0,
)
```

A trailing slash in `base_url` is normalised away.

---

#### Nodes

```python
client.list_nodes() -> list[Node]
```
Return all enrolled nodes.

```python
client.get_node(node_id: str) -> Node
```
Fetch a single node by ID.  Raises `NotFoundError` if not found.

```python
client.drain_node(node_id: str) -> None
```
Cordon a node: mark it `DRAINING` so no new work is scheduled onto it.
Existing deployments on the node are **not** migrated automatically.

```python
client.delete_node(node_id: str) -> None
```
Decommission a node (lifecycle transition — the node remains visible in
`list_nodes()` with state `DECOMMISSIONED`).  Raises `ConflictError` if active
deployments still occupy the node.

---

#### Models

```python
client.list_models() -> list[Model]
```
Return all models in the catalog.  When the server has a Planner configured,
each model includes a `fit` annotation.

```python
client.create_model(spec: ModelSpec) -> Model
```
Register a new model.  Raises `ConflictError` if a model with the same ID
already exists.

```python
client.get_model(model_id: str) -> Model
```
Fetch a single model by ID.  Raises `NotFoundError` if not found.

```python
client.delete_model(model_id: str) -> None
```
Remove a model from the catalog.  Raises `ConflictError` if active deployments
still reference it.

```python
client.preview_plan(model_id: str) -> PlanPreview
```
Dry-run deployment planning.  Returns a `PlanPreview` with `feasible=True` and
the plan details, or `feasible=False` and a `reason` string explaining the
resource deficit.  Never persists or deploys anything.

```python
client.deploy_model(model_id: str, plan_id: str | None = None) -> Deployment
```
Deploy a model.  The planner computes a plan automatically unless `plan_id` is
supplied.

---

#### Deployments

```python
client.list_deployments() -> list[Deployment]
```
Return all deployments (active and historical).

```python
client.delete_deployment(deployment_id: str) -> None
```
Tear down a deployment.  Raises `NotFoundError` if not found.

```python
client.get_plan(plan_id: str) -> Plan
```
Fetch a stored deployment plan by ID.

---

#### API keys

```python
client.list_api_keys() -> list[APIKey]
```
Return all gateway API keys.  The plaintext key is **never** re-exposed.

```python
client.create_api_key(
    name: str,
    tenant: str = "",
    quota: int = 0,
) -> APIKey
```
Mint a new gateway key.  The plaintext key is in `result.key` and is returned
**exactly once** — store it immediately.

```python
client.delete_api_key(key_id: str) -> None
```
Permanently revoke an API key.

---

#### Cluster

```python
client.create_join_token(ttl_seconds: int = 86400) -> JoinToken
```
Mint a single-use, expiring enrollment token.  Pass the `token` to a new
machine via `PURSER_JOIN_TOKEN` so its Purser agent can enroll.

```python
client.cluster_health() -> ClusterHealth
```
Returns a `ClusterHealth` with `status`, `total_nodes`, `ready_nodes`, and
`checked_at`.

---

#### Enterprise (requires license)

```python
client.enterprise_status() -> EnterpriseStatus
```
Returns edition, licensee, and enabled features.  Always succeeds — reports
`"community"` when no license is active.

```python
client.audit_log(limit: int = 100) -> AuditLog
```
Returns the most recent audit entries with hash-chain verification.  Raises
`LicenseRequiredError` when no enterprise license with the `"audit"` feature is
active.

## Error handling

All methods raise a subclass of `PurserError` on non-2xx responses.

```python
from purser import (
    PurserError,
    NotFoundError,
    ConflictError,
    LicenseRequiredError,
)

try:
    client.delete_model("llama3-8b")
except NotFoundError:
    # HTTP 404
    print("Model does not exist")
except ConflictError as e:
    # HTTP 409 — e.message lists the blocking deployment IDs
    print("Still in use:", e.message)
except LicenseRequiredError as e:
    # HTTP 402
    print("Need enterprise license for feature:", e.feature)
except PurserError as e:
    # Any other non-2xx response
    print(f"Error {e.status_code}: {e.message}  (type={e.error_type})")
```

### Exception hierarchy

| Exception | HTTP status | When |
|---|---|---|
| `PurserError` | any 4xx/5xx | Base class |
| `NotFoundError` | 404 | Resource does not exist |
| `ConflictError` | 409 | Duplicate creation or resource in use |
| `LicenseRequiredError` | 402 | Enterprise feature without valid license |

## Types

| Type | Description |
|---|---|
| `Node` | Enrolled cluster node |
| `Model` | Catalog model entry |
| `ModelSpec` | Input for `create_model()` |
| `Plan` | Stored deployment plan |
| `PlanPreview` | Result of `preview_plan()` |
| `Deployment` | Active or historical deployment |
| `APIKey` | Gateway credential |
| `JoinToken` | Node enrollment token |
| `ClusterHealth` | Cluster health snapshot |
| `EnterpriseStatus` | License and edition info |
| `AuditEntry` | Single audit log entry |
| `AuditChain` | Chain verification summary |
| `AuditLog` | Full `audit_log()` response |

## Development

```bash
cd sdk/python
pip install -e ".[dev]"
python -m pytest
```
