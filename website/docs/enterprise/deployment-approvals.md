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

3. **Approve** — `POST /api/v1/approvals/{deploymentId}/approve` (admin role required):

   ```bash
   curl -X POST http://cp:8080/api/v1/approvals/a1b2c3d4/approve \
     -H 'Authorization: Bearer <admin-key>' \
     -H 'Content-Type: application/json' \
     -d '{"notes": "reviewed and approved for production"}'
   ```

   The deployment now proceeds. The approval record is updated to
   `status: "approved"` and an audit entry (`deployment.approval.approved`) is
   written to the tamper-evident audit log.

4. **Reject** — `POST /api/v1/approvals/{deploymentId}/reject`:

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

The response includes a `quorum` block showing real-time vote progress:

```json
{
  "id": 1,
  "deployment_id": "a1b2c3d4",
  "model_id": "llama3-8b",
  "requester": "sha256-of-api-key",
  "requested_at": "2026-09-08T20:00:00Z",
  "status": "pending",
  "required_approvals": 2,
  "quorum": {
    "required": 2,
    "received": 1,
    "remaining": 1,
    "approvers": [
      {
        "actor": "sha256-of-alice-key",
        "approved_at": "2026-09-08T21:00:00Z"
      }
    ]
  }
}
```

| Field | Description |
|-------|-------------|
| `quorum.required` | Approvals needed (from `min_approvers` or `required_approvals`) |
| `quorum.received` | Qualifying approvals received so far |
| `quorum.remaining` | `required − received`, clamped to 0 |
| `quorum.approvers` | List of reviewers who have approved (actor = SHA-256 hash of API key) |

When `reviewer_keys` is configured, only votes from those designated keys
count toward `received`.

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

### Quorum progress in the UI

When `min_approvers > 1` is configured, each pending row shows a compact
progress bar beneath the status badge indicating how many approvals have been
received out of how many are required (e.g. "1 of 2 approvals received").

The **Approve** button is disabled for the current session once that reviewer
has cast their vote, preventing accidental double-votes. Attempting to vote
again returns `409 Conflict` with `"error": "already_voted"` which the UI
surfaces as an inline error.

!!! note "UI screenshot"
    The Approvals page renders a filterable table with status tabs (Pending / Approved / Rejected),
    approve/reject buttons with a confirm dialog for optional notes, color-coded status badges,
    and — for multi-approver quorums — a progress bar showing received vs. required votes.

## Dual control (AI Act Art.14)

EU AI Act Article 14 requires that high-risk AI systems be designed to allow
**effective human oversight** — including the ability for natural persons to
intervene, interrupt, or override the AI system. A single admin approving
unilaterally may not be sufficient for systems classified as high-risk.

Purser's dual-control mode requires **two distinct admins** to each cast an
independent "approved" vote before the deployment is released, preventing any
single person from authorising a rollout alone.

### Quorum configuration

The recommended way to enforce dual-control is via `purser.yaml` rather than
per-request parameters. Add a `quorum` block to your cluster config:

```yaml
# purser.yaml
quorum:
  # Number of distinct approvals required (default: 1 — backward compatible).
  min_approvers: 2

  # Optional: restrict which API keys count toward quorum.
  # Leave empty to allow any admin to approve.
  reviewer_keys:
    - key-alice          # API key ID for Alice (CISO)
    - key-bob            # API key ID for Bob (Chief AI Officer)

  # Prevent the same reviewer from casting two votes on the same deployment.
  # Default: true. The storage layer always enforces this regardless.
  require_distinct: true
```

`min_approvers` and `reviewer_keys` are applied cluster-wide to every pending
approval gate. Individual deploy requests that already have a higher
`required_approvals` value continue to use whichever is larger.

**AI Act Art.14 compliance note (dual-control):** Setting `min_approvers: 2`
and `reviewer_keys` to a named set of senior reviewers ensures that no single
person can unilaterally authorise deployment of a high-risk AI model. The
designated reviewers are recorded in the immutable audit trail. This directly
satisfies the Art.14(1) requirement for "natural persons" being able to
"properly oversee" the AI system and the Art.14(4)(b) requirement that the
human oversight measures be commensurate with the risks.

#### Legacy per-request override

If you prefer per-request control (without a `purser.yaml` quorum block), set
`required_approvals` when you call `POST /api/v1/models/{id}/deploy`:

```json
{ "required_approvals": 2 }
```

The default is `1` (single-approver mode — backward-compatible with existing
deployments). Set `2` or higher to enforce dual or multi-person control.

### Dual-control workflow

```
Operator                Control Plane              Admin-1        Admin-2
   |                         |                       |               |
   |-- POST /deploy -------> |                       |               |
   |                         |-- creates pending ---> [queue r=2]    |
   |<-- 202 pending_approval -|                       |               |
   |                         |                       |               |
   |                         |<-- POST /approve ----- |               |
   |                         | vote recorded (1/2)    |               |
   |                         | quorum not reached     |               |
   |                         |                        |               |
   |                         |<-- POST /approve --------------------------------|
   |                         | vote recorded (2/2)                    |
   |                         | quorum reached → starts deploy         |
   |<-- deployment active ---|                                        |
```

1. **First vote** (`required_approvals=2`): the response carries
   `"quorum_reached": false` and the deployment stays `pending`.
2. **Second vote** (a different admin): the response carries
   `"quorum_reached": true` and the record transitions to `approved`.

### Anti self-approval

The requester of a deployment **cannot** approve it — even if the requester
has admin role. Attempting to do so returns `409 Conflict` with:

```json
{ "error": "self_approval_denied",
  "message": "the requester cannot approve their own deployment" }
```

This ensures that at least one human independent of the deployment initiator
reviews it before the rollout starts.

### Approval expiry

When creating an approval you may set an `expires_at` timestamp. Any approve
or reject attempt after that timestamp returns `410 Gone`:

```json
{ "error": "approval_expired",
  "message": "this approval request has expired" }
```

Expired approvals must be re-created (via a new deploy request) to be re-
evaluated.

### Vote response

Every call to `POST /api/v1/approvals/{deploymentId}/approve` now returns a vote-result
object rather than the raw approval record:

```json
{
  "voted": true,
  "quorum_reached": false,
  "approvals_so_far": 1,
  "approvals_needed": 2,
  "message": "Vote recorded. Waiting for 1 more approval(s)."
}
```

When `quorum_reached` is `true` the deployment has been released:

```json
{
  "voted": true,
  "quorum_reached": true,
  "approvals_so_far": 2,
  "approvals_needed": 2,
  "message": "Deployment approved and starting."
}
```

### Single-veto reject

A single reject is sufficient to block the deployment, regardless of
`required_approvals`. One veto outweighs any number of approvals. The reject
endpoint records the vote and immediately transitions the approval to
`rejected`.

## AI Act Art.14 compliance note

EU AI Act Article 14 requires that high-risk AI systems be designed to allow
**effective human oversight**, including the ability for natural persons to
intervene, interrupt, or override the AI system.

The deployment approval gate satisfies this by:

- **Intercepting** every production rollout before execution.
- **Requiring explicit human sign-off** from a named admin (recorded by
  `api_key_hash` in the `reviewer` field).
- **Dual-control option** (`required_approvals: 2`) prevents a single person
  from unilaterally authorising a high-risk model deployment.
- **Anti self-approval** enforcement ensures independence between the
  requestor and the reviewer.
- **Creating an immutable audit trail** via the tamper-evident audit log
  (`deployment.approval.approved` / `deployment.approval.rejected` entries).
- **Storing structured metadata** (model ID, requester, reviewer, notes,
  timestamps) for compliance reporting.
- **Per-vote records** in `deployment_approval_votes` provide a full audit
  trail of every individual reviewer decision.

Combine with the [enterprise audit log](./audit-log.md) to produce a complete
traceability record for regulatory audits.

---

## See also

- [AI Act compliance overview](ai-act-compliance.md) — full mapping of Purser controls to EU AI Act articles
- [Enterprise audit log](audit-log.md) — tamper-evident, hash-chained audit trail
- [Policy-as-code (OPA)](policy-as-code.md) — enforce deployment constraints with Rego policies
- [RBAC Permissions](../configuration/permissions.md) — roles that can approve or reject deployments
