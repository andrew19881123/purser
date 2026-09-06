# purser.yaml — Declarative Cluster Configuration

Purser supports a declarative, versionable desired-state file called **`purser.yaml`**.
Instead of managing models, deployments and quotas one API call at a time, you describe
the full cluster state in a single file and let Purser reconcile the difference.

## Quick start

```yaml
apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: my-cluster
cluster:
  id: my-cluster-01
models:
  - id: qwen3-8b
    source:
      type: huggingface
      repo: Qwen/Qwen3-8B
    quantizations: [Q4_K_M]
deployments:
  - model: qwen3-8b
    quantization: Q4_K_M
```

Apply it with:

```bash
purser apply purser.yaml        # converge live state → desired state
```

Preview what would change without touching the cluster:

```bash
purser diff purser.yaml         # dry-run: prints add/remove/upsert plan
```

> **Wave 2 note:** `purser apply` and `purser diff` are scheduled for the Wave 2 CLI
> release. The schema, loader, validator and diff engine are available now in
> `go/controlplane/config` for programmatic use.

---

## Schema reference

### Top-level fields

| Field | Type | Required | Description |
|---|---|---|---|
| `apiVersion` | string | yes | Must be `purser/v1` |
| `kind` | string | yes | Must be `ClusterConfig` |
| `metadata` | [Metadata](#metadata) | no | Name, labels, annotations |
| `cluster` | [ClusterSpec](#clusterspec) | no | Cluster identity |
| `models` | [][ModelSpec](#modelspec) | no | Desired model catalog |
| `deployments` | [][DeploySpec](#deployspec) | no | Desired active deployments |
| `quotas` | [][QuotaSpec](#quotaspec) | no | Team usage limits |
| `gateway` | [GatewaySpec](#gatewayspec) | no | API gateway tuning |
| `orgs` | [][OrgSpec](#orgspec) | no | Organisation and team hierarchy (v0.4+) |
| `node_pools` | [][NodePoolSpec](#nodepoolspec) | no | Node pool GitOps configuration (v0.4+) |

---

### Metadata

```yaml
metadata:
  name: prod-cluster          # Human-readable cluster name
  labels:
    env: production
    team: platform
  annotations:
    owner: platform@example.com
```

| Field | Type | Description |
|---|---|---|
| `name` | string | Human-readable name for this config |
| `labels` | map[string]string | Arbitrary key/value labels |
| `annotations` | map[string]string | Arbitrary key/value annotations |

---

### ClusterSpec

```yaml
cluster:
  id: prod-cluster-01         # Stable identifier for the cluster
```

| Field | Type | Description |
|---|---|---|
| `id` | string | Stable, unique cluster identifier |

---

### ModelSpec

Declares a model that should exist in the catalog.

```yaml
models:
  - id: qwen3-moe-235b              # Unique model ID used in deployments and API calls
    source:
      type: huggingface
      repo: Qwen/Qwen3-MoE-235B-A22B
    quantizations: [Q4_K_M, Q8_0]  # Quantization variants to register
    description: Qwen3 MoE 235B (A22B active)
    max_context_len: 32768
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Unique model identifier; referenced by `deployments[].model` |
| `source` | [SourceSpec](#sourcespec) | no | Where to fetch the model weights |
| `quantizations` | []string | no | Quantization variants (e.g. `Q4_K_M`, `Q8_0`) |
| `description` | string | no | Free-text description |
| `max_context_len` | int64 | no | Maximum context length in tokens |

---

### SourceSpec

Describes where to obtain the model weights.

```yaml
source:
  type: huggingface            # "huggingface" | "s3" | "local" | "vertexai" | "sagemaker" | "azureml"
  repo: Qwen/Qwen3-MoE-235B-A22B
```

| Field | Type | Description |
|---|---|---|
| `type` | string | Source backend — see table below |
| `repo` | string | HuggingFace repo path (e.g. `Qwen/Qwen3-8B`) |
| `bucket_url` | string | S3 URL (e.g. `s3://mybucket/models/qwen3`) |
| `path` | string | Local filesystem path (e.g. `/data/models/qwen3`) |

**Supported source types:**

| Type | Weights location |
|---|---|
| `huggingface` | HuggingFace Hub — `repo` field required |
| `s3` | AWS S3 — `bucket_url` field required |
| `local` | Local path on the control-plane node — `path` field required |
| `vertexai` | Google Vertex AI Model Garden |
| `sagemaker` | AWS SageMaker Model Registry |
| `azureml` | Azure Machine Learning Registry |

---

### DeploySpec

Declares that a model should be actively deployed.

```yaml
deployments:
  - model: qwen3-moe-235b     # Must reference an id in models[]
    quantization: Q4_K_M
    min_nodes: 2              # Minimum number of nodes (optional)
    max_nodes: 8              # Maximum number of nodes (optional)
    approved: true            # Approval gate for Wave 3 gated rollouts
```

| Field | Type | Description |
|---|---|---|
| `model` | string | Model ID — must match a `models[].id` in the same file |
| `quantization` | string | Quantization variant to serve |
| `min_nodes` | int | Minimum node count for this deployment |
| `max_nodes` | int | Maximum node count for this deployment |
| `approved` | bool | Approval gate (Wave 3 gated rollouts) |

---

### QuotaSpec

Sets per-team usage limits enforced by the gateway.

```yaml
quotas:
  - team: eng
    monthly_requests: 100000   # 0 = unlimited
    monthly_tokens: 50000000   # 0 = unlimited
  - team: data-science
    monthly_requests: 500000
```

| Field | Type | Description |
|---|---|---|
| `team` | string | Team identifier matching the `Tenant` on an API key |
| `monthly_requests` | int64 | Monthly request cap; `0` means unlimited |
| `monthly_tokens` | int64 | Monthly token cap (input + output); `0` means unlimited |

---

### GatewaySpec

Tunes the API gateway.

```yaml
gateway:
  port: 8080
  body_limit_mb: 16
```

| Field | Type | Description |
|---|---|---|
| `port` | int | Gateway listen port (default: 8080) |
| `body_limit_mb` | int | Maximum request body size in MiB |

---

### OrgSpec

Declares an organisation and its nested team hierarchy.

```yaml
orgs:
  - id: acme                      # Unique org ID (immutable)
    name: "Acme Corp"             # Display name
    slug: acme                    # URL-safe identifier
    description: "Main org"       # Optional
    teams:
      - id: ml-research
        name: "ML Research"
        slug: ml-research
        members:
          - user_id: "alice@example.com"   # email or OIDC subject
            role: developer
          - user_id: "bob@example.com"
            role: team_admin
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Unique, immutable organisation identifier |
| `name` | string | no | Human-readable display name |
| `slug` | string | no | URL-safe short name |
| `description` | string | no | Free-text description |
| `teams` | [][TeamSpec](#teamspec) | no | Teams within this org |

#### TeamSpec

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Unique team identifier within the org |
| `name` | string | no | Human-readable display name |
| `slug` | string | no | URL-safe short name |
| `description` | string | no | Free-text description |
| `members` | [][TeamMemberSpec](#teammemberspec) | no | Team members |

#### TeamMemberSpec

| Field | Type | Required | Description |
|---|---|---|---|
| `user_id` | string | yes | User identifier — email address or OIDC `sub` claim |
| `role` | string | yes | Role name — see [built-in roles](#built-in-roles) |

**Built-in roles**

| Role | Description |
|---|---|
| `platform_admin` | Full cluster administration |
| `org_admin` | Administers one org and all its teams |
| `team_admin` | Administers one team |
| `developer` | Can create and manage deployments within the team's pools |
| `viewer` | Read-only access |
| `inference_only` | Can call inference endpoints but cannot manage resources |

Custom role names are also accepted — they are stored as-is and matched against
policies defined in the RBAC configuration.

---

### NodePoolSpec

Declares a node pool under GitOps control.

```yaml
node_pools:
  - id: ml-gpu-lab
    name: "ML GPU Lab"
    owner_type: team       # "platform" | "org" | "team"
    owner_id: ml-research  # team ID when owner_type is "team"
    policy: exclusive      # "exclusive" | "shared"
    nodes:
      - gpu-node-01
      - gpu-node-02

  - id: platform-shared
    name: "Platform Shared Pool"
    owner_type: platform
    policy: shared
    nodes:
      - gpu-node-03
    quotas:
      - team_id: ml-research
        max_deployments: 3
        max_gpu_nodes: 6
        priority: 10
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Unique pool identifier (immutable) |
| `name` | string | no | Human-readable display name |
| `description` | string | no | Free-text description |
| `owner_type` | string | yes | Ownership scope: `platform`, `org`, or `team` |
| `owner_id` | string | no | ID of the owning org or team; omit when `owner_type` is `platform` |
| `policy` | string | yes | `exclusive` — single-team dedicated pool; `shared` — multi-team pool with quotas |
| `nodes` | []string | no | Node host names or IDs assigned to this pool |
| `quotas` | [][PoolQuotaSpec](#poolquotaspec) | no | Per-team quotas; only valid when `policy: shared` |

**Validation rules:**

- `policy` must be `exclusive` or `shared`.
- `quotas` may only be set on `shared` pools — an `exclusive` pool with quotas
  is a validation error.
- `owner_id` is advisory when `owner_type` is `team`; the team may be defined
  externally and the loader will not fail if it is not present in `orgs`.

#### PoolQuotaSpec

Per-team resource quota on a shared pool.

| Field | Type | Description |
|---|---|---|
| `team_id` | string | Team identifier matching a `teams[].id` in `orgs` |
| `max_deployments` | int | Maximum concurrent deployments this team may run on the pool |
| `max_gpu_nodes` | int | Maximum GPU nodes this team may occupy on the pool |
| `priority` | int | Scheduling priority — higher values are served first |

---

## Production example

```yaml
apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: prod-eu-west-1
  labels:
    env: production
    region: eu-west-1
  annotations:
    owner: platform@example.com
    cost-center: "CC-1042"

cluster:
  id: prod-eu-west-1

models:
  - id: qwen3-moe-235b
    source:
      type: huggingface
      repo: Qwen/Qwen3-MoE-235B-A22B
    quantizations: [Q4_K_M]
    description: Primary reasoning model
    max_context_len: 32768

  - id: qwen3-8b
    source:
      type: huggingface
      repo: Qwen/Qwen3-8B
    quantizations: [Q8_0]
    description: Fast chat model
    max_context_len: 32768

  - id: bge-m3
    source:
      type: s3
      bucket_url: s3://ml-models-eu/bge-m3
    quantizations: [Q4_K_M]
    description: Multilingual embeddings

deployments:
  - model: qwen3-moe-235b
    quantization: Q4_K_M
    min_nodes: 4
    max_nodes: 16
    approved: true

  - model: qwen3-8b
    quantization: Q8_0
    min_nodes: 2
    max_nodes: 8
    approved: true

  - model: bge-m3
    quantization: Q4_K_M
    min_nodes: 1
    max_nodes: 4
    approved: true

quotas:
  - team: eng
    monthly_requests: 500000
    monthly_tokens: 200000000
  - team: data-science
    monthly_requests: 1000000
    monthly_tokens: 500000000
  - team: external-api
    monthly_requests: 50000
    monthly_tokens: 10000000

gateway:
  port: 8080
  body_limit_mb: 32
```

---

## v0.4 full example (orgs + node pools)

```yaml
apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: acme-prod

cluster:
  id: prod-cluster

models:
  - id: llama3-70b
    source:
      type: huggingface
      repo: meta-llama/Llama-3.1-70B
    quantizations: [Q4_K_M]

deployments:
  - model: llama3-70b
    quantization: Q4_K_M
    min_nodes: 2
    max_nodes: 8
    approved: true

orgs:
  - id: acme
    name: "Acme Corp"
    slug: acme
    teams:
      - id: ml-research
        name: "ML Research"
        slug: ml-research
        members:
          - user_id: "alice@example.com"
            role: developer

      - id: platform-eng
        name: "Platform Engineering"
        slug: platform-eng
        members:
          - user_id: "admin@example.com"
            role: team_admin

node_pools:
  - id: ml-gpu-lab
    name: "ML GPU Lab"
    owner_type: team
    owner_id: ml-research
    policy: exclusive
    nodes:
      - gpu-node-01
      - gpu-node-02

  - id: platform-shared
    name: "Platform Shared Pool"
    owner_type: platform
    policy: shared
    nodes:
      - gpu-node-03
      - gpu-node-04
    quotas:
      - team_id: ml-research
        max_deployments: 3
        max_gpu_nodes: 6
        priority: 10
      - team_id: platform-eng
        max_deployments: 2
        max_gpu_nodes: 4
        priority: 20

gateway:
  port: 8081
  body_limit_mb: 4

quotas:
  - team: ml-research
    monthly_requests: 100000
```

---

## Node Pool GitOps

Node pools can be fully managed through `purser.yaml`, keeping pool topology
alongside the rest of your infrastructure code.

### How it works

1. **Declare** pools in `node_pools` with their assigned nodes and ownership.
2. **Commit** the updated `purser.yaml` and push to your GitOps branch.
3. **Apply** — either via `purser apply` or automatically via the control-plane
   watcher — and Purser reconciles the pool topology:
   - Pools present in the file but not live → **created**.
   - Pools present both in the file and live → **updated** (nodes, quotas, policy).
   - Pools present live but absent from the file → **removed**.

### Exclusive vs shared pools

| Policy | Use case | Quotas allowed? |
|---|---|---|
| `exclusive` | Dedicated GPU cluster for one team; no sharing | No |
| `shared` | Shared infrastructure with per-team resource limits | Yes |

### Diff output (v0.4)

```
+ node_pool  ml-gpu-lab          (add, exclusive, 2 nodes)
+ node_pool  platform-shared     (add, shared, 4 nodes, 2 team quotas)
+ org        acme                (add, 2 teams, 2 members)
```

---

## Staging example

Identical cluster topology but with a different `cluster.id` and reduced quotas:

```yaml
apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: staging-eu-west-1
  labels:
    env: staging
    region: eu-west-1

cluster:
  id: staging-eu-west-1   # ← only this differs from production

models:
  - id: qwen3-8b
    source:
      type: huggingface
      repo: Qwen/Qwen3-8B
    quantizations: [Q4_K_M]

deployments:
  - model: qwen3-8b
    quantization: Q4_K_M
    min_nodes: 1
    max_nodes: 2

quotas:
  - team: eng
    monthly_requests: 50000   # 10× smaller than prod
    monthly_tokens: 20000000
```

---

## Applying the file (Wave 2 preview)

```bash
# Show what would change without modifying the cluster
purser diff purser.yaml

# Converge the cluster to the desired state
purser apply purser.yaml

# Apply and wait for all deployments to become active
purser apply --wait purser.yaml
```

`purser diff` output format:

```
+ model     qwen3-moe-235b    (add)
~ model     qwen3-8b          (already present — no change)
+ deploy    qwen3-moe-235b/Q4_K_M  (add)
~ quota     eng               (upsert)
```

---

## Validation rules

The loader enforces the following rules at parse time:

- `apiVersion` must be exactly `purser/v1`.
- `kind` must be exactly `ClusterConfig`.
- Every `models[].id` must be non-empty and unique within the file.
- Every `models[].source.type`, when set, must be one of the known backends.
- Every `deployments[].model` must reference an `id` declared in `models`.
- Every `quotas[].team` must be non-empty.
- Every `orgs[].id` must be non-empty and unique.
- Every team within an org must have a non-empty `id`.
- Every team member must have a non-empty `user_id`.
- Every `node_pools[].id` must be non-empty and unique.
- `node_pools[].policy` must be `exclusive` or `shared`.
- A pool with `policy: exclusive` must not carry any `quotas` entries.

Validation errors are returned as structured Go errors that include the offending
field and value, making them suitable for display in CI pipelines.

---

## GitOps workflow

Store `purser.yaml` in version control alongside your infrastructure code.
A typical CI pipeline looks like this:

```yaml
# .github/workflows/deploy.yml
on:
  push:
    branches: [main]
    paths: [purser.yaml]

jobs:
  apply:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Diff
        run: purser diff purser.yaml
      - name: Apply
        run: purser apply --wait purser.yaml
        env:
          PURSER_URL: ${{ secrets.PURSER_URL }}
          PURSER_API_KEY: ${{ secrets.PURSER_API_KEY }}
```

This gives you full audit history (every desired-state change is a git commit), easy
rollback (`git revert`), and environment parity (prod and staging share the same file
structure with only `cluster.id` and quota values differing).

---

## Applying configuration

### Via startup flag and continuous reconciliation (GitOps)

Pass `--config` (or set `PURSER_CONFIG`) to apply a purser.yaml on every
control-plane start. The desired state is reconciled against the live registry
on boot — idempotent, safe to use in containers and Kubernetes deployments.

```bash
purser-control-plane --config purser.yaml
# or
PURSER_CONFIG=purser.yaml purser-control-plane
```

In addition to the one-time startup apply, Purser watches the file every **30 seconds**
(configurable via `PURSER_CONFIG_INTERVAL`) and automatically re-applies it whenever
the content changes. This enables a fully pull-based GitOps workflow:

1. Commit changes to `purser.yaml` in your Git repo.
2. Your CD pipeline writes the updated file to the control-plane host
   (or mounts it as a ConfigMap in Kubernetes).
3. Purser detects the change (within one polling interval) and converges
   automatically — no manual `purser apply` needed.

The watcher uses **SHA-256 content hashing** so polling is cheap and spurious
re-applies (same file, no change) are skipped. Temporary errors (file missing,
invalid YAML) are non-fatal and retried on the next tick — this lets you use
Kubernetes ConfigMap late-mount without a crash loop.

#### Tuning the polling interval

```bash
# Default: 30 s
PURSER_CONFIG_INTERVAL=60 purser-control-plane --config purser.yaml
```

`PURSER_CONFIG_INTERVAL` is interpreted as an integer number of seconds.

#### Kubernetes ConfigMap example

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: purser-config
  namespace: purser
data:
  purser.yaml: |
    apiVersion: purser/v1
    kind: ClusterConfig
    metadata:
      name: prod-cluster
    models:
      - id: qwen3-8b
        source:
          type: huggingface
          repo: Qwen/Qwen3-8B
        quantizations: [Q4_K_M]
    deployments:
      - model: qwen3-8b
        quantization: Q4_K_M
```

Mount it at `--config /etc/purser/purser.yaml` in the control-plane Pod:

```yaml
# Deployment snippet
containers:
  - name: control-plane
    args: ["--config", "/etc/purser/purser.yaml"]
    env:
      - name: PURSER_CONFIG_INTERVAL
        value: "30"
    volumeMounts:
      - name: purser-config
        mountPath: /etc/purser
volumes:
  - name: purser-config
    configMap:
      name: purser-config
```

When the ConfigMap is updated (e.g. via `kubectl apply -f purser.yaml`), Kubernetes
projects the new content to the mounted file within seconds. Purser picks it up on
the next polling tick and applies the diff automatically.

### Via REST API

All three endpoints accept a raw `application/yaml` body and require a valid
API key (`Authorization: Bearer $PURSER_ADMIN_KEY`).

```bash
# Dry-run — shows what would change, makes no mutations
curl -X POST http://cp:8080/api/v1/config/diff \
  -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
  -H "Content-Type: application/yaml" \
  --data-binary @purser.yaml

# Apply — reconciles desired state with live cluster
curl -X POST http://cp:8080/api/v1/config/apply \
  -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
  -H "Content-Type: application/yaml" \
  --data-binary @purser.yaml

# Export — download current cluster state as purser.yaml
curl http://cp:8080/api/v1/config/export \
  -H "Authorization: Bearer $PURSER_ADMIN_KEY" > current.yaml
```

#### Response shapes

`POST /api/v1/config/diff` returns a JSON summary of pending changes:

```json
{
  "models_to_add":         [{ "id": "qwen3-moe-235b", ... }],
  "models_to_remove":      [],
  "deployments_to_add":    [{ "model": "qwen3-moe-235b", "quantization": "Q4_K_M" }],
  "deployments_to_remove": [],
  "quotas_to_upsert":      [{ "team": "eng", "monthly_requests": 100000 }]
}
```

`POST /api/v1/config/apply` returns a summary of what was changed:

```json
{
  "applied": {
    "models_added": 1,
    "deployments_added": 0,
    "quotas_upserted": 0
  }
}
```

`GET /api/v1/config/export` returns a `Content-Type: application/yaml` body in
the same `ClusterConfig` format as the input, representing the current live
state of the cluster (models + active deployments).
