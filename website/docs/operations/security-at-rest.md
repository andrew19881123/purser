# Encryption at Rest

## Overview

Purser stores all operational state — model registry, API keys, audit log,
deployment configuration — in a SQLite database (`purser-registry.db`) on
the control-plane PVC.

Out of the box this file is **not encrypted at the page level**. This page
documents the available options to protect it, from the simplest (cloud
provider encrypted storage) to the most thorough (SQLCipher or LUKS).

---

## Option 1: Kubernetes PVC Encryption (recommended)

The simplest approach is to use your cloud provider's encrypted storage class.
The data is encrypted and decrypted transparently by the storage driver — no
application changes are required.

### AWS EKS (EBS with KMS)

Create an encrypted `StorageClass`:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: purser-encrypted
provisioner: ebs.csi.aws.com
parameters:
  encrypted: "true"
  # Optional: use a customer-managed key instead of the AWS-managed default.
  # kmsKeyId: "arn:aws:kms:us-east-1:123456789012:key/mrk-..."
reclaimPolicy: Retain
volumeBindingMode: WaitForFirstConsumer
```

Then configure Purser to use it:

```yaml
# helm values
controlPlane:
  persistence:
    storageClass: purser-encrypted

security:
  encryptedPVC: true
  encryptionAnnotations:
    eks.amazonaws.com/encrypted: "true"
```

### GKE (CMEK with Persistent Disk)

On GKE, encryption is configured at the `StorageClass` level via a `DiskEncryptionKeyRef`.
No PVC annotations are required; set the storage class:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: purser-cmek
provisioner: pd.csi.storage.gke.io
parameters:
  disk-encryption-kms-key: projects/MY_PROJECT/locations/us-central1/keyRings/MY_RING/cryptoKeys/MY_KEY
  type: pd-ssd
reclaimPolicy: Retain
volumeBindingMode: WaitForFirstConsumer
```

```yaml
# helm values
controlPlane:
  persistence:
    storageClass: purser-cmek

security:
  encryptedPVC: true   # adds the metadata.annotations block to the PVC
  # encryptionAnnotations: {}  # no extra annotations needed for GKE CMEK
```

### Azure (Disk Encryption Set)

Create a `StorageClass` that references an Azure Disk Encryption Set:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: purser-des
provisioner: disk.csi.azure.com
parameters:
  diskEncryptionSetID: /subscriptions/.../resourceGroups/.../providers/Microsoft.Compute/diskEncryptionSets/myDES
  skuName: Premium_LRS
reclaimPolicy: Retain
volumeBindingMode: WaitForFirstConsumer
```

```yaml
# helm values
controlPlane:
  persistence:
    storageClass: purser-des

security:
  encryptedPVC: true
```

---

## Option 2: SQLCipher (page-level encryption)

SQLCipher provides AES-256-CBC page-level encryption inside the SQLite file.
Every page on disk is encrypted; the file is unreadable without the key even
when directly accessed by an OS user with read permission on the volume.

**Trade-off**: SQLCipher requires CGO, which breaks Purser's static binary build
and complicates cross-compilation. This option is therefore not supported in the
official pre-built images; it requires building from source.

For users who need page-level encryption and can build from source:

1. Replace `modernc.org/sqlite` with `github.com/mutecomm/go-sqlcipher/v4` in
   `go/controlplane/go.mod`.
2. In `go/controlplane/registry/sqlite.go`, open the database with a `_key`
   DSN parameter:
   ```go
   key := os.Getenv("PURSER_DB_KEY")
   conn := fmt.Sprintf("file:%s?_key=%s&_pragma=...", dsn, key)
   ```
3. Set `PURSER_DB_KEY` to a strong 32-character passphrase or a hex-encoded
   32-byte key.
4. Rebuild: `CGO_ENABLED=1 go build ./...`

!!! warning
    Once SQLCipher is enabled, the database file is permanently encrypted. Keep
    the key in a secrets manager (Vault, AWS Secrets Manager, etc.) — losing it
    means losing access to all data. Rotate the key with
    `PRAGMA rekey = 'new-key'` inside a transaction.

---

## Option 3: LUKS (Linux Unified Key Setup)

For on-premises bare-metal or VM deployments where Kubernetes storage classes
do not provide encryption, LUKS provides block-level encryption.

### Setup

```bash
# 1. Create a LUKS container on the data disk (e.g. /dev/sdb)
sudo cryptsetup luksFormat /dev/sdb

# 2. Open it
sudo cryptsetup luksOpen /dev/sdb purser-data

# 3. Format and mount
sudo mkfs.ext4 /dev/mapper/purser-data
sudo mkdir -p /data
sudo mount /dev/mapper/purser-data /data

# 4. Point Purser at the mount
export PURSER_DB=/data/purser-registry.db
```

For automatic unlock at boot, store the LUKS key in a TPM or a key-escrow
system rather than in `/etc/crypttab` in plain text.

---

## Database Integrity Check

Regardless of encryption strategy, Purser can verify database structural
integrity at startup using SQLite's built-in `PRAGMA integrity_check`.

Enable it at runtime:

```bash
PURSER_DB_INTEGRITY_CHECK=1 purser-control-plane
```

Or via the Helm chart:

```yaml
security:
  integrityCheckOnStart: true
```

When enabled, the check runs at the end of the migration pass and logs:

```
INFO  database integrity check passed
```

If corruption is detected, the process exits with an error:

```
registry: database integrity check failed: *** in database main ***
On tree page 5 cell 2: invalid page number 0
(consider restoring from backup)
```

**Performance**: the check scans up to 100 B-tree pages and typically adds
< 100 ms to startup for a small database (< 100 MB). For databases above
1 GB, budget ~1 s per GiB. Safe to leave enabled in production.

---

## CA Key Encryption

The internal PKI Certificate Authority private key is stored on the same PVC
under `PURSER_PKI_DIR`. It can be encrypted at rest using AES-256-GCM by
setting a passphrase:

```bash
export PURSER_PKI_KEY_PASSPHRASE="your-strong-passphrase"
```

The passphrase is used to derive an encryption key (PBKDF2-SHA256) that wraps
the CA private key on disk. At startup, the correct passphrase must be present
to decrypt and load the CA key.

See [PKI Operations](../configuration/cert-manager.md) for rotation procedures.

---

## Compliance Mapping

| Standard | Requirement | Covered by |
|---|---|---|
| ISO 27001 A.8.24 | Cryptographic controls | Option 1, 2, or 3 |
| SOC 2 PI1.5 | Cryptographic controls | Option 1, 2, or 3 |
| GDPR Art. 32 | Appropriate technical security measures | Option 1 sufficient |
| NIS2 Art. 21.2(h) | Encryption of data at rest | Option 1 sufficient |
| PCI DSS Req. 3.5 | Render stored data unreadable | Option 2 or 3 recommended |

!!! note
    "Option 1 sufficient" means that cloud-provider PVC encryption satisfies the
    control in the context of a properly access-controlled Kubernetes cluster. A
    penetration-tester or auditor with direct node access would still see
    unencrypted pages in a PVC snapshot. Where page-level protection is required
    (PCI, certain government frameworks), use Option 2 or 3.
