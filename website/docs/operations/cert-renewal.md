# mTLS Certificate Renewal

## Certificate lifetime

Every Purser agent receives a mutual-TLS (mTLS) client certificate when it
enrolls with the control plane.  The certificate is issued by the cluster
CA and is valid for **24 hours** by default.  When the certificate expires
the agent can no longer authenticate with the control plane and is silently
ejected — heartbeats stop being recorded and the node transitions to
`UNREACHABLE`.

The certificate is stored encrypted at rest in the agent's secret store
directory (`PURSER_SECRET_STORE_DIR`, default `~/.purser/secrets/`) under
the key `client_cert`.

---

## Expiry monitor

Starting with v0.3, `purser-agent` runs a background certificate expiry
monitor that wakes every **6 hours** and checks the `notAfter` field of the
stored `client_cert`.

| Condition | Log level | Message |
|---|---|---|
| Certificate is valid, more than 24 h remaining | `DEBUG` | `mTLS certificate valid — expires in N days` |
| Certificate expires within 24 h | `WARN` | `mTLS certificate expires soon — … days_left=N` |
| Certificate has already expired | `ERROR` | `mTLS certificate has EXPIRED — agent cannot communicate` |

Search your log stream for `mTLS certificate expires` or `mTLS certificate
has EXPIRED` to build an alerting rule.

---

## Re-enrollment (manual, v0.3)

Automatic re-enrollment is deferred to v0.4.  Until then, re-enroll manually
when you receive the `WARN` or `ERROR` log above:

1. Obtain a new join token from the control plane operator:

   ```bash
   # On the control-plane host
   purser-cp token create --ttl 1h
   ```

2. Set the token and restart the agent:

   ```bash
   export PURSER_JOIN_TOKEN=<new-token>
   systemctl restart purser-agent
   # or, if running manually:
   purser-agent
   ```

   On restart the agent calls `RegistrationService::Join` with the new token,
   receives a fresh certificate, and stores it in the secret store.

3. Verify enrollment succeeded:

   ```bash
   journalctl -u purser-agent -n 50 | grep -E "enrolled|mTLS"
   ```

   You should see `purser-agent starting AgentService` followed by an
   `encrypted file secret store initialised` line and then the agent reaching
   `NodeState::READY`.

---

## Automated alerting

The `purser_agent_cert_expiry_seconds` Prometheus gauge (v0.4+) will expose
the Unix timestamp of the certificate expiry.  Until then, alert on the log
pattern `mTLS certificate expires soon` with a `days_left` label less than 2.

Example Loki / Promtail alerting rule (v0.3):

```yaml
groups:
  - name: purser-agent-cert
    rules:
      - alert: PurserAgentCertExpiringSoon
        expr: |
          count_over_time(
            {job="purser-agent"} |= "mTLS certificate expires soon"
            [6h]
          ) > 0
        labels:
          severity: warning
        annotations:
          summary: "Purser agent mTLS certificate expiring within 24 h"
          description: >
            Re-enroll the agent with a new join token before the certificate
            expires or the node will be silently ejected.

      - alert: PurserAgentCertExpired
        expr: |
          count_over_time(
            {job="purser-agent"} |= "mTLS certificate has EXPIRED"
            [15m]
          ) > 0
        labels:
          severity: critical
        annotations:
          summary: "Purser agent mTLS certificate has expired"
          description: >
            The agent is no longer able to authenticate with the control plane.
            Re-enroll immediately with a new PURSER_JOIN_TOKEN.
```

---

## Roadmap

| Version | Feature |
|---|---|
| v0.3 | Certificate expiry monitor — WARN 24 h before expiry, ERROR on expiry |
| v0.4 | Automatic re-enrollment via gRPC `Join` with a long-lived join token |
| v0.5 | Certificate rotation without restart (live credential refresh) |
