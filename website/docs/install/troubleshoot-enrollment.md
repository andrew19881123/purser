# Troubleshooting Agent Enrollment

This page covers every known failure mode when enrolling Purser agents with the
Control Plane, with causes and fixes in one place.

---

## Quick diagnostic

Before debugging an agent, confirm the Control Plane itself is healthy:

```bash
# 1. Check the compose stack (or your native CP process)
docker compose ps

# 2. Hit the health endpoint — must return 200
curl -s http://localhost:8080/api/v1/cluster/health

# 3. Check CP logs for startup errors
docker compose logs control-plane --tail=50
```

If the health endpoint returns anything other than `200 ok`, fix the Control
Plane first — agent enrollment errors are secondary to a broken CP.

---

## Problem: Agent shows DEGRADED immediately after enrollment

**Symptom in agent logs:**

```
self-diagnosis flagged issues: ["low disk: X.X GiB free (< 5.0 GiB)"] implied_state=NODE_STATE_DEGRADED
```

**Cause:** The agent runs a self-check at startup. If the free disk on the
agent's working partition is below 5 GiB (the default threshold), the agent
transitions to `NODE_STATE_DEGRADED` instead of `READY`.

**Fix:**

Lower the threshold for developer machines:

```bash
PURSER_DISK_FREE_WARN_GB=1.0 ./bin/purser-agent
```

If the Rust build cache is filling `/tmp`, reclaim space:

```bash
rm -rf /tmp/purser-shared-target/debug/incremental
```

---

## Problem: Agent won't enroll — "transport error" / "connection refused" on port 9443

### Cause A — Control Plane not running or not reachable

**Check:**

```bash
curl http://localhost:8080/api/v1/cluster/health
```

**Fix:** Verify the CP is up. Verify that `PURSER_CONTROL_PLANE_ADDR` matches
the actual address the CP is bound to.

### Cause B — Port 9443 not exposed in the compose stack

**Check:** Does `docker compose ps` show `9443/tcp` without a host binding (i.e.
no `0.0.0.0:9443->9443/tcp`)?

**Fix:** Ensure `9443:9443` is listed under `ports:` for the control-plane
service in `docker-compose.yml` (added in v0.6). Alternatively, use host
network mode for the CP container.

### Cause C — Wrong `PURSER_CONTROL_PLANE_ADDR` format

The address must be plain HTTP on port 9443:

```
PURSER_CONTROL_PLANE_ADDR=http://host:9443   # correct
```

Common mistakes:

| Wrong | Problem |
|---|---|
| `https://host:9443` | Tries TLS on a plain gRPC listener |
| `http://host:8080` | Port 8080 is REST/HTTP only — not gRPC |
| `host:9443` | Missing scheme — client library may reject it |

---

## Problem: "authentication handshake failed: tls: first record does not look like a TLS handshake"

**Cause:** The Control Plane has initialized its PKI (it always does when
`pki-state/` exists on disk) and expects mTLS when connecting back to the
agent. Native agents built without explicit TLS configuration serve plain gRPC,
so the handshake fails.

This happens in any **mixed Docker/native** setup: the Docker CP initializes
PKI at startup; native agents on the host have no certs.

**Fix on the CP side:**

Set `PURSER_AGENT_GRPC_INSECURE=true` in the CP's environment before starting.
This tells the CP orchestrator to use plain gRPC when calling back to agents.

In `docker-compose.yml`:

```yaml
control-plane:
  environment:
    PURSER_AGENT_GRPC_INSECURE: "true"   # dev only
```

For a native CP:

```bash
PURSER_AGENT_GRPC_INSECURE=true make dev
```

!!! warning "Never use `PURSER_AGENT_GRPC_INSECURE=true` in production"
    This disables authentication between the Control Plane and agents.
    Use it only on your local dev machine.

---

## Problem: Two agents on the same machine conflict ("address already in use")

**Cause:** Both agents default to the same ports.

**Fix:** Override the conflicting ports for the second agent:

```bash
# Agent 1 — defaults are fine
PURSER_AGENT_BIND=0.0.0.0:50151

# Agent 2 — must use different ports for all three listeners
PURSER_AGENT_BIND=0.0.0.0:50161
PURSER_SWIM_BIND_ADDR=0.0.0.0:7947   # SWIM gossip (default: 7946)
PURSER_AGENT_METRICS_PORT=9092        # Prometheus metrics (default: 9091)
```

Full example:

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/join-token \
  -H 'Content-Type: application/json' \
  -d '{"ttl_seconds":3600}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

PURSER_AGENT_BIND=0.0.0.0:50161 \
PURSER_SWIM_BIND_ADDR=0.0.0.0:7947 \
PURSER_AGENT_METRICS_PORT=9092 \
PURSER_CONTROL_PLANE_ADDR=http://localhost:9443 \
PURSER_JOIN_TOKEN="$TOKEN" \
./bin/purser-agent
```

---

## Problem: Agent process dies when the terminal closes

**Cause:** The agent is a foreground process. Closing the terminal sends
`SIGHUP`, which terminates it.

**Fix options:**

```bash
# Option A: tmux (recommended for interactive dev)
tmux new-session -d -s agent1 "PURSER_JOIN_TOKEN=... ./bin/purser-agent"

# Attach to watch logs
tmux attach -t agent1
```

```bash
# Option B: nohup + disown
nohup PURSER_JOIN_TOKEN=... ./bin/purser-agent > agent.log 2>&1 &
disown
```

For persistent dev, use a **systemd user service** — see
[Local Dev Setup → Keeping agents alive](local-dev.md#keeping-agents-alive-after-the-shell-exits).

---

## Problem: Join token expired

**Symptom in agent logs:**

```
enrollment failed: token expired
```

**Fix:** Mint a new token (tokens are single-use and expire after `ttl_seconds`):

```bash
curl -X POST http://localhost:8080/api/v1/join-token \
  -H 'Authorization: Bearer demo-key-12345' \
  -H 'Content-Type: application/json' \
  -d '{"ttl_seconds":86400}'
```

**Prevention:** Use `ttl_seconds: 86400` (24 h) or longer when minting tokens
for local dev. Tokens for automated fleet enrollment should use shorter TTLs.

---

## Problem: Deployment stays PENDING forever

A deployment goes to `PENDING` when no node is in `READY` state. The planner
cannot schedule it.

**Check node states:**

```bash
curl -s http://localhost:8080/api/v1/nodes \
  -H 'Authorization: Bearer demo-key-12345' \
  | python3 -c 'import sys,json; [print(n["id"], n["state"]) for n in json.load(sys.stdin)["nodes"]]'
```

**Common causes:**

| Cause | Fix |
|---|---|
| Node is `DEGRADED` (disk) | Set `PURSER_DISK_FREE_WARN_GB=1.0` and re-enroll |
| Stale dead nodes blocking the planner | Delete stale nodes (see below) |
| No agents enrolled at all | Enroll at least one agent |

**Delete stale nodes:**

```bash
# List nodes and note the id of stale/dead ones
curl -s http://localhost:8080/api/v1/nodes -H 'Authorization: Bearer demo-key-12345'

# Delete by id
curl -X DELETE http://localhost:8080/api/v1/nodes/<id> \
  -H 'Authorization: Bearer demo-key-12345'
```

---

## Problem: "no nodes available after applying include/exclude constraints"

**Cause:** All nodes in the registry are `DECOMMISSIONED`. This happens when
you restart the Control Plane after previously decommissioning nodes — the
SQLite (or Postgres) database persists node state across restarts.

**Fix:**

Delete the stale DECOMMISSIONED nodes via the API (see above), then re-enroll
your agents.

If this is a dev machine and you want a clean slate, delete the SQLite database
and restart the CP:

```bash
rm -f /data/purser-registry.db   # or wherever PURSER_DB points
make dev
```

!!! warning "Deletes all state"
    Removing the database also removes all API keys, models, deployments, and
    audit log entries. Only do this on a dev machine.

---

## See also

- [Local Dev Setup (No Docker)](local-dev.md) — full native setup walkthrough
- [Linux Agent (.deb/.rpm)](linux-agent.md) — fleet installation
- [Environment Variables Reference](../configuration/env-vars.md) — all knobs
- [PKI Operations](../operations/pki-operations.md) — mTLS certificate management
