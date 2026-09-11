# License Status Tile

The **Settings** page includes a **License** card that shows at a glance
whether the instance is running the MIT Core edition or a licensed Enterprise
build, along with expiry details and enabled feature gates.

---

## What the license tile shows

### MIT Core mode

When no license key is configured, or when the control plane returns a 404 on
the license endpoint, the tile shows:

```
Community  (badge)
You are running the MIT-licensed community edition.
Upgrade for HA, RBAC, OIDC, audit log, and more.
```

There is no error state — the absence of a license is a valid, supported
configuration (the fully open-source MIT Core).

### Enterprise mode

When a valid `PURSER_LICENSE_KEY` is loaded, the tile shows:

| Field | Description |
|-------|-------------|
| **Edition badge** | Green "Enterprise" badge |
| **Licensee** | Organisation name embedded in the key payload |
| **Features** | Blue badges, one per active feature gate (e.g. `audit`, `billing`, `policy_engine`) — the raw flag strings from the key payload, not product names |
| **Expires** | Expiry date in the local locale format |
| **Days remaining** | Countdown "(N days remaining)" beside the expiry date — absent once the key has expired |
| **Expired badge** | Red "Expired" badge shown when the current date is past the expiry |

---

## How to install an enterprise license

### Option 1 — Environment variable (recommended)

Set `PURSER_LICENSE_KEY` before starting the control plane:

```bash
export PURSER_LICENSE_KEY="<key string from Purser team>"
./purser-control-plane
```

In Kubernetes, store the key in a Secret and reference it in the Pod spec:

```yaml
env:
  - name: PURSER_LICENSE_KEY
    valueFrom:
      secretKeyRef:
        name: purser-license
        key: key
```

### Option 2 — purser.yaml configuration

```yaml
enterprise:
  license_key: "<key string>"
```

The control plane merges `purser.yaml` with environment variables; the env var
takes precedence if both are set.

### Option 3 — REST API

You can upload a key at runtime without restarting:

```bash
curl -s -X POST http://localhost:8080/api/v1/enterprise/license \
  -H "Authorization: Bearer <admin-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"license_key": "<key string>"}'
```

The endpoint validates the signature and activates the features immediately.
A `200 OK` response confirms the key was accepted:

```json
{
  "edition": "enterprise",
  "licensee": "Acme Corp",
  "features": ["audit", "billing", "inference_audit"],
  "expires": "2027-01-01T00:00:00Z"
}
```

**Error cases:**

| Status | Meaning |
|--------|---------|
| `400` | Malformed key (not a valid base64url.signature string) |
| `403` | Signature verification failed (wrong key or tampered payload) |
| `422` | Key is structurally valid but already expired |

### Verifying a key before installing

```bash
purser-license verify "<key string>"
```

Output for a valid, in-date key:

```
License: VALID
  Licensee:  Acme Corp
  Expires:   2027-01-01
  Features:  audit, billing, inference_audit
  Valid now: yes
```

The `Features:` line echoes the flag strings exactly as they were signed into the
key. `purser-license sign` validates every `--feature` value against the gates
below and refuses to sign an unrecognised one, so a fresh key cannot carry a dead
flag by accident. Two cases still warrant a look at this output: keys minted
before that check existed, and keys deliberately signed with
`--allow-unknown-feature`. `verify` names any dead flags it finds.

---

## Feature gate reference

This table is the authoritative list of the flag strings the control plane
enforces. A string that does not appear here has no effect: the key will still
verify and the flag will still show up in `/api/v1/enterprise/status`, but no
endpoint is unlocked by it.

| Feature flag | Product name | What it unlocks |
|---|---|---|
| `audit` | Tamper-Evident Audit Log | Hash-chained control-plane audit log with end-to-end chain verification — `GET /api/v1/enterprise/audit-log`. See [Audit Log](audit-log.md). |
| `inference_audit` | Inference Audit Log | **Reading** per-request inference records — `GET /api/v1/inference-audit`, `/inference-audit/verify`, and the GDPR Art.30 record of processing. On its own it also opens the AI Act technical-doc endpoint (see `ai_act_compliance`). See [Inference Audit Log](inference-audit.md). |
| `billing` | Chargeback / FinOps | Full per-tenant chargeback reports, burn-rate forecasting, model adoption, and org/team billing rollups. See [Chargeback](chargeback.md) and [FinOps](finops.md). |
| `deployment_approvals` | Deployment Approval Gates | Human-in-the-loop approval gates for model deployments (AI Act Art.14). See [Deployment Approvals](deployment-approvals.md). |
| `policy_engine` | Policy-as-Code | Embedded OPA/Rego evaluation on deploy and inference actions. See [Policy-as-Code](policy-as-code.md). |
| `ai_act_compliance` | AI Act Compliance | The AI Act Art.11 / Annex IV technical documentation endpoint — `GET /api/v1/compliance/ai-act/technical-doc`. This endpoint accepts **either** `ai_act_compliance` **or** `inference_audit`; a key holding just one of the two is enough. See [AI Act Compliance](ai-act-compliance.md). |
| `gdpr` | GDPR Right to Erasure | The erasure endpoint and erasure audit log. See [GDPR Compliance](gdpr-compliance.md). |

Features are additive — include as many as the license entitles.

### Product name vs. flag string

A capability's product name and its licence-key flag string are chosen
independently, and for two capabilities they differ. **Chargeback** is the
product name; `billing` is the flag string that must appear in the key's
`features` array. Likewise the Policy-as-Code capability is gated on
`policy_engine`. Always take the value from the **Feature flag** column above
when signing a key or writing a purchase order — the product name will not work.

!!! warning "Flag strings are matched exactly"
    The entitlement check is a literal string comparison against the `features`
    array in the signed payload. There is no normalisation, no aliasing, and no
    wildcard: `chargeback`, `Billing`, and `opa_policies` are all simply absent
    as far as the control plane is concerned. Because the payload is signed, a
    key with the wrong string cannot be corrected in place — it has to be
    reissued.

### Capabilities with no feature gate

Some capabilities documented in this section are not gated by a licence feature
in the current release. They are listed here so that nobody signs a key
containing a flag that does nothing:

| Capability | Current state |
|---|---|
| SLO contracts | Shipped and ungated — `GET /api/v1/slo/compliance` answers for any admin or viewer, with or without a key. See [SLO Contracts](slo.md). |
| What-if planner | Shipped and ungated — `POST /api/v1/planner/what-if`. |
| Raft HA control plane | Shipped and ungated at runtime; no `ha` flag is checked. See [HA Control Plane](ha-control-plane.md). |
| RBAC and OIDC | Shipped, and always enforced in every edition — never a licence feature. |
| Ansible fleet enrollment | Shipped and ungated. See [Ansible](../integrations/ansible.md). |
| Internal CA / PKI | Shipped and ungated — certificate issue, renewal, revocation, and rotation. |

Earlier revisions of this page listed `ha`, `rbac`, `fleet-scale`, `opa_policies`,
and `chargeback` as feature flags. None of those five strings is checked
anywhere, so none of them entitles anything. For `opa_policies` and `chargeback`
use `policy_engine` and `billing` instead; for `ha` and `rbac` see the rows above.

`fleet-scale` covered several capabilities at once, and they are not in the same
state: Ansible enrollment and the internal CA are shipped (and ungated, as
above), while MDM and golden-image enrollment, signed air-gap bundles, and
multi-cluster fleet management have no implementation we can point you at in this
release. Treat that group as roadmap rather than as something a key can unlock,
and ask before committing to it contractually. Note that air-gapped *operation*
is fully supported and always has been — licence verification is offline by
design — which is a different thing from a signed bundle artefact.

"Ungated" describes runtime behaviour only — which endpoints answer without a
key. It is a separate question from the licence terms that govern the code
itself; see [Enterprise Licensing](licensing.md) for those. Whether any of these
capabilities gains a gate in a future release is a roadmap question, and this
page will be updated if one does.

---

## Renewal and expiry

A license key expires at the UTC timestamp in its `expires` field. There is no
grace period — at the moment `expires` is reached, `HasFeature()` returns false
and enterprise features are disabled.

**Plan renewals before expiry.** The dashboard's "days remaining" counter makes
this easy to track. When you receive a renewal key from the Purser team, install
it via any of the methods above. The new key takes effect immediately; no restart
is required when using the REST API upload.

The control plane also emits an `license.expiry_warning` audit event 14 days
before expiry if the `audit` feature is active.

---

## Related pages

- [Enterprise Licensing — technical deep-dive](licensing.md) (key format, offline verification, keygen)
- [API Key Lifecycle](../configuration/api-keys.md)
- [Audit Log](audit-log.md)
- [Enterprise overview](overview.md)
