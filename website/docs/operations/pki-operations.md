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

## Zero-downtime CA rotation

CA rotation replaces the active signing key.  Purser implements a **dual-trust
bundle** to ensure that leaf certificates issued under the old CA remain valid
while agents re-enroll — no hard cutover.

### How it works

1. `Rotate()` is called (via the admin API or the scheduled rotation job).
2. The old CA certificate moves into a "grace slot" with a 72-hour expiry
   (`RotationGracePeriod`).
3. A new CA keypair is generated and becomes the active signer.
4. `CertPool()` returns a pool that includes **both** the new active CA and the
   old CA (while `now < oldExpiry`).
5. TLS verification for both old-CA-signed and new-CA-signed leaf certs
   succeeds during the grace window.
6. After 72 hours the old CA is removed from the pool automatically.  Any agent
   still holding an old-CA cert at that point will fail mTLS and be forced to
   re-enroll.

### Step-by-step rotation procedure

```bash
# 1. Trigger rotation via the admin API
curl -X POST https://<control-plane>:8443/admin/pki/rotate \
     -H "Authorization: Bearer $ADMIN_TOKEN"

# 2. Verify the new CA serial is different
curl https://<control-plane>:8443/admin/pki/ca | jq .serial_number

# 3. Monitor agent re-enrollment during the grace window (72 h).
#    Watch for any agent that stops responding and force-reconnect it:
kubectl rollout restart deployment/purser-agent -n purser

# 4. After 72 h, confirm no agents are still using the old CA serial:
curl https://<control-plane>:8443/admin/pki/certs?state=issued \
     | jq '[.[] | select(.issuer_serial == "<old-serial>")]'
```

**Note:** the old CA serial is logged at rotation time.  Keep it in your
change-management record in case you need to identify stale agents.

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

1. **Immediately revoke all outstanding leaf certificates** via the admin API:
   ```bash
   curl -X POST https://<control-plane>:8443/admin/pki/revoke-all \
        -H "Authorization: Bearer $ADMIN_TOKEN"
   ```
2. **Rotate the CA** (this generates a new root and clears the grace slot for
   the compromised CA):
   ```bash
   curl -X POST https://<control-plane>:8443/admin/pki/rotate \
        -H "Authorization: Bearer $ADMIN_TOKEN"
   ```
3. **Force all agents to re-enroll** — the revoked certs will be rejected by
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
