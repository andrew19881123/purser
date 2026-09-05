# Enterprise Licensing

## Open-core model

Purser follows an open-core model:

- **MIT-licensed core** — everything outside the `enterprise/` directory is
  free, open-source software. You may run, study, modify, and redistribute it
  — including for commercial purposes — with no restrictions beyond the MIT
  license terms.

- **Source-available enterprise** — the `enterprise/` directory is published
  under the [Purser Enterprise License](../../enterprise/LICENSE). You may view,
  compile, and use enterprise code for development, evaluation, and testing at
  no cost. **Production or commercial use requires a valid commercial license**
  activated at runtime with a `PURSER_LICENSE_KEY`.

This is the same model used by projects like LiteLLM: the code ships in the
open, but the license check enforces commercial terms for production use.

To obtain a commercial license: **andrew19881123@gmail.com**.

---

## How the license key works

License keys are **offline ed25519-signed tokens**. There is:

- No phone-home, no license server, no network dependency of any kind.
- Full support for **air-gapped environments**: a key is a single
  copy-pasteable string. Any node can verify it locally against the public key
  that was compiled into the binary.

### Key format

```
base64url(payloadJSON) "." base64url(ed25519_signature)
```

`payloadJSON` is a UTF-8 JSON object:

```json
{
  "licensee": "Acme Corp",
  "features": ["audit", "ha", "rbac"],
  "issued": "2026-01-01T00:00:00Z",
  "expires": "2027-01-01T00:00:00Z"
}
```

The signature covers the raw payload bytes (not the base64 text). The whole
key is a single shell-safe string — no spaces, no special characters.

---

## Production deployment guide

### Step 1 — Generate a signing keypair (once per organization)

```bash
purser-license keygen --output-key
```

This prints step-by-step instructions and writes the private key to
`purser-license-signing.key` (already in `.gitignore`):

```
Step 1: Save this PRIVATE key securely (never commit or share it):

-----BEGIN PURSER LICENSE SIGNING KEY-----
<base64 private key>
-----END PURSER LICENSE SIGNING KEY-----

  (also written to purser-license-signing.key with mode 0600)

Step 2: Embed this PUBLIC key in your Purser build by replacing
        ProductionPublicKeyBase64 in enterprise/license/license.go:

  const ProductionPublicKeyBase64 = "<base64 public key>"

Step 3: Sign licenses with: purser-license sign --key purser-license-signing.key \
          --licensee "Acme Corp" --expires 2027-01-01T00:00:00Z \
          --feature audit --feature ha
```

**Store the private key in a secret manager** (HashiCorp Vault, AWS Secrets
Manager, 1Password, etc.). Never commit it to version control.

### Step 2 — Embed the public key in your build

Edit `enterprise/license/license.go` and replace the placeholder value:

```go
const ProductionPublicKeyBase64 = "<base64 public key from keygen output>"
```

Commit and ship the binary. The embedded public key is not sensitive — anyone
who has the binary can read it.

### Step 3 — Sign a license key for a customer

```bash
purser-license sign \
  --key purser-license-signing.key \
  --licensee "Acme Corp" \
  --expires 2027-01-01T00:00:00Z \
  --feature audit \
  --feature ha \
  --feature rbac
```

This prints a single-line license key to stdout. Send it to the customer
over any channel (email, ticket, secure paste). The key is not secret — its
integrity is protected by the ed25519 signature, not by secrecy.

For a time-bounded key using duration instead of a date:

```bash
purser-license sign \
  --key purser-license-signing.key \
  --licensee "Acme Corp" \
  --ttl 8760h \
  --feature audit --feature ha
```

### Step 4 — Customer sets the key

The customer sets the environment variable before starting Purser:

```bash
export PURSER_LICENSE_KEY="<key string from step 3>"
./purser-control-plane
```

Or in a systemd unit, Kubernetes Secret, Docker env file, etc. The control
plane reads `PURSER_LICENSE_KEY` at startup and enables the licensed features.

---

## Feature reference

| Feature flag | What it unlocks |
|---|---|
| `audit` | Tamper-evident, hash-chained audit log (append-only; every entry signed) |
| `ha` | High-availability mode: Raft leader election, replicated registry, Gateway HA behind a VIP |
| `rbac` | Role-based access control, SSO/SAML/OIDC, LDAP/Active Directory integration |
| `fleet-scale` | MDM/Ansible/golden-image enrollment, signed air-gap bundles, enterprise CA integration, multi-cluster fleet management |

Features are additive — include as many as the customer's license entitles.

---

## Verifying a license key

Use `purser-license verify` to inspect a key before distributing it or to
troubleshoot a customer's deployment:

```bash
purser-license verify <key-string>
```

Or let it read from the environment:

```bash
PURSER_LICENSE_KEY="<key>" purser-license verify
```

Example output for a valid key:

```
License: VALID
  Licensee:  Acme Corp
  Expires:   2027-01-01
  Features:  audit, ha, rbac
  Valid now: yes
```

Example output for an invalid key:

```
License: INVALID
  Error: signature verification failed
```

Exit codes: **0** = valid signature (check "Valid now" to know if it is in
date), **1** = invalid or malformed.

### Verifying against the development key

To verify a test/development-signed key (useful in CI before you have
provisioned production keys):

```bash
purser-license verify --dev <key-string>
```

---

## Renewal and expiry

A license key is valid while the current time is strictly between the `issued`
and `expires` fields. There is no grace period enforced by the software — the
moment `expires` is reached, `HasFeature()` calls return false and enterprise
features are disabled.

**Plan renewals before expiry.** The renewal workflow is identical to the
initial provisioning: run `purser-license sign` with a new `--expires` date
and send the customer a replacement key. They update `PURSER_LICENSE_KEY` and
restart (or send a `SIGHUP` if hot-reload is enabled).

Expired keys continue to verify (exit 0 from `purser-license verify`) — the
signature is valid even though the key is out of date. Check "Valid now" in
the output to distinguish the two cases.

---

## Security notes

- The **private signing key** is the trust root for your entire license
  infrastructure. Treat it like a CA private key: store it in a hardware
  security module or secret manager, rotate it if it is ever exposed, and
  never put it in version control or CI environment variables.
- The **public key** embedded in the binary is not sensitive. Shipping it in
  the open-source tree is fine and expected.
- License keys are **not secret**. Their validity is enforced by the
  cryptographic signature, not by keeping the key string hidden.
- The license check is **entirely offline**. Air-gapped deployments are fully
  supported with no special configuration.
