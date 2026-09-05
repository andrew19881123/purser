# GCP Vertex AI Model Registry Integration

Purser can import models directly from the
[GCP Vertex AI Model Registry](https://cloud.google.com/vertex-ai/docs/model-registry/introduction).
Once imported, the model entry in the Purser catalog carries the source
provenance (project, version, GCS artifact URI) so you can trace every
deployment back to the original registry record.

## Prerequisites

1. **A model registered in Vertex AI** with an associated GCS artifact URI.
   The model must be registered in a Vertex AI location that Purser can reach.

2. **A Google service account** (or a GCE / GKE workload identity) with at
   least the `roles/aiplatform.viewer` IAM role on the project that owns the
   model.

3. **A GCS artifact**: the model version must have an `artifactUri` set to a
   `gs://` URI. If your model was uploaded via `google.cloud.aiplatform.Model`
   with `artifact_uri`, this is already populated.

### How to register a model in Vertex AI

If your model weights live in GCS but are not yet in the registry, register
them with the `gcloud` CLI:

```bash
gcloud ai models upload \
  --region=us-central1 \
  --display-name=my-model \
  --artifact-uri=gs://my-bucket/my-model/ \
  --container-image-uri=us-docker.pkg.dev/vertex-ai/prediction/pytorch-cpu.2-0:latest
```

The `--artifact-uri` becomes the `artifactUri` field that Purser reads back.

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `PURSER_VERTEX_PROJECT` | Yes* | — | GCP project ID that owns the Vertex AI model. Not required when the `model` field in the import request is a full resource name (`projects/…`). |
| `PURSER_VERTEX_LOCATION` | No | `us-central1` | Vertex AI region where the model is registered. |
| `GOOGLE_APPLICATION_CREDENTIALS` | Yes* | — | Path to a service-account JSON key file. If unset, Purser falls back to the [GCE metadata server](https://cloud.google.com/compute/docs/metadata/default-metadata-values) (Application Default Credentials on GCE / GKE). |

## Import workflow

Send a `POST /api/v1/models/import` request to the Purser control-plane:

```bash
curl -X POST http://localhost:8080/api/v1/models/import \
  -H 'Content-Type: application/json' \
  -d '{
    "source":  "vertexai",
    "model":   "projects/my-project/locations/us-central1/models/llama-3",
    "version": ""
  }'
```

**Request fields**

| Field | Type | Description |
|---|---|---|
| `source` | string | Must be `"vertexai"`. |
| `model` | string | Full Vertex AI resource name (`projects/p/locations/l/models/m`) or a bare model ID (requires `PURSER_VERTEX_PROJECT`). |
| `version` | string | Specific version ID to import. Leave empty to import the latest version. |

**Example success response** (`201 Created`):

```json
{
  "model_id": "llama-3@1",
  "source": {
    "type":    "vertexai",
    "model":   "projects/my-project/locations/us-central1/models/llama-3",
    "version": "1",
    "gcs_uri": "gs://my-bucket/llama-3/v1/"
  }
}
```

The `model_id` (`{model-name}@{version}`) is the Purser catalog ID you use
for subsequent `GET /api/v1/models`, `POST /api/v1/models/{id}/deploy`, etc.

## Authentication details

Purser uses the **JWT Bearer Grant** (RFC 7523) to authenticate with the
Google OAuth2 token endpoint — no Google Cloud SDK is required:

1. The service-account JSON key is parsed with the Go standard library
   (`crypto/rsa`, `encoding/pem`, `crypto/x509`).
2. A JWT is constructed with `iss`, `sub`, `aud`, `scope`, `iat`, `exp`
   claims and signed with RSA-PKCS1v15-SHA256.
3. The signed JWT is exchanged at `https://oauth2.googleapis.com/token` for
   a short-lived access token (cached for the token's lifetime minus 30 s).
4. Every Vertex AI REST call carries `Authorization: Bearer <token>`.

If `GOOGLE_APPLICATION_CREDENTIALS` is not set, Purser falls back to the
GCE instance metadata server (`http://metadata.google.internal/…`) — this
works automatically on GCE, GKE, and Cloud Run.

## Errors

| HTTP status | Error code | Meaning |
|---|---|---|
| `400` | `missing_project` | Model name is a bare ID but `PURSER_VERTEX_PROJECT` is not set. |
| `404` | `no_versions` | The model exists in Vertex AI but has no registered versions. |
| `404` | `version_not_found` | The requested version ID does not exist. |
| `409` | `model_exists` | A model with the same ID (`name@version`) is already in the catalog. |
| `500` | `list_versions_failed` | Vertex AI API call failed (network, auth, or API error). |
