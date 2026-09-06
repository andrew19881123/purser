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

## Enterprise Source-Available — what's gated

The `enterprise/` directory is **source-available** under the [Purser Enterprise License](https://github.com/andrew19881123/purser/blob/main/enterprise/LICENSE). The code is **public** — you can view, compile, modify, and use it for development, evaluation, and testing. However, **use in production or for commercial purposes requires a valid commercial license**.

Enterprise features:

| Feature area | Status in v0.3 | Capabilities |
|---|---|---|
| **Identity & Access** | ✅ Shipped | RBAC (per-API-key roles), OIDC PKCE (EntraID / Okta / Keycloak). See [OIDC configuration](../configuration/oidc.md) and [RBAC](../configuration/rbac.md). |
| **Compliance** | ✅ Shipped | **Tamper-evident audit log** (hash-chained, offline-verifiable), strong per-tenant isolation, chargeback/usage accounting. See [Audit Log](audit-log.md). |
| **Policy-as-Code** | ✅ Shipped | Embedded OPA/Rego engine — version-controlled governance rules for deploy gating, model allowlists, and team-based access control. See [Policy-as-Code](policy-as-code.md). |
| **Fleet at Scale** | ✅ Shipped | MDM/Ansible/golden-image enrollment, signed air-gap bundles, enterprise CA integration, offline license validation. |
| **High Availability** | Targeted v0.4 | Leader election (Raft) + replicated registry; Gateway HA behind a VIP. Required for `replicaCount > 1` on the Control Plane. |

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
- **Enterprise edition**: the licensee name, a badge per enabled feature (`audit`, `ha`, `rbac`, …), and the expiry date. A red "Expired" badge appears when the key has passed its `expires` date.

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
  "features": ["audit", "ha", "rbac"],
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
