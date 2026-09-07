# mTLS Certificate Renewal

## Certificate lifetime

Every Purser agent receives a mutual-TLS (mTLS) client certificate when it
enrolls with the control plane.  The certificate is issued by the cluster
CA and is valid for **90 days** by default (controlled by `PURSER_PKI_LEAF_TTL`
on the control-plane).  When the certificate expires the agent can no longer
authenticate with the control plane and is silently ejected — heartbeats stop
being recorded and the node transitions to `UNREACHABLE`.

The certificate is stored encrypted at rest in the agent's secret store
directory (`PURSER_SECRET_STORE_DIR`, default `~/.purser/secrets/`) under
the key `client_cert`.

---

## Architecture

```
Agent                              Control Plane
  │                                      │
  │  (daily check: days_left < 30?)      │
  │──POST /api/v1/enrollment/renew──────>│
  │           {"node_id": "node-abc"}    │
  │                                      │  Look up node in registry
  │                                      │  Check: days_left > 60? → 409
  │                                      │  Issue new leaf cert via CA
  │<────── {"certificate_pem": "...",    │
  │         "expires_at": "..."}  ───────│
  │                                      │
  │  Store new cert in secret store      │
  │  (atomic write via EncryptedFileSecretStore.put) │
```

---

## Auto-renewal (v0.5+)

Starting with v0.5, `purser-agent` runs an **automatic certificate renewal
loop** that runs every 24 hours (configurable via `PURSER_CERT_CHECK_INTERVAL_HOURS`)
and renews the certificate when fewer than **30 days** remain before expiry.

### How it works

1. Every `PURSER_CERT_CHECK_INTERVAL_HOURS` hours (default: 24), the agent
   reads its current `client_cert` from the secret store and parses the
   `notAfter` field.
2. If more than 30 days remain, nothing happens.
3. If fewer than 30 days remain, the agent sends:
   ```
   POST /api/v1/enrollment/renew
   Content-Type: application/json
   {"node_id": "<node-id>"}
   ```
4. The control plane checks the node exists and that the current cert is
   within the renewal window (< 60 days remaining).  If the cert still has
   > 60 days left it returns `409 cert_not_yet_expiring` to prevent
   premature churn.
5. On success the control plane issues a new 90-day certificate and returns
   it as PEM.
6. The agent writes the new certificate atomically to the secret store
   (replacing the old entry).
7. A structured log line is emitted:
   ```
   INFO cert renewed, new expiry: 2027-09-07T00:00:00Z
   ```

### Failure handling

If the renewal request fails (network error, 5xx, unreachable CP), the agent
logs a warning and retries at the next check interval.  It does **not** crash
or restart.  The existing expiry monitor continues to log `WARN` and `ERROR`
as the certificate approaches and reaches expiry:

| Condition | Log level | Message |
|---|---|---|
| More than 30 days remaining | `DEBUG` | `mTLS certificate valid — no renewal needed` |
| Fewer than 30 days remaining | `INFO` | `mTLS certificate expires soon — requesting renewal` |
| Renewal succeeded | `INFO` | `cert renewed, new expiry: <RFC3339>` |
| Renewal failed | `WARN` | `cert renewal check failed: <error>` |
| Certificate expired | `ERROR` | `mTLS certificate has EXPIRED — agent cannot communicate` |

---

## Configuration

| Environment variable | Default | Description |
|---|---|---|
| `PURSER_CERT_CHECK_INTERVAL_HOURS` | `24` | How often (hours) the renewal loop wakes to check expiry. Minimum 1. |
| `PURSER_PKI_LEAF_TTL` (CP) | `2160h` (90 days) | Validity window for newly issued leaf certificates. |

---

## Manual cert renewal via API

Operators can also trigger renewal manually:

```bash
curl -s -X POST https://<control-plane>:8443/api/v1/enrollment/renew \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer $ADMIN_TOKEN" \
     -d '{"node_id": "<node-id>"}' | jq .
```

### Response (200 OK)

```json
{
  "certificate_pem": "-----BEGIN CERTIFICATE-----\n...",
  "expires_at": "2027-09-07T00:00:00Z"
}
```

### Error responses

| Code | `error` field | Meaning |
|---|---|---|
| `400` | `bad_request` | `node_id` is missing or the request body is malformed |
| `404` | `not_found` | The node ID does not exist in the registry |
| `409` | `cert_not_yet_expiring` | Current cert has > 60 days remaining; renewal rejected |
| `501` | `no_fleet` | Fleet manager not configured on the control plane |

---

## Verifying renewal

Check the agent logs after enrollment:

```bash
# systemd
journalctl -u purser-agent | grep -E "cert renewal|cert renewed|expires"

# Docker
docker logs purser-agent | grep -E "cert renewal|cert renewed|expires"
```

Expected output after a successful renewal:
```
INFO cert renewal loop started check_interval_hours=24
INFO cert renewed, new expiry: 2027-09-07T00:00:00Z
```

---

## Troubleshooting

### Agent can't reach the control plane for renewal

If `PURSER_CONTROL_PLANE_ADDR` is set but the agent can't POST to
`/api/v1/enrollment/renew`, the renewal will fail with a warning.  Check:

1. Network connectivity: `curl -v $PURSER_CONTROL_PLANE_ADDR/api/v1/cluster/health`
2. TLS: ensure `PURSER_AGENT_CA_BUNDLE` points to the CA cert if the CP uses
   a private CA.
3. Proxy: set `PURSER_AGENT_HTTPS_PROXY` if traffic must go through a proxy.

The agent will keep retrying every `PURSER_CERT_CHECK_INTERVAL_HOURS` hours.
It will continue to work until the current certificate actually expires.

### Certificate expires before renewal succeeds

If the control plane is unreachable for the entire renewal window (30 days),
the agent will eventually log:

```
ERROR mTLS certificate has EXPIRED — agent cannot communicate with the control plane.
      Re-enrollment required: set PURSER_JOIN_TOKEN and restart the agent.
```

In this case, re-enroll the agent manually:

1. Obtain a new join token:
   ```bash
   curl -X POST https://<control-plane>:8443/api/v1/join-token \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -d '{"ttl_seconds": 3600}'
   ```
2. Set the token and restart:
   ```bash
   export PURSER_JOIN_TOKEN=<token>
   systemctl restart purser-agent
   ```

### Renewal rejected with 409

The control plane returns `409 cert_not_yet_expiring` when the current
certificate has more than 60 days remaining.  This is a safety guard to
prevent premature certificate churn.  The agent's renewal loop only triggers
at < 30 days, so this 409 should not be seen in normal operation — only if
you trigger renewal manually when the cert is fresh.

---

## Roadmap

| Version | Feature |
|---|---|
| v0.3 | Certificate expiry monitor — WARN before expiry, ERROR on expiry |
| v0.5 | **Automatic cert renewal** — `POST /api/v1/enrollment/renew`, daily check, < 30 days trigger |
| v0.6 | `purser_agent_cert_expiry_seconds` Prometheus gauge |
