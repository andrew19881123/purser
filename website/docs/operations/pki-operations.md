# PKI Operations

Purser's internal PKI provides mTLS identity to every Agent and Gateway in the
cluster.  The control plane acts as the Certificate Authority (CA): it issues,
renews, and revokes certificates, and exposes a trust bundle that all components
use for peer verification.

---

## Architecture

```
Root CA  (MaxPathLen=1, kept offline in production)
   │
   └── Intermediate CA  (MaxPathLen=0, online, signs leaf certs)
            │
            ├── Agent cert   (node-abc, 90-day TTL)
            ├── Agent cert   (node-xyz, 90-day TTL)
            └── Gateway cert (gw-1,     90-day TTL)
```

| Layer | Role | On-disk |
|---|---|---|
| Root CA | Self-signed; signs the intermediate only. In production, take offline after initial setup. | `<dir>/ca.crt`, `<dir>/ca.key` |
| Intermediate CA | Online signer; issues all leaf certificates (Agents, Gateways). | in-memory only (re-derived from Root on boot) |
| Leaf cert | Per-component ECDSA P-256 cert, 90-day TTL by default. | returned at enroll time |

### Cert fields on leaf certificates

| Field | Value |
|---|---|
| Subject | `CN=<node-id>, O=Purser, OU=agent\|gateway` |
| Key Usage | `DigitalSignature`, `KeyEncipherment` |
| Extended Key Usage | `ClientAuth`, `ServerAuth` |
| CRL Distribution Point | `http://control-plane.purser.internal/pki/crl.pem` |

---

## CA rotation

CA rotation replaces the active signing key.  The control plane's CA implements
a **dual-trust bundle** so that, *within a running process*, leaf certificates
issued under the old CA keep verifying while agents re-enroll.

Rotation is triggered with the `control-plane pki rotate` CLI subcommand — it is
intentionally **not** exposed over the management API (see
[Why CLI, not HTTP](#why-cli-not-http)).

### How the dual-trust bundle works

1. `Rotate()` moves the old CA certificate into a "grace slot" with a 72-hour
   expiry (`RotationGracePeriod`).
2. A new CA keypair is generated, becomes the active signer, and the new
   `ca.crt` / `ca.key` are written to the PKI directory.
3. `CertPool()` returns a pool that includes **both** the new active CA and the
   old CA (while `now < oldExpiry`), so old-CA-signed and new-CA-signed leaf
   certs both verify during the grace window.
4. After 72 hours the old CA is removed from the pool automatically.

!!! warning "The grace slot is in-memory only — a root rotation forces re-enrollment"
    The grace slot lives in the running control-plane process; it is **not**
    persisted to disk. `control-plane pki rotate` runs as a separate short-lived
    process, so the live control plane keeps serving the *old* CA until it is
    **restarted**, and on restart the CA loads only the new `ca.crt` with no
    grace slot. In practice a root rotation therefore **forces every enrolled
    agent to re-enroll under the new CA** — plan it as a maintenance event, not a
    hot swap. (Revocation is different: it takes effect immediately, no restart
    required — see [Disaster recovery](#disaster-recovery-ca-key-compromise).)

### Step-by-step rotation procedure

Run this on a control-plane host with access to the PKI directory
(`$PURSER_PKI_DIR`) and the registry (`$PURSER_DB`).

```bash
# 1. Note the current CA serial (for your change record).
openssl x509 -in "$PURSER_PKI_DIR/ca.crt" -noout -serial

# 2. Rotate the CA. --confirm is mandatory: the operation is irreversible.
control-plane pki rotate --pki-dir "$PURSER_PKI_DIR" --db "$PURSER_DB" --confirm
#    Logs: old_serial=<...> new_serial=<...>

# 3. Confirm the on-disk CA serial changed.
openssl x509 -in "$PURSER_PKI_DIR/ca.crt" -noout -serial

# 4. Restart the control plane so it adopts the new CA.
kubectl rollout restart deployment/purser-control-plane -n purser

# 5. Re-enroll agents under the new CA (they fail mTLS against the new root
#    until they do). A rolling restart re-triggers enrollment.
kubectl rollout restart deployment/purser-agent -n purser
```

**Note:** the old and new CA serials are logged at rotation time.  Keep them in
your change-management record so you can spot any agent still presenting an
old-CA cert.

### Why CLI, not HTTP

Rotation re-issues the cluster trust root and [revoke-all](#disaster-recovery-ca-key-compromise)
invalidates every live certificate — both are destructive, rare, and
cluster-wide. Exposing them over the management API would put a full outage one
stray request away. As CLI subcommands (like `backup` / `restore`) they require
shell and filesystem access to the control-plane data directory — the same trust
boundary that already protects `ca.key` — and cannot be triggered remotely.

---

## Passphrase-protected CA key at rest

### Why it matters

Without a passphrase, anyone who can read `ca.key` from the data directory can
impersonate the CA and issue arbitrary certificates for any node.  A passphrase
adds a second factor at rest (the passphrase itself is not stored on disk).

### Enabling passphrase protection

Set `PURSER_PKI_KEY_PASSPHRASE` in the control-plane environment **before** the
first start.  The key is then encrypted with AES-256-GCM (Argon2id key
derivation) when written to disk.

```yaml
# Kubernetes Secret (recommended)
apiVersion: v1
kind: Secret
metadata:
  name: purser-pki-passphrase
  namespace: purser
type: Opaque
stringData:
  passphrase: "change-me-to-something-strong"
```

```yaml
# Helm values
controlplane:
  env:
    - name: PURSER_PKI_KEY_PASSPHRASE
      valueFrom:
        secretKeyRef:
          name: purser-pki-passphrase
          key: passphrase
```

### Key derivation parameters

| Parameter | Value |
|---|---|
| Algorithm | Argon2id |
| Memory | 64 MiB |
| Iterations | 3 |
| Parallelism | 4 |
| Derived key length | 32 bytes (AES-256) |
| Cipher | AES-256-GCM |

### Rotating the passphrase

1. Take a backup of `ca.crt` and the encrypted `ca.key`.
2. Stop the control plane.
3. Decrypt the key with the old passphrase:
   ```bash
   PURSER_PKI_KEY_PASSPHRASE=old-pass purser-admin pki decrypt-key \
       --in ca.key --out ca.key.plain
   ```
4. Update the secret with the new passphrase.
5. Re-encrypt and restart.  The key is re-encrypted on the next write
   (triggered automatically by `Rotate()` or by deleting `ca.key` and
   restarting — the CA is then regenerated).

### Backward compatibility

If `PURSER_PKI_KEY_PASSPHRASE` is unset, plaintext PEM keys are read and
written unchanged.  Existing deployments that upgrade to v0.3 do not need to
set the passphrase immediately; they can migrate at their own pace by setting
the env var and triggering a rotation.

---

## Disaster recovery — CA key compromise

If you believe the CA private key has been exfiltrated:

1. **Immediately revoke all outstanding leaf certificates.** This takes effect
   at once — `VerifyClient` rejects revoked certs regardless of trust-bundle
   membership, so a running control plane enforces it without a restart:
   ```bash
   control-plane pki revoke-all --pki-dir "$PURSER_PKI_DIR" --db "$PURSER_DB" --confirm
   ```
2. **Rotate the CA** (generates a new root; the compromised CA is no longer the
   active signer). Restart the control plane afterwards so it adopts the new CA:
   ```bash
   control-plane pki rotate --pki-dir "$PURSER_PKI_DIR" --db "$PURSER_DB" --confirm
   ```
3. **Force all agents to re-enroll** — the revoked certs are rejected by
   `VerifyClient` even during the grace period because revocation is checked
   independently of trust-bundle membership.
4. **Rotate the passphrase** (see above) and restart the control plane.
5. **Audit** the registry `certs` table for any serial numbers not issued by
   your control plane (indicates the attacker used the key).

### How revocation interacts with the grace period

The dual-trust bundle and revocation are independent checks in `VerifyClient`:

1. The cert must chain to a trusted CA in `CertPool()` (trust bundle check).
2. The cert serial must not be in the `revoked` state in the registry (revocation
   check).

Revoking a cert removes it from service **immediately**, regardless of whether
its issuing CA is still in the grace window.  This means you can use revocation
as an emergency stop without waiting for the grace period to expire.

---

## Configuration reference

| Environment variable | Default | Description |
|---|---|---|
| `PURSER_PKI_KEY_PASSPHRASE` | _(empty)_ | Passphrase for AES-256-GCM key-at-rest encryption.  If unset, the key is stored as plaintext PEM (backward-compatible). |
| `PURSER_PKI_CA_TTL` | `87600h` (10 years) | Root CA validity window. |
| `PURSER_PKI_LEAF_TTL` | `2160h` (90 days) | Default leaf certificate TTL. |
| `PURSER_PKI_DIR` | `<data-dir>/pki` | Directory where `ca.crt` and `ca.key` are persisted. |
