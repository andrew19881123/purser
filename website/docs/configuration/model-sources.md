# Model Sources: S3, GCS, and Azure Blob Storage

Purser can import model weights stored in any of the three major object-storage
systems. At import time the control plane resolves the URI to an HTTPS download
URL; the agent fetches the weights at deploy time. No streaming or proxying
happens through the control plane.

## How it works

1. **Upload** your model weights to an S3 bucket, GCS bucket, or Azure Blob
   container.
2. **Import** the model into the Purser catalog with `POST /api/v1/models/import`,
   providing the object-storage URI and catalog metadata.
3. The control plane resolves the URI to a pre-signed HTTPS URL (1-hour TTL)
   and stores it in the model's `source` field.
4. When you **deploy** the model, the agent reads `source.download_url` and
   fetches the weights directly from object storage.

```
┌───────────────┐  import request   ┌─────────────────┐
│  Purser CLI   │ ───────────────▶  │  Control Plane  │
│  / operator   │                   │                 │
└───────────────┘                   │  resolves URI → │
                                    │  pre-signed URL │
                                    └────────┬────────┘
                                             │ model.source.download_url
                                    ┌────────▼────────┐    ┌──────────────┐
                                    │     Agent       │───▶│ Object Store │
                                    └─────────────────┘    └──────────────┘
```

## Import request format

```http
POST /api/v1/models/import
Content-Type: application/json

{
  "source":  "s3",
  "uri":     "s3://my-models-bucket/llama-3.1-8b/llama-3.1-8b-q4_k_m.gguf",
  "name":    "llama-3.1-8b",
  "family":  "llama",
  "size_gb": 4.8
}
```

| Field     | Required | Description                                                   |
|-----------|----------|---------------------------------------------------------------|
| `uri`     | yes      | Object-storage URI (`s3://`, `gs://`, or `az://`)             |
| `name`    | no       | Model ID in the catalog. Defaults to the object key basename. |
| `family`  | no       | Model family (e.g. `llama`, `mistral`, `phi`).                |
| `size_gb` | no       | Weight size hint used by the planner's memory-fit check.      |
| `source`  | no       | Informational; the scheme is inferred from `uri`.             |

---

## Amazon S3

### URI format

```
s3://<bucket>/<key>
```

Examples:

```
s3://my-models/llama-3.1-8b/llama-3.1-8b-q4_k_m.gguf
s3://private-weights/mistral-7b-instruct-v0.3.gguf
```

### Environment variables

| Variable               | Description                                              |
|------------------------|----------------------------------------------------------|
| `AWS_ACCESS_KEY_ID`    | AWS access key ID for pre-signing.                       |
| `AWS_SECRET_ACCESS_KEY`| AWS secret access key for pre-signing.                   |
| `PURSER_S3_REGION`     | AWS region (default: `us-east-1`).                       |

All three must be set for pre-signed URL generation. When any one is absent the
control plane returns the public virtual-hosted URL
(`https://<bucket>.s3.<region>.amazonaws.com/<key>`), which works for
publicly-accessible buckets.

### Signing method

When credentials are present, a minimal AWS SigV4 query-string pre-signer
generates the URL (no SDK dependency). The URL is valid for **1 hour**.

### Public bucket case

If your bucket is publicly accessible (e.g. for open-weight models), you can
import without setting any credentials:

```bash
# No AWS credentials needed for public buckets.
curl -X POST http://cp:8080/api/v1/models/import \
  -H 'Content-Type: application/json' \
  -d '{
    "uri":    "s3://public-weights/llama-3.1-8b.gguf",
    "name":   "llama-3.1-8b",
    "family": "llama"
  }'
```

---

## Google Cloud Storage

### URI format

```
gs://<bucket>/<object>
```

Examples:

```
gs://my-gcs-bucket/weights/llama-3.1-8b-q4.gguf
gs://ml-weights/phi-3-mini-128k-instruct.gguf
```

### Environment variables

| Variable                       | Description                                        |
|--------------------------------|----------------------------------------------------|
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to a service-account JSON key file.         |

When `GOOGLE_APPLICATION_CREDENTIALS` is absent the control plane returns the
public XML API URL (`https://storage.googleapis.com/<bucket>/<object>`), which
works for objects with public read access.

### Signing method

When a service-account file is present, the control plane reads the RSA private
key from the file and generates a **GOOG4-RSA-SHA256** V4 signed URL valid for
**1 hour**. Only `service_account` credential type is supported.

### Service account setup

The service account needs the `Storage Object Viewer` role (or
`roles/storage.objectViewer`) on the bucket. Create and download a JSON key:

```bash
gcloud iam service-accounts keys create sa-key.json \
  --iam-account purser-agent@my-project.iam.gserviceaccount.com
export GOOGLE_APPLICATION_CREDENTIALS=/etc/purser/sa-key.json
```

---

## Azure Blob Storage

### URI format

```
az://<container>/<blob>
```

Examples:

```
az://models/llama-3.1-8b/llama-3.1-8b-q4_k_m.gguf
az://weights/mistral-7b-instruct.gguf
```

### Environment variables

| Variable               | Description                                                |
|------------------------|------------------------------------------------------------|
| `AZURE_STORAGE_ACCOUNT`| Storage account name.                                      |
| `AZURE_STORAGE_KEY`    | Storage account access key (base64-encoded, from the portal).|

When either variable is absent the control plane returns a plain HTTPS URL
(`https://<account>.blob.core.windows.net/<container>/<blob>`). Set both for
SAS-signed URLs.

### Signing method

When both variables are present, an HMAC-SHA256 Shared Access Signature (SAS)
is generated using service version `2020-02-10`. The SAS grants read-only
(`sp=r`) access over HTTPS only (`spr=https`) for **1 hour**.

### Finding the account key

In the Azure portal: **Storage account → Security + networking → Access keys**.
Copy the base64 `key1` value.

---

## Example workflow

```bash
# 1. Upload your model weights.
aws s3 cp llama-3.1-8b-q4_k_m.gguf s3://my-models-bucket/llama-3.1-8b/

# 2. Export credentials so the control plane can generate a pre-signed URL.
export AWS_ACCESS_KEY_ID=AKIA...
export AWS_SECRET_ACCESS_KEY=...
export PURSER_S3_REGION=us-east-1

# 3. Import the model into the Purser catalog.
curl -X POST http://control-plane:8080/api/v1/models/import \
  -H 'Content-Type: application/json' \
  -d '{
    "uri":     "s3://my-models-bucket/llama-3.1-8b/llama-3.1-8b-q4_k_m.gguf",
    "name":    "llama-3.1-8b",
    "family":  "llama",
    "size_gb": 4.8
  }'
# → {"model_id":"llama-3.1-8b","source_type":"s3","download_url":"https://..."}

# 4. Deploy the model — the agent will fetch the weights from the download_url.
curl -X POST http://control-plane:8080/api/v1/models/llama-3.1-8b/deploy
```

## Notes

- **Pre-signed URL lifetime**: URLs are valid for 1 hour from the time the
  import request is made. The URL is stored in `model.source.download_url` in
  the registry; if you need to refresh it, re-import the model (delete + import).
  Long-running deployments should be set up before the URL expires.
- **IAM / network access**: the Purser agent must be able to reach the object
  store endpoint (`*.amazonaws.com`, `storage.googleapis.com`,
  `*.blob.core.windows.net`). Adjust firewall rules and VPC endpoints as needed.
- **Private vs public buckets**: for internal, airgapped deployments keep
  buckets private and always supply credentials. For open-weight models on
  public buckets, credentials are optional.
