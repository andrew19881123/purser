# purser-sdk

Python management SDK for [Purser](https://github.com/purser/purser) cluster
administration.  Provides a clean, typed, synchronous client for every
management API endpoint — nodes, models, deployments, API keys, plans, cluster
health, and enterprise features.

## Installation

```bash
pip install purser-sdk
```

Requires Python 3.10+ and [`httpx`](https://www.python-httpx.org/) (installed
automatically).

## Quick start

```python
from purser import PurserClient

with PurserClient("http://localhost:8080", api_key="psk_...") as client:
    # Check cluster health
    health = client.cluster_health()
    print(health.status, health.ready_nodes, "/", health.total_nodes)

    # List nodes
    nodes = client.list_nodes()
    for node in nodes:
        print(node.hostname, node.state, node.vram_gb, "GB VRAM")

    # Register and deploy a model
    from purser import ModelSpec
    spec = ModelSpec(
        model_id="llama3-8b",
        family="llama",
        architecture="transformer",
        params_total_b=8.0,
        engine="llama.cpp",
    )
    model = client.create_model(spec)
    deployment = client.deploy_model(model.id)
    print("Deployment:", deployment.id, deployment.state)
```

## Error handling

```python
from purser import (
    PurserClient,
    NotFoundError,
    ConflictError,
    LicenseRequiredError,
    PurserError,
)

client = PurserClient("http://localhost:8080")

try:
    client.delete_model("my-model")
except NotFoundError:
    print("Model does not exist")
except ConflictError as e:
    print("Model still in use:", e.message)
except LicenseRequiredError as e:
    print("Enterprise feature required:", e.feature)
except PurserError as e:
    print(f"Unexpected error [{e.status_code}]: {e.message}")
```

## API reference

### `PurserClient(base_url, api_key=None, timeout=30.0)`

All methods raise `PurserError` (or a subclass) on non-2xx responses.

| Method | Description |
|---|---|
| `list_nodes()` | Return all enrolled nodes |
| `get_node(node_id)` | Fetch a single node |
| `drain_node(node_id)` | Cordon a node (stop new scheduling) |
| `delete_node(node_id)` | Decommission a node |
| `list_models()` | Return the model catalog |
| `create_model(spec)` | Register a new model |
| `get_model(model_id)` | Fetch a single model |
| `delete_model(model_id)` | Remove a model from the catalog |
| `preview_plan(model_id)` | Dry-run deployment plan |
| `deploy_model(model_id, plan_id=None)` | Deploy a model |
| `list_deployments()` | Return all deployments |
| `delete_deployment(deployment_id)` | Tear down a deployment |
| `get_plan(plan_id)` | Fetch a stored deployment plan |
| `list_api_keys()` | Return all API keys |
| `create_api_key(name, tenant="", quota=0)` | Mint a new gateway key |
| `delete_api_key(key_id)` | Revoke an API key |
| `create_join_token(ttl_seconds=86400)` | Mint a node enrollment token |
| `cluster_health()` | Cluster health summary |
| `enterprise_status()` | License and edition information |
| `audit_log(limit=100)` | Tamper-evident audit log (enterprise) |

### Exception hierarchy

```
PurserError(status_code, message, error_type)
├── NotFoundError          # 404
├── ConflictError          # 409
└── LicenseRequiredError   # 402  (.feature attribute)
```

## Development

```bash
cd sdk/python
pip install -e ".[dev]"
python -m pytest
```
