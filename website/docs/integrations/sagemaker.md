# AWS SageMaker Model Registry Integration

Purser can import approved models directly from an AWS SageMaker Model Package
Group into its catalog, making the SageMaker → Purser workflow a single API
call.

## How it works

1. You train a model and register it as a SageMaker Model Package (with a
   GGUF artifact stored in S3).
2. A reviewer approves the package in the SageMaker Model Registry.
3. You call `POST /api/v1/models/import` with `"source": "sagemaker"`.
4. Purser calls SageMaker's `ListModelPackages` and `DescribeModelPackage` APIs,
   extracts the S3 URI from `InferenceSpecification.Containers[0].ModelDataUrl`,
   and registers a new catalog entry.
5. The model is ready to plan and deploy with the normal Purser lifecycle.

No AWS SDK is required on the Purser control plane — requests are signed with
a minimal SigV4 (HMAC-SHA256) implementation using only the Go standard library.

## Prerequisites

- An AWS SageMaker **Model Package Group** with at least one package in
  **Approved** status.
- The model artifact must be a GGUF (or compatible) file stored in S3 and
  referenced in the package's `InferenceSpecification.Containers[0].ModelDataUrl`.
- IAM credentials with `sagemaker:ListModelPackages` and
  `sagemaker:DescribeModelPackage` permissions.

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `AWS_ACCESS_KEY_ID` | Yes | AWS access key for SigV4 signing |
| `AWS_SECRET_ACCESS_KEY` | Yes | AWS secret key for SigV4 signing |
| `PURSER_SAGEMAKER_REGION` | No | AWS region (default: `us-east-1`) |
| `PURSER_SAGEMAKER_MODEL_GROUP` | No | Default model package group name |

`PURSER_SAGEMAKER_MODEL_GROUP` can be overridden per request via the
`model_group` field in the request body.

## Import the latest approved model

```bash
curl -X POST http://localhost:8080/api/v1/models/import \
  -H "Content-Type: application/json" \
  -d '{
    "source": "sagemaker",
    "model_group": "my-llama-models"
  }'
```

Response (201 Created):

```json
{
  "model_id": "my-llama-models-v3",
  "source": {
    "type":    "sagemaker",
    "arn":     "arn:aws:sagemaker:us-east-1:123456789012:model-package/my-llama-models/3",
    "s3_uri":  "s3://my-bucket/models/v3/model.tar.gz",
    "version": 3
  }
}
```

### Import a specific version

Add `"version": N` to select a particular approved package version instead of
the latest:

```bash
curl -X POST http://localhost:8080/api/v1/models/import \
  -H "Content-Type: application/json" \
  -d '{
    "source":       "sagemaker",
    "model_group":  "my-llama-models",
    "version":      2
  }'
```

## End-to-end workflow

```
Train model
    │
    ▼
Register as SageMaker Model Package
(set InferenceSpecification.Containers[0].ModelDataUrl = s3://...)
    │
    ▼
Reviewer approves the package in SageMaker Model Registry
    │
    ▼
POST /api/v1/models/import   ← Purser fetches metadata via SigV4-signed API calls
    │
    ▼
POST /api/v1/models/{id}/plan   ← Purser plans the deployment across the fleet
    │
    ▼
POST /api/v1/models/{id}/deploy  ← Purser deploys the model
```

## Error reference

| HTTP status | Error code | Meaning |
|---|---|---|
| 400 | `missing_model_group` | `model_group` not set in body or `PURSER_SAGEMAKER_MODEL_GROUP` env |
| 404 | `no_approved_packages` | The group exists but has no Approved packages |
| 404 | `version_not_found` | The requested version is not in the Approved list |
| 409 | `model_exists` | A model with this ID (`{group}-v{N}`) is already in the catalog |
| 500 | `sagemaker_error` | SageMaker API call failed (check credentials/region) |

## Notes

- The SageMaker API calls made are:
  - `POST https://api.sagemaker.{region}.amazonaws.com/ListModelPackages`
  - `POST https://api.sagemaker.{region}.amazonaws.com/DescribeModelPackage`
- Requests are signed with AWS Signature Version 4 (`service=sagemaker`).
- The model family (llama, mistral, falcon, …) is inferred from the package
  group name and description. Set it explicitly via `PUT /api/v1/models/{id}`
  if the heuristic misidentifies it.
- Purser stores the source ARN and S3 URI in the model's `spec` field for
  traceability but does not download the artifact itself — that happens at
  deploy time via the node agents.
