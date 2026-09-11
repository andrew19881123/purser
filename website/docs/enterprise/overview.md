# Open-Core Model

Purser follows the open-core licensing model popularized by LiteLLM: a permissive MIT-licensed core, plus a source-available `enterprise/` directory whose features are gated behind a commercial license key.

---

## MIT Core — what's included

Everything **outside** the `enterprise/` directory is free and open source under the [MIT License](https://github.com/andrew19881123/purser/blob/main/LICENSE). You may run, study, modify, and redistribute it — including for commercial purposes — with no copyleft obligations.

The MIT-licensed core is the **full single-cluster orchestration stack**:

| Component | What it includes |
|---|---|
| **Agent** | Hardware probe, link benchmark, engine supervisor, model cache, mDNS discovery, self-healing, mock inference server |
| **Engine Adapter** | `EngineBackend` trait, mock backend, llama.cpp backend (flag builder, GGUF reader, metrics parser) |
| **Planner** | Dynamic-programming optimal layer-split algorithm with calibrated performance estimates |
| **Control Plane** | SQLite registry, orchestrator, reconciler, internal PKI, RegistrationService (gRPC), REST API |
| **API Gateway** | OpenAI-compatible `/v1` endpoint, auth, quota, route-sync |
| **Dashboard** | Fleet view, model catalog, deploy, chat playground |

---

## Enterprise Source-Available

The `enterprise/` directory is **source-available** under the [Purser Enterprise License](https://github.com/andrew19881123/purser/blob/main/enterprise/LICENSE). The code is **public** — you can view, compile, modify, and use it for development, evaluation, and testing. However, **use in production or for commercial purposes requires a valid commercial license**.

### Capabilities requiring a licence flag — v0.6

Each of these returns `402 Payment Required` unless the active key's `features` array contains the exact flag string. The [feature gate reference](license.md#feature-gate-reference) is the authoritative list and records the product names that differ from their flag.

| Capability | Licence flag |
|---|---|
| Tamper-evident audit log (hash-chained, offline-verifiable). See [Audit Log](audit-log.md). | `audit` |
| Inference audit log — read access; recording is always active. See [Inference Audit Log](inference-audit.md). | `inference_audit` |
| Per-tenant usage accounting and chargeback reports. See [Chargeback](chargeback.md). | `billing` |
| Deployment approval gates. See [Deployment Approvals](deployment-approvals.md). | `deployment_approvals` |
| Embedded OPA/Rego policy engine. See [Policy-as-Code](policy-as-code.md). | `policy_engine` |
| AI Act Art.11 / Annex IV technical documentation. See [AI Act Compliance](ai-act-compliance.md). | `ai_act_compliance` **or** `inference_audit` |
| GDPR right to erasure. See [GDPR Compliance](gdpr-compliance.md). | `gdpr` |

### Shipped in v0.6 with no licence flag

These are implemented and no entitlement is checked for them. A key does not need a flag for any of them, and no flag would enable or disable them.

| Capability | Notes |
|---|---|
| RBAC (per-API-key roles) | Always enforced, in every edition. See [RBAC](../configuration/rbac.md). |
| OIDC / SSO with Authorization Code Flow + PKCE (EntraID, Okta, Keycloak) | See [OIDC configuration](../configuration/oidc.md). |
| Per-tenant scoping of registry records, keys, and usage data | Underlies the chargeback and audit surfaces. |
| Raft HA control plane — leader election and replicated registry | Reachable through configuration; needs an external PostgreSQL, since SQLite requires `replicaCount=1`. See [HA Control Plane](ha-control-plane.md). |
| SLO contracts and the what-if planner | See [SLO Contracts](slo.md). |
| Ansible fleet enrollment | See [Ansible](../integrations/ansible.md). |
| Internal CA / PKI — root → intermediate → leaf issuance, renewal, revocation, rotation | Purser's own CA, used for agent mTLS enrolment. |
| Certificates issued by your existing CA, via cert-manager | The chart renders a `cert-manager.io/v1` Certificate for the control-plane TLS secret from a `ClusterIssuer` or `Issuer` you name, with configurable duration and renewal. See [cert-manager](../configuration/cert-manager.md) and [certificate renewal](../operations/cert-renewal.md). |
| Gateway horizontal scaling behind a Kubernetes Service | Multiple gateway replicas behind a Service whose type is configurable (ClusterIP by default, LoadBalancer or NodePort for clients outside the cluster). |
| Signed release artefacts | SLSA L2 provenance, cosign SBOM attestations on image digests, and `SHA256SUMS` for the `.deb`/`.rpm`/tarball artefacts. |
| Offline licence validation | The enforcement mechanism itself — see below. |

### Not implemented in v0.6

Earlier revisions of this page listed these as shipped under the heading "Fleet at Scale". Each row below says what is genuinely missing and what nearby capability does exist, because in several cases part of the ground is covered by something under a different name. No licence flag enables any of them:

| Capability | State |
|---|---|
| A dedicated MDM integration (Jamf, Intune, or similar) | None. Fleet enrolment is offered through Ansible, and the join-token model works with any tool that can set three environment variables — including an MDM-delivered package — but there is no MDM-specific integration to configure. |
| Golden-image build pipeline | No image-building tooling ships here (no Packer, cloud image, or kickstart/preseed templates). Enrolment itself supports scripted provisioning, so you can bake an image yourself — see [enrollment bundle](../install/enrollment-bundle.md). |
| An offline install bundle for air-gapped sites | No single downloadable archive of images, charts, and dependencies for a disconnected install. Note that release artefacts *are* signed and air-gapped *operation* is supported — see the distinction below. |
| Multi-cluster fleet management | None. A control plane serves one cluster: its cluster ID is a single value, and there is no federation, peering, or remote-cluster concept. |
| A bare-metal virtual IP (keepalived / VRRP) | Not provided. In Kubernetes, gateway and control-plane replicas sit behind Services, as above; on bare metal you would supply your own VIP or load balancer. No PodDisruptionBudget ships with the chart. |

Whether any of these is built, and in which release, is not decided — this page deliberately gives no target version, and will name one only once there is something to point at. Please do not rely on them in a procurement decision; ask first.

**Three things are easy to conflate here, and two of the three ship.** *Air-gapped operation* is fully supported: licence verification is offline by design — no phone-home, no licence server, no network dependency of any kind — and telemetry degrades to no-ops. *Artefact signing* also ships: releases carry SLSA L2 provenance and cosign SBOM attestations, so you can verify what you received. What does not exist is the third thing — a *packaged offline bundle* that collects images, charts, and dependencies into one archive for installing into a disconnected site. Running Purser air-gapped works today, and you can verify the artefacts you fetch; assembling them for transfer is currently your own step.

---

## How the license key works

The license system is designed to be **fully offline** and **air-gap safe**:

1. **Format** — the key is an ed25519-signed JWT-style token encoding the licensee name, expiry date, and feature entitlements.
2. **Verification** — the Control Plane verifies the key at startup against an **embedded ed25519 public key**. No phone-home, no external requests.
3. **Failure behavior** — an absent key silently enables the community edition. A present-but-invalid key causes a **fatal startup error** by design — so a misconfigured deployment fails loud rather than silently dropping to community.
4. **Temporal validity** — the key has an `expires` field. The Control Plane checks `license.ValidAt(time.Now())` before each enterprise operation; an expired key disables enterprise features without crashing.

Configure the key:

```bash
# Environment variable
export PURSER_LICENSE_KEY=<your-key>

# Helm
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.3.0 \
  --set license.key="<your-key>"
```

The Helm chart stores the key in a Kubernetes Secret and injects it into the Control Plane pod via `secretKeyRef`.

---

## Checking license status

### Dashboard

The **Settings** page of the operator dashboard shows a **License** section at the bottom. It displays:

- **Community edition**: a neutral "Community" badge and a link to upgrade documentation.
- **Enterprise edition**: the licensee name, a badge per enabled feature (`audit`, `billing`, `policy_engine`, …), and the expiry date. A red "Expired" badge appears when the key has passed its `expires` date. The badges show the raw flag strings from the key — see the [feature gate reference](license.md#feature-gate-reference) for the full list and for the product names that differ from their flag.

### API

```bash
curl -s http://<control-plane>:8080/api/v1/enterprise/status | python3 -m json.tool
```

Community edition response:

```json
{
  "edition": "community",
  "licensee": "community",
  "features": []
}
```

Enterprise response:

```json
{
  "edition": "enterprise",
  "licensee": "Acme Corp",
  "features": ["audit", "billing", "inference_audit"],
  "expires": "2027-09-04T00:00:00Z"
}
```

---

## Requesting a license

Contact **andrew19881123@gmail.com** to obtain a commercial license.

The license is issued per-organization (licensee name embedded in the key) and covers production use of all enterprise features listed in the key's `features` array.

---

## Contribution licensing

Contributions are accepted under the Developer Certificate of Origin:

- Contributions to the **core** are licensed under the **MIT License**
- Contributions to the **`enterprise/` directory** are licensed under the **Purser Enterprise License**

See [CONTRIBUTING.md](https://github.com/andrew19881123/purser/blob/main/CONTRIBUTING.md) for details.
