# Azure ML Model Registry Integration

Purser can import models directly from an **Azure Machine Learning workspace**,
reading version metadata and artifact URIs from the Azure ML model registry via
the Azure Resource Manager REST API.

This integration is distinct from the plain Azure Blob Storage import (I2):
the Azure ML import queries the ML workspace registry to discover model versions,
their lifecycle stage (Production / Staging / …), and the associated Blob artifact
URI — rather than constructing a Blob URL directly.

## Prerequisites

1. A model registered in an Azure ML workspace with at least one version that
   has an associated Blob artifact (`properties.modelUri` set).
2. An Azure Active Directory **service principal** with the
   `AzureML Data Scientist` role (or `Contributor`) on the workspace, so it can
   call the Azure Resource Manager API.
3. The Purser control-plane process must have network access to
   `login.microsoftonline.com` (for the OAuth2 token) and
   `management.azure.com` (for the Azure ML API).

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `PURSER_AZURE_SUBSCRIPTION_ID` | Yes | Azure subscription ID that owns the ML workspace |
| `PURSER_AZURE_RESOURCE_GROUP` | Yes | Resource group containing the ML workspace |
| `PURSER_AZURE_ML_WORKSPACE` | Yes (or per-request) | Default Azure ML workspace name |
| `PURSER_AZURE_TENANT_ID` | Yes | Azure AD tenant ID for OAuth2 |
| `PURSER_AZURE_CLIENT_ID` | Yes | Service principal (app) client ID |
| `PURSER_AZURE_CLIENT_SECRET` | Yes | Service principal client secret |

The following variables are optional and mainly useful for testing or
non-standard Azure clouds:

| Variable | Default | Description |
|---|---|---|
| `PURSER_AZURE_ML_BASE_URL` | `https://management.azure.com` | Azure Resource Manager base URL |
| `PURSER_AZURE_TOKEN_URL` | `https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token` | OAuth2 token endpoint |

## Import Workflow

Send a `POST /api/v1/models/import` request to the Purser control-plane:

```bash
curl -X POST http://localhost:8080/api/v1/models/import \
  -H 'Content-Type: application/json' \
  -d '{
    "source":    "azureml",
    "model":     "llama-3-8b",
    "workspace": "my-ml-workspace",
    "version":   ""
  }'
```

### Request Fields

| Field | Required | Description |
|---|---|---|
| `source` | Yes | Must be `"azureml"` |
| `model` | Yes | Model name as registered in the Azure ML workspace |
| `workspace` | No | Overrides `PURSER_AZURE_ML_WORKSPACE` for this request |
| `version` | No | Specific version to import; empty selects the latest Production version (or the latest overall if no version is staged as Production) |

### Response (201 Created)

```json
{
  "model_id": "llama-3-8b",
  "source": {
    "type":         "azureml",
    "workspace":    "my-ml-workspace",
    "model":        "llama-3-8b",
    "version":      "3",
    "artifact_uri": "azureml://datastores/workspaceartifactstore/paths/ExperimentRun/..."
  }
}
```

The `model_id` is the model name. Re-importing the same model name returns
`409 Conflict` — delete the catalog entry first if you want to re-register.

## Authentication Flow

Purser uses the **OAuth2 client credentials** flow — no interactive sign-in,
no browser, no managed identity daemon required:

1. `POST https://login.microsoftonline.com/{PURSER_AZURE_TENANT_ID}/oauth2/v2.0/token`
   with `grant_type=client_credentials`, `scope=https://management.azure.com/.default`,
   `client_id`, and `client_secret`.
2. The resulting `access_token` is attached as `Authorization: Bearer …` on
   every subsequent Azure Resource Manager call.
3. Purser calls
   `GET https://management.azure.com/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.MachineLearningServices/workspaces/{ws}/models/{name}/versions?api-version=2023-04-01`
   to list model versions.

No Azure SDK dependency is used; all HTTP calls use Go's standard library.

## Differences from Plain Azure Blob Import (I2)

| Aspect | Azure ML Import (this page) | Azure Blob Import (I2) |
|---|---|---|
| Source | Azure ML workspace registry | Raw Azure Blob Storage container |
| Discovery | Lists ML model versions with stage / description metadata | Requires knowing the exact blob path |
| Artifact URI | Resolved from `properties.modelUri` in the ML registry | Constructed from `AZURE_STORAGE_ACCOUNT` + container + path |
| Auth | OAuth2 client credentials (`PURSER_AZURE_TENANT_ID` / `CLIENT_ID` / `CLIENT_SECRET`) | HMAC-SHA256 SAS token (`AZURE_STORAGE_ACCOUNT` / `AZURE_STORAGE_KEY`) |
| Stage filtering | Automatically prefers Production stage | N/A |

Use the Azure ML import when your models are tracked in an Azure ML workspace
(experiment runs, versioned datasets, custom environments). Use the plain Blob
import when you manage model artifacts directly in Blob Storage without an ML
workspace.
