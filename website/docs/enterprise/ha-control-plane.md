# HA Control Plane

Purser v0.3 introduces a **Raft-based high-availability (HA) control plane**.  Three
or more control-plane replicas use the [hashicorp/raft](https://github.com/hashicorp/raft)
library to elect a leader, replicate every write through the Raft log, and tolerate
the failure of any minority of nodes — delivering the same eventual-consistency
guarantee as a single-node deployment while removing the control plane as a single
point of failure.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         clients / agents                        │
└───────────────────────────────┬─────────────────────────────────┘
                                │ REST + gRPC
         ┌──────────────────────┼──────────────────────┐
         ▼                      ▼                      ▼
  ┌────────────┐        ┌────────────┐        ┌────────────┐
  │  CP node 1 │◄──────►│  CP node 2 │◄──────►│  CP node 3 │
  │  (leader)  │  Raft  │ (follower) │  Raft  │ (follower) │
  │  SQLite +  │  TCP   │  SQLite +  │  TCP   │  SQLite +  │
  │  BoltDB    │        │  BoltDB    │        │  BoltDB    │
  └────────────┘        └────────────┘        └────────────┘
```

Each node runs:

| Component | Role |
|---|---|
| `SQLiteRegistry` | Local read store; all writes go through the Raft log |
| `PurserFSM` | Applies committed log entries to the local SQLite DB |
| `raft.Raft` (TCP transport) | Consensus, leader election, log replication |
| BoltDB log store | Persistent Raft log |
| BoltDB stable store | Persistent term / voted-for metadata |
| File snapshot store | Periodic state snapshots (last 2 retained) |

---

## v0.3 MVP limitations

* **Snapshot restore is a no-op.** Full state restoration after a catastrophic
  disk failure requires restarting the node with a backup copy of the SQLite
  file in place. The Raft snapshot mechanism is scaffolded so the runtime
  never blocks, but the snapshot body is empty and Restore is a no-op.
* **Reads are local** (eventually consistent). Leader-lease reads are out of
  scope for v0.3.
* **Only the control-plane REST/gRPC API is HA.** The gateway and agents remain
  stateless and are unaffected.

---

## Enabling HA mode

HA mode is **opt-in**. A single unset environment variable keeps the existing
single-node behavior, so existing deployments require zero migration.

### Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `PURSER_RAFT_NODE_ID` | yes (to enable HA) | _(unset = standalone)_ | Unique, stable node identifier. Use the hostname or a UUID persisted between restarts. |
| `PURSER_RAFT_BIND_ADDR` | no | `:7000` | `host:port` on which the Raft TCP transport listens. Must be reachable by all other cluster members. |
| `PURSER_RAFT_DATA_DIR` | no | `raft-data` | Directory for BoltDB log/stable stores and file snapshots. Keep on a persistent volume. |
| `PURSER_RAFT_BOOTSTRAP` | no | `false` | Set to `true` **only on the very first start of the first node**. Bootstrapping a running cluster corrupts it. |

### Three-node example (docker-compose)

```yaml
services:
  cp1:
    image: ghcr.io/your-org/purser-controlplane:v0.3
    environment:
      PURSER_RAFT_NODE_ID: cp1
      PURSER_RAFT_BIND_ADDR: "cp1:7000"
      PURSER_RAFT_DATA_DIR: /data/raft
      PURSER_RAFT_BOOTSTRAP: "true"   # bootstrap flag only on cp1
    volumes:
      - cp1-data:/data

  cp2:
    image: ghcr.io/your-org/purser-controlplane:v0.3
    environment:
      PURSER_RAFT_NODE_ID: cp2
      PURSER_RAFT_BIND_ADDR: "cp2:7000"
      PURSER_RAFT_DATA_DIR: /data/raft
    volumes:
      - cp2-data:/data

  cp3:
    image: ghcr.io/your-org/purser-controlplane:v0.3
    environment:
      PURSER_RAFT_NODE_ID: cp3
      PURSER_RAFT_BIND_ADDR: "cp3:7000"
      PURSER_RAFT_DATA_DIR: /data/raft
    volumes:
      - cp3-data:/data

volumes:
  cp1-data:
  cp2-data:
  cp3-data:
```

!!! warning "Bootstrap flag"
    Remove `PURSER_RAFT_BOOTSTRAP: "true"` (or set it to `"false"`) before restarting
    `cp1` after it has joined an existing cluster. Bootstrapping an already-running
    cluster will corrupt its Raft log.

### Adding a node to a running cluster

Cluster membership changes (AddVoter / RemoveServer) use the underlying
`hashicorp/raft` API directly. A helper CLI command and the REST API surface
for cluster membership are planned for v0.4.

---

## Checking cluster status

```
GET /api/v1/cluster/status
```

This endpoint is **unauthenticated** so load-balancers and Kubernetes liveness
probes can determine which replica is the active leader.

**Standalone mode response:**

```json
{
  "mode": "standalone",
  "is_leader": true
}
```

**Raft mode response:**

```json
{
  "mode": "raft",
  "is_leader": true,
  "leader": "10.0.0.1:7000",
  "state": "Leader",
  "stats": {
    "applied_index": "42",
    "commit_index": "42",
    "fsm_pending": "0",
    "last_log_index": "42",
    "last_log_term": "3",
    "num_peers": "2",
    "state": "Leader",
    "term": "3"
  }
}
```

Key fields:

| Field | Description |
|---|---|
| `mode` | `"standalone"` or `"raft"` |
| `is_leader` | `true` when this node is the current Raft leader |
| `leader` | Network address of the current leader (empty during elections) |
| `state` | Raft FSM state: `Leader`, `Follower`, `Candidate`, or `Shutdown` |
| `stats` | Raw stats map from `hashicorp/raft` (term, commit index, peer count, …) |

---

## Kubernetes readiness probe

Configure the readiness probe to route traffic only to the current leader when
you need strongly-consistent writes:

```yaml
readinessProbe:
  httpGet:
    path: /api/v1/cluster/status
    port: 8080
  # A non-leader node returns is_leader: false but still HTTP 200.
  # Use a custom exec probe to filter for is_leader: true.
  exec:
    command:
      - sh
      - -c
      - "curl -sf http://localhost:8080/api/v1/cluster/status | grep -q '\"is_leader\":true'"
  initialDelaySeconds: 5
  periodSeconds: 5
```

For read traffic that tolerates eventual consistency, point all replicas to the
standard `liveness` path (`/api/v1/cluster/health`) instead.

---

## Operational notes

* **Odd quorum sizes only.** A three-node cluster tolerates one failure; a
  five-node cluster tolerates two. Even-sized clusters provide no additional
  fault tolerance and complicate split-brain handling — avoid them.
* **Raft data directory.** Use a fast local SSD or a low-latency network volume.
  High-latency storage widens the election window and slows writes.
* **Firewall.** Open the Raft TCP port (default `7000`) between all control-plane
  replicas. The port is configurable via `PURSER_RAFT_BIND_ADDR`.
* **Node IDs must be stable.** If a node's ID changes between restarts, Raft
  treats it as a new, unknown member. Use a persistent hostname or an ID stored
  on the same persistent volume as `PURSER_RAFT_DATA_DIR`.
