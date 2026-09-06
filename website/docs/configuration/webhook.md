# Webhook Notifications

Purser can POST a JSON payload to a configurable HTTP(S) endpoint whenever the
reconciler raises an event that requires operator sign-off before it will act.
Use this to integrate with on-call tools like Slack, PagerDuty, or custom
runbook automation.

---

## Events that trigger a webhook

| Event | When | Why operator sign-off |
|---|---|---|
| `approval_required` / `node_down` | A node hosting an active deployment becomes unreachable | Multi-node failover is higher-risk than a local engine restart; the default policy requires explicit approval before the reconciler moves the deployment |

The webhook fires in the `approval_required` automation path. Events handled
autonomously (`auto`) or silently (`notify_only`) do not trigger webhooks.

See [Reconciler](../architecture/reconciler.md) for the full automation-level
policy and how to override per event type.

---

## Configuration

| Env var | Default | Description |
|---|---|---|
| `PURSER_RECONCILER_WEBHOOK_URL` | (empty) | HTTP(S) URL to POST to. Delivery is disabled when empty. |
| `PURSER_RECONCILER_WEBHOOK_RETRIES` | `3` | Maximum POST attempts before abandoning delivery. |

Set these alongside the other reconciler tuning variables (see
[Environment Variables](./env-vars.md)):

```bash
PURSER_RECONCILER_WEBHOOK_URL=https://hooks.example.com/purser
PURSER_RECONCILER_WEBHOOK_RETRIES=3
```

---

## Payload format

Purser posts `application/json` with the following fields:

```json
{
  "event": "approval_required",
  "event_type": "node_down",
  "node_id": "node-abc123",
  "deployment_id": "dep-xyz789",
  "timestamp": "2026-09-06T12:34:56Z",
  "purser_version": "0.3.0",
  "message": "Node node-abc123 went down; deployment dep-xyz789 requires manual approval to failover"
}
```

| Field | Type | Description |
|---|---|---|
| `event` | string | Always `"approval_required"` for the current trigger set |
| `event_type` | string | Reconciler event type; currently always `"node_down"` |
| `node_id` | string | ID of the node that became unreachable |
| `deployment_id` | string | ID of the deployment affected |
| `timestamp` | RFC 3339 | UTC time of the reconciler pass that raised the event |
| `purser_version` | string | Control-plane version string |
| `message` | string | Human-readable summary |

---

## Retry behaviour

Purser uses **exponential backoff** between delivery attempts:

| Attempt | Delay before attempt |
|---------|---------------------|
| 1 | Immediate |
| 2 | ~500 ms |
| 3 | ~1 s |
| N | ~2^(N-2) × 500 ms |

A `2xx` response code is treated as success. Any other status code or network
error causes a retry. After all attempts are exhausted, Purser logs a WARN and
moves on — the webhook is fire-and-forget and never blocks the reconciler.

Delivery runs in a goroutine separate from the control loop, so a slow or
unreachable webhook endpoint has zero impact on self-healing latency.

---

## Integration examples

### Slack

Create an [Incoming Webhook](https://api.slack.com/messaging/webhooks) and
forward the Purser payload through a lightweight proxy or a serverless function
that reformats it into Slack's `{"text": "..."}` envelope. A simple example
using [smee.io](https://smee.io) for local development:

```bash
PURSER_RECONCILER_WEBHOOK_URL=https://smee.io/your-channel-id
```

Production: use a proper Slack Webhook relay (e.g. a short AWS Lambda) so you
can enrich the message with buttons that call the Purser approval API.

### PagerDuty Events API v2

Send the Purser payload to a micro-service or proxy that maps it to a
PagerDuty `trigger` event:

```bash
PURSER_RECONCILER_WEBHOOK_URL=https://your-pd-relay.example.com/purser
```

The relay maps `deployment_id` to `dedup_key`, `message` to `summary`, and
returns `202` so Purser considers delivery successful.

### Direct HTTP endpoint

If your internal tooling accepts arbitrary JSON POST requests, point
`PURSER_RECONCILER_WEBHOOK_URL` directly at the endpoint:

```bash
PURSER_RECONCILER_WEBHOOK_URL=http://ops-runbook.internal:9000/api/alerts
```

Purser will POST the payload above on every `approval_required` event and retry
up to `PURSER_RECONCILER_WEBHOOK_RETRIES` times on failure.

---

## Security notes

- Purser does **not** sign the webhook payload. If your endpoint is
  internet-accessible, add a shared secret check in front of it (e.g. verify a
  custom header set by a proxy, or use mTLS).
- The `node_id` and `deployment_id` fields are internal identifiers; treat them
  as opaque strings.
- Use HTTPS in production to prevent payload interception.
