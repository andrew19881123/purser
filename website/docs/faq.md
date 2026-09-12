# FAQ

Answers to the questions people actually hit. Where the honest answer is "not
yet", it says so.

---

## Is Purser ready for production?

**No.** Purser is alpha. The zero-config path — enrol a node, deploy a model,
chat — is implemented and demonstrated end to end, but **live inference on real
GPU hardware has not been validated**, and the project publishes no throughput
numbers. See [Status](index.md#status).

Concretely, what is and is not settled today:

| | State |
|---|---|
| Orchestration: enrolment, planning, routing, reconciliation | Implemented, tested |
| OpenAI-compatible gateway, auth, rate limiting | Implemented, tested |
| Real GPU inference via llama.cpp | Adapter registered, **not validated** |
| Planner performance constants | Uncalibrated first-order estimates |
| Published benchmarks | None — see [Benchmarks](benchmarks/index.md) |

Use it to evaluate the architecture, to develop against, and to give feedback.
Do not put it in front of users who expect an SLA.

---

## What is the mock engine?

A deterministic, GPU-free inference backend built into the agent. It accepts
requests and returns synthetic output, so the whole control path — enrolment,
planning, deployment, routing, streaming, metrics — can be exercised on a laptop
with no GPU and no model weights.

It is the agent's **default**: an agent uses `mock` unless you set
`PURSER_ENGINE_BACKEND` to something else.

The mock engine is **not** part of the Docker Compose demo stack. No agent ships
in that stack, so nothing in it serves inference of any kind — compose gives you
the control plane, gateway, and dashboard, and the mock engine only comes into
play once you run an agent yourself.

!!! warning "Mock output is not inference"
    The mock engine does not load weights and does not run a model. Text it
    returns is synthetic, and any timing measured against it reflects Purser's
    orchestration overhead only. Never run it in production, and never benchmark
    with it.

---

## Which inference engines are supported?

Two backends are registered in the agent:

- `mock` — always available, described above
- `llamacpp` — real inference via llama.cpp, compiled in only when the agent is
  built with `--features llamacpp`, and requiring `PURSER_LLAMACPP_BIN` to point
  at the llama.cpp binaries

!!! warning "The prebuilt agent has only the mock engine"
    The release workflow builds `purser-agent` without the `llamacpp` feature, so
    the published `.deb`, `.rpm`, and tarball binaries register `mock` only.
    Requesting `llamacpp` on one of them fails with *"llama.cpp backend requested
    but binary was not compiled with --features llamacpp"*. To serve real weights
    you currently have to build the agent yourself.

Purser drives engines through an Engine Adapter rather than implementing
inference, so additional backends are a matter of adapters — but only these two
exist today.

---

## Do I need NVLink or InfiniBand?

**No.** Purser only does pipeline parallelism: each node gets a contiguous range
of layers, and the network carries the hidden-state activation once per stage
boundary per token — kilobytes, not the weights. Commodity Ethernet is the
assumed case.

In fact Purser has **no concept** of NVLink or PCIe topology. It models only
node-to-node links, so absent NVLink is not a degraded mode. If you have NVLink
and want it used, a tensor-parallel engine will exploit it and Purser will not —
see [Purser vs vLLM](guides/vs-vllm.md).

---

## Can I use Purser without Kubernetes?

Yes. The control plane, gateway, and UI run under Docker Compose, and agents
install natively on each GPU machine as a `.deb`, `.rpm`, or tarball. Kubernetes
and Helm are one deployment option, not a requirement.

Note that the Compose stack contains **no agent service** — agents run on the
hosts that own the GPUs, because they supervise a local engine process. See
[Homelab Setup](guides/homelab-setup.md) for the two-machine topology.

---

## Why isn't my deployment becoming ACTIVE?

First, a correction that resolves most confusion: **there is no `PENDING`
deployment state.** The lifecycle states are `PLANNED`, `PROVISIONING`, `ACTIVE`,
`REBALANCING`, `STOPPING`, `STOPPED`, and `FAILED`.

That matters because when a model cannot be planned, **no deployment row is
created at all** — the deploy call itself returns `422` with
`"error": "model_does_not_fit"` and a reason. There is nothing to get stuck. If
you are waiting for something to appear, re-read the response body of the deploy
request.

Work down this list:

**1. The model has no architecture spec.** By far the most common cause, and the
one that catches everyone importing from HuggingFace — see the next question. The
planner reports *"model ... declares no layers"* or *"declares no quantizations"*.

**2. No node is `READY`.** The planner only considers nodes in `READY` or
`RUNNING`. A node that has enrolled sits in `ENROLLED` until its agent is up and
heartbeating, so an enrolled-but-silent agent is invisible to planning. Check:

```bash
curl -s http://<control-plane>:8080/api/v1/nodes \
  -H 'Authorization: Bearer <admin-key>' | jq '.nodes[] | {id, state}'
```

**3. The model genuinely does not fit.** The `422` carries a `deficit_gb` telling
you how much memory is missing. Try a smaller quantisation or add a node.

**4. It fits in aggregate but no contiguous split works.** Reported as *"no
feasible contiguous partition ... (memory/headroom)"* — and, confusingly, with
`deficit_gb: 0`, because the shortfall is per-stage rather than fleet-wide. Your
smallest node is usually the binding constraint; excluding it often helps. See
[Consumer GPU Setup](guides/consumer-gpu-setup.md).

**5. A node pool restricts the fleet.** On a multi-tenant setup, an API key
scoped to a team can only plan onto that team's node pool. The error names the
pool.

**6. It is waiting for a human.** With enterprise
[deployment approval gates](enterprise/deployment-approvals.md) enabled, the
deploy returns `202` with `"status": "pending_approval"` and nothing rolls out
until an admin approves it. This is the one thing legitimately described as
"pending".

**7. It reached `PROVISIONING` and then `FAILED`.** Each engine start has a
two-minute budget; a first-time model download that overruns it fails the
deployment. The failure reason is recorded on the deployment.

The fastest diagnostic is the read-only dry run, which returns `200` with
`"feasible": false` and a reason rather than an error:

```bash
curl -sS -X POST http://<control-plane>:8080/api/v1/models/<model-id>/plan \
  -H 'Authorization: Bearer <admin-key>' | jq
```

---

## I imported a model from HuggingFace and it will not deploy

Expected, and worth knowing: the HuggingFace importer records the model's **id,
family, and source** — it does **not** populate the architecture spec. The
planner needs the layer count, hidden size, KV-head geometry, context limit, and
available quantisations, and it rejects a model whose layer count is zero with
*"model ... declares no layers"*.

Register the spec explicitly with `POST /api/v1/models`, whose body is the model
spec. See [Model Catalog](configuration/models.md) and
[Model Sources](configuration/model-sources.md).

---

## Why is my model missing from `GET /v1/models`?

That endpoint lists models with an **active deployment and a route pushed to the
gateway**, not your catalog. A registered but undeployed model will not appear,
and calling it returns `404 model_not_found`.

Ask the control plane for the catalog instead:

```bash
curl -s http://<control-plane>:8080/api/v1/models \
  -H 'Authorization: Bearer <admin-key>'
```

---

## Why does a model I deployed three times appear only once in the Playground?

Because the Playground's model picker lists **models, not deployments** — and that
is deliberate. The Gateway's route table is keyed by **model id**, so deploying the
same model several times does not register several entries: it registers **one
route** that the Gateway load-balances across all the replicas behind it. Deploying
a model N times is how you scale throughput, not how you add a second model.

One model is therefore one option in the picker, and one id to send as `model` in a
`POST /v1/chat/completions` call. Which replica serves a given request is the
Gateway's business, not the client's — you cannot address an individual deployment
from the API, and you do not need to.

A model with three ACTIVE deployments behind it is the expected shape of a
scaled-out model, not a duplicate. If you want two distinct entries in the picker,
they have to be two distinct model ids.

!!! note "When the Gateway is unreachable"
    The picker prefers the Gateway's served list (`GET /v1/models`). If the Gateway
    cannot be reached, the Playground falls back to the models that have an
    **ACTIVE deployment**, de-duplicated the same way — so three ACTIVE deployments
    of one model still render a single option. If no model has an active
    deployment, the picker falls back to the default model and shows a notice
    pointing at the [Model Catalog](configuration/models.md) instead.

---

## Does anything phone home?

**No telemetry, analytics, or usage data is sent to the Purser maintainers.**
There is no phone-home, no version check, and no license server — enterprise
licenses are verified **fully offline**, by checking an Ed25519 signature against
a public key embedded in the binary. The dashboard bundles its assets locally: no
CDN, no web fonts, no analytics scripts.

Being precise about what *does* leave a node, so you can audit it:

| Connection | When |
|---|---|
| Agent → **your** control plane | Enrolment, heartbeats, and mTLS certificate renewal (roughly daily) |
| Gateway → **your** control plane | Token-usage accounting, only when the control-plane URL is configured |
| Control plane → **your** OIDC / LDAP, object storage, webhook endpoint | Only what you configure |
| Control plane → `huggingface.co` | Only when you issue a HuggingFace model import. This base URL is the one remote default in the binary; it is overridable for mirrors |
| Anything → an OTel collector | **Off** unless you set `OTEL_EXPORTER_OTLP_ENDPOINT`; then it goes to the collector you named |

Two caveats for anyone doing a serious privacy review. Model imports from object
storage reach whatever bucket you point at, which is expected but is outbound
traffic. And the Helm charts ship permissive egress rules — Purser does not need
outbound internet access, but the chart does not block it for you, so egress
lockdown is yours to configure.

See [OpenTelemetry](configuration/otel.md) for the tracing opt-in and
[Licensing & Key Management](enterprise/licensing.md) for offline license
verification.

---

## Do I have to compile anything?

For the control plane, gateway, and UI: no — they ship as container images and a
Helm chart. For the agent: prebuilt packages exist, **but** they contain only the
mock engine, so real inference currently requires building the agent yourself
with `--features llamacpp`. See the engines question above.

---

## Does Purser make a single request faster?

No, and it is worth being clear about this. Pipeline parallelism exists to run a
model that does not fit on one machine. With one request in flight, only one
pipeline stage works at a time while the rest idle, and every stage boundary adds
a network hop. Throughput improves when many requests overlap; single-request
latency does not.

If your model already fits on one machine, splitting it will not help. See
[Purser vs Ollama](guides/vs-ollama.md).

---

## How fast is it?

Unknown, and deliberately unstated. See [Benchmarks](benchmarks/index.md) for why
no numbers are published and what a published benchmark will contain.

---

## See also

- [Purser vs Ollama](guides/vs-ollama.md) · [Purser vs vLLM](guides/vs-vllm.md)
- [Homelab Setup](guides/homelab-setup.md) · [Consumer GPU Setup](guides/consumer-gpu-setup.md)
- [Migrating from the OpenAI API](guides/openai-migration.md)
- [Multi-Node Inference](getting-started/multi-node-inference.md)
- [Benchmarks](benchmarks/index.md)
