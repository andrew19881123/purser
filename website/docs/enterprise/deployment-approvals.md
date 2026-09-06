# Deployment Approval Gates

> **Enterprise feature** — requires the `deployment_approvals` entitlement in your
> license key. Without a valid entitlement the deploy endpoint behaves as in the
> community edition (immediate rollout).

## Overview

The deployment approval gate implements the **human oversight** requirement of
**EU AI Act Article 14** for high-risk AI systems. When enabled, every model
deploy request is queued for explicit admin review before the rollout starts.

```
Operator                Control Plane              Admin
   |                         |                       |
   |-- POST /deploy model --> |                       |
   |                         |-- creates pending ---> [approval queue]
   |<-- 202 pending_approval -|                       |
   |                         |                       |
   |                         |    (admin reviews)    |
   |                         |<-- POST /approve ----- |
   |                         |-- starts actual deploy |
   |<-- deployment active ---|                       |
```

## Enabling the feature

Set the environment variable on the control plane to activate the feature:

```bash
PURSER_FEATURE_DEPLOYMENT_APPROVALS=1
```

This variable is checked at runtime by the license gate. The feature is only
active when **both** the environment variable is set **and** the license includes
the `deployment_approvals` entitlement.

Alternatively, the feature is activated automatically when your license key
includes `deployment_approvals` in its `features` list — no environment variable
is required in that case, as the license itself signals entitlement.

## Workflow

1. **Deploy request** — an operator (or CI pipeline) calls
   `POST /api/v1/models/{id}/deploy`. Instead of starting the rollout, the
   control plane creates an approval record with `status: "pending"` and returns:

   ```json
   {
     "status": "pending_approval",
     "deployment_id": "a1b2c3d4",
     "model_id": "llama3-8b",
     "message": "deployment queued for admin approval (AI Act Art.14); call POST /api/v1/approvals/a1b2c3d4/approve to proceed"
   }
   ```

2. **Admin review** — an admin opens the **Approvals** page in the UI (or polls
   `GET /api/v1/approvals?status=pending`) and inspects the request.

3. **Approve** — `POST /api/v1/approvals/{id}/approve` (admin role required):

   ```bash
   curl -X POST http://cp:8080/api/v1/approvals/a1b2c3d4/approve \
     -H 'Authorization: Bearer <admin-key>' \
     -H 'Content-Type: application/json' \
     -d '{"notes": "reviewed and approved for production"}'
   ```

   The deployment now proceeds. The approval record is updated to
   `status: "approved"` and an audit entry (`deployment.approval.approved`) is
   written to the tamper-evident audit log.

4. **Reject** — `POST /api/v1/approvals/{id}/reject`:

   ```bash
   curl -X POST http://cp:8080/api/v1/approvals/a1b2c3d4/reject \
     -H 'Authorization: Bearer <admin-key>' \
     -H 'Content-Type: application/json' \
     -d '{"notes": "model not cleared for this data category"}'
   ```

   The approval record is updated to `status: "rejected"`. The deployment is
   never started.

## API reference

### `GET /api/v1/approvals`

List approval records. Requires admin or viewer role.

**Query parameters**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `status`  | (all)   | Filter: `pending`, `approved`, or `rejected` |
| `limit`   | 50      | Maximum results (max 200) |

**Response**

```json
{
  "approvals": [
    {
      "id": 1,
      "deployment_id": "a1b2c3d4",
      "model_id": "llama3-8b",
      "requester": "sha256-of-api-key",
      "requested_at": "2026-09-05T14:23:11Z",
      "status": "pending",
      "reviewer": null,
      "reviewed_at": null,
      "notes": null
    }
  ]
}
```

### `GET /api/v1/approvals/{deploymentId}`

Retrieve a single approval record by deployment ID. Returns 404 if not found.

### `POST /api/v1/approvals/{deploymentId}/approve`

Approve a pending deployment. Admin role required. Returns 409 if the approval
is not in `pending` status.

**Request body** (optional)

```json
{ "notes": "free-text reason (optional)" }
```

### `POST /api/v1/approvals/{deploymentId}/reject`

Reject a pending deployment. Admin role required. Returns 409 if the approval
is not in `pending` status.

**Request body** (optional)

```json
{ "notes": "rejection reason (optional)" }
```

## UI

The **Approvals** page (sidebar → OPERATE → Approvals) shows the approval queue
with live status badges:

- **Pending** (yellow) — awaiting admin decision.
- **Approved** (green) — rollout was authorised.
- **Rejected** (grey) — rollout was cancelled.

Admins can filter by status and approve or reject directly from the table via a
confirm dialog that accepts optional notes.

!!! note "UI screenshot"
    The Approvals page renders a filterable table with status tabs (Pending / Approved / Rejected),
    approve/reject buttons with a confirm dialog for optional notes, and color-coded status badges.

## AI Act Art.14 compliance note

EU AI Act Article 14 requires that high-risk AI systems be designed to allow
**effective human oversight**, including the ability for natural persons to
intervene, interrupt, or override the AI system.

The deployment approval gate satisfies this by:

- **Intercepting** every production rollout before execution.
- **Requiring explicit human sign-off** from a named admin (recorded by
  `api_key_hash` in the `reviewer` field).
- **Creating an immutable audit trail** via the tamper-evident audit log
  (`deployment.approval.approved` / `deployment.approval.rejected` entries).
- **Storing structured metadata** (model ID, requester, reviewer, notes,
  timestamps) for compliance reporting.

Combine with the [enterprise audit log](./audit-log.md) to produce a complete
traceability record for regulatory audits.
