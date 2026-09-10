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
  "features": ["audit", "billing"],
  "issued": "2026-01-01T00:00:00Z",
  "expires": "2027-01-01T00:00:00Z"
}
```

The signature covers the raw payload bytes (not the base64 text). The whole
key is a single shell-safe string — no spaces, no special characters.

The strings in `features` are matched **byte-exactly** against the gates the
control plane enforces. The authoritative list of those strings, with the
product name each one corresponds to, is the
[feature gate reference](license.md#feature-gate-reference) — that table is the
single source of truth and is deliberately not repeated on this page.

---

## Production deployment guide

!!! info "v0.3: real trust root already provisioned"
    Starting in **v0.3**, `enterprise/license/license.go` embeds a **real
    production ed25519 public key** (`ProductionPublicKeyBase64`). The open-core
    model is now commercially functional — a signed `PURSER_LICENSE_KEY` will
    validate against the production binary without any further maintainer steps.
    The private signing key is stored securely and is never committed to the repo.
    Steps 1 and 2 below are documented for completeness and for future key
    rotation; operators deploying v0.3+ can skip directly to step 3.

### Step 1 — Generate a signing keypair (once, or on rotation)

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
          --feature audit --feature billing

        Valid --feature values are the gates the control plane enforces;
        see website/docs/enterprise/license.md ("Feature gate reference").
        sign rejects anything else, so a mistyped flag cannot reach a customer.
```

**Store the private key in a secret manager** (HashiCorp Vault, AWS Secrets
Manager, 1Password, etc.). Never commit it to version control.

### Step 2 — Embed the public key in your build

Edit `enterprise/license/license.go` and replace the `ProductionPublicKeyBase64`
constant with the value printed by `keygen`:

```go
const ProductionPublicKeyBase64 = "<base64 public key from keygen output>"
```

Commit and ship the binary. The embedded public key is not sensitive — anyone
who has the binary can read it. **This step is already done for v0.3 builds.**

### Step 3 — Sign a license key for a customer

```bash
purser-license sign \
  --key purser-license-signing.key \
  --licensee "Acme Corp" \
  --expires 2027-01-01T00:00:00Z \
  --feature audit \
  --feature billing \
  --feature policy_engine
```

This prints a single-line license key to **stdout** and nothing else, so it can
be piped or redirected safely. Send it to the customer over any channel (email,
ticket, secure paste). The key is not secret — its integrity is protected by the
ed25519 signature, not by secrecy.

For a time-bounded key using duration instead of a date:

```bash
purser-license sign \
  --key purser-license-signing.key \
  --licensee "Acme Corp" \
  --ttl 8760h \
  --feature audit --feature billing
```

Take each `--feature` value from the
[feature gate reference](license.md#feature-gate-reference). Product names are
not flag strings: the Chargeback capability is `billing`, and Policy-as-Code is
`policy_engine`. `sign` checks every value before it signs anything, so a wrong
one fails loudly rather than producing a key that quietly grants nothing.

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

The authoritative list of feature flag strings lives in one place only:
**[feature gate reference](license.md#feature-gate-reference)** on the License
Management page. It gives each flag, the product name it corresponds to, and
what it unlocks — including which product names differ from their flag string
(**Chargeback** is signed as `billing`) and which capabilities carry no gate at
all. Use that table when choosing the `--feature` values to sign into a key.

It is not duplicated here on purpose. An earlier revision of this page carried
its own copy of the table, and that copy drifted: it listed `ha`, `rbac` and
`fleet-scale` as flags (none of them is checked anywhere) and omitted most of
the flags that are. A second copy is how that happens, so there is now one
table and everything else links to it.

Features are additive — include as many as the customer's license entitles.
`purser-license sign` validates each `--feature` value against that list and
refuses an unrecognised one.

---

## Feature string validation

`purser-license sign` validates every `--feature` and `--features` value against
the gates the control plane actually enforces, and refuses to sign if any value
is unrecognised.

### Why the guard exists

Entitlement checking is a byte-exact string comparison — there is no
normalisation, aliasing, or wildcard. Before this validation existed, signing a
key with a wrong string failed silently on **both** sides of a commercial deal:

- the person signing saw a successful command and a valid-looking key;
- the customer installing it saw the feature listed in
  `GET /api/v1/enterprise/status`;
- every gated endpoint kept returning `402 Payment Required`.

And because `features` is inside the ed25519-signed payload, the key cannot be
corrected in place. It has to be reissued. The guard turns a silent commercial
failure into a loud local one.

### What a rejection looks like

Passing a product name where a flag string is required:

```console
$ purser-license sign --key purser-license-signing.key \
    --licensee "Acme Corp" --expires 2027-01-01T00:00:00Z \
    --feature audit --feature chargeback
purser-license: "chargeback" is not a feature flag the control plane enforces: "chargeback" is the product name; the licence flag for that capability is "billing"
  A key granting it would verify but unlock nothing, and the features array is
  inside the signed payload — such a key cannot be corrected, only reissued.
  Authoritative flag list: website/docs/enterprise/license.md (Feature gate reference).

Nothing was signed. Correct the flag, or pass --allow-unknown-feature to sign it deliberately (e.g. for an unreleased feature)
```

Nothing is written to stdout and the exit status is **1**. The valid features in
the same command are not signed either — the command is all-or-nothing, so there
is no half-issued key to clean up.

The error adapts to the kind of mistake:

| What you passed | What the error says |
|---|---|
| A product name (`chargeback`, `opa_policies`, `finops`) | Names the flag string that actually works |
| A typo (`inference_audi`, `gdrp`) | `did you mean "inference_audit"?` — nearest match by edit distance |
| A case or separator variant (`Billing`, `policy-engine`) | Points at the exact spelling, since matching is byte-exact |
| A capability with no gate (`ha`, `rbac`, `fleet-scale`, `slo`) | Explains that the capability is shipped and ungated, or never a licence feature, so no flag is needed. It does **not** offer a substitute, because there isn't one |

That last row matters: `--feature ha` is not a typo for anything. Suggesting a
nearest match would send you looking for a flag that does not exist.

### Signing an unreleased feature — `--allow-unknown-feature`

A flag string may legitimately need to be signed before the gate that reads it
ships. Pass `--allow-unknown-feature` to sign anyway:

```console
$ purser-license sign --key purser-license-signing.key \
    --licensee "Acme Corp" --ttl 8760h \
    --feature audit --feature unreleased_thing --allow-unknown-feature
WARNING: ============================================================
WARNING: --allow-unknown-feature was passed. Signed WITHOUT validation:
WARNING:   "unreleased_thing"
WARNING:
WARNING: No gate in the control plane checks these strings, so this key
WARNING: will verify and appear correct in /api/v1/enterprise/status while
WARNING: unlocking nothing. The features array is inside the ed25519-signed
WARNING: payload: this key CANNOT be corrected in place: it must be reissued.
WARNING: Do not send it to a customer unless you intend exactly this.
WARNING: ============================================================
eyJsaWNlbnNlZSI6IkFjbWUgQ29ycCIsImZlYXR1cmVz...
```

Notes on the override:

- It is an **explicit command-line flag, deliberately not an environment
  variable** — nothing in a shell profile or CI environment can switch the guard
  off by accident.
- The warning goes to **stderr**; stdout still contains only the key, so
  `sign ... > key.txt` keeps working and the warning still reaches a human.
- It only warns about the strings that failed validation. Valid features in the
  same command are not mentioned, and a command whose features are all valid
  prints no warning at all even with the flag present.
- Exit status is **0** — the key was produced.

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
  Features:  audit, billing
  Valid now: yes
```

Example output for an invalid key:

```
License: INVALID
  Error: signature verification failed
```

Exit codes: **0** = valid signature (check "Valid now" to know if it is in
date), **1** = invalid or malformed.

### Diagnosing a key that grants nothing

`verify` also flags feature strings that match no enforced gate. This is the
tool to reach for when a customer reports that a feature they were sold does
nothing — including for keys issued before `sign` validated its input:

```
License: VALID
  Licensee:  Acme Corp
  Expires:   2027-01-01
  Features:  audit, chargeback, ha
  Valid now: yes

  WARNING: 2 feature strings match no gate the control plane enforces:
    - "chargeback" — "chargeback" is the product name; the licence flag for that capability is "billing"
    - "ha" — the Raft HA control plane is shipped and ungated — no licence flag is checked for it, so a key does not need one
  These flags unlock nothing. Because they are inside the signed payload,
  the key must be reissued to fix them — it cannot be edited.
  Authoritative flag list: website/docs/enterprise/license.md
```

The **exit code stays 0**: the signature really is valid, and a key that grants
a useless string is not a forgery. Scripts that treat 0 as "good signature"
continue to work unchanged. Read the warning, not the exit status, to judge
whether a key entitles what it was meant to.

In the example above only `chargeback` needs a reissued key — the customer wanted
the Chargeback capability and the flag should have been `billing`. The `ha` entry
needs no key at all, because the HA control plane is not gated; dropping the
string from the next key is enough.

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
- `--allow-unknown-feature` weakens no cryptography — it only skips the check
  that a flag string means something. A key signed with it is exactly as
  tamper-proof as any other; it simply may grant nothing.

---

## Related pages

- [Feature gate reference](license.md#feature-gate-reference) — the authoritative
  flag strings, and the product name each one corresponds to
- [License Status Tile](license.md) — installing a key and reading it in the dashboard
- [Enterprise overview](overview.md)
