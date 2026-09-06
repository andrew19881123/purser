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
