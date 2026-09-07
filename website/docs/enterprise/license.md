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
| **Features** | Blue badges, one per active feature gate (e.g. `audit`, `ha`, `rbac`) |
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
  "features": ["audit", "ha", "rbac"],
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
  Features:  audit, ha, rbac
  Valid now: yes
```

---

## Feature gate reference

| Feature flag | What it unlocks |
|---|---|
| `audit` | Tamper-evident, hash-chained audit log; every entry signed with an ed25519 key |
| `ha` | High-availability mode: Raft leader election, replicated registry, Gateway HA behind a VIP |
| `rbac` | Role-based access control, SSO / SAML / OIDC, LDAP / Active Directory integration |
| `fleet-scale` | MDM / Ansible / golden-image enrollment, signed air-gap bundles, enterprise CA, multi-cluster management |
| `opa_policies` | Open Policy Agent integration: Rego-based inference routing and admission policies |
| `chargeback` | Detailed per-tenant token and cost accounting; exportable billing reports |
| `deployment_approvals` | Human-in-the-loop approval gates for model deployments (AI Act Art. 14) |

Features are additive — include as many as the license entitles.

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
