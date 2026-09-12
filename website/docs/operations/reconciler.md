# Reconciler

The Purser **reconciler** is a background loop in the control plane that continuously compares the desired cluster state (deployments, node assignments) against observed reality and takes corrective actions. It runs on a configurable interval and uses a hysteresis tracker to avoid flapping when a discrepancy is detected briefly and then self-corrects.

## What the reconciler does

On every tick the reconciler:

1. **Loads the desired state** — reads every active deployment from the registry.
2. **Surveys the observed state** — reads the current node pool and their running engines.
3. **Computes the delta** — identifies discrepancies between desired and observed (e.g. a node went down, a deployment became orphaned because its node disappeared).
4. **Applies the hysteresis filter** — each discrepancy must persist for at least `hysteresis_s` seconds before an action is taken. Transient blips (a node briefly unreachable) are ignored.
5. **Enforces the action cooldown** — after triggering a corrective action the reconciler waits `action_cooldown_s` seconds before acting again on the same event type, preventing rapid-fire changes.

## Reading the Reconciler status widget

The **Fleet** page in the operator dashboard contains a **Reconciler** card. It shows:

| Field | Meaning |
|---|---|
| **State** | `idle` — nothing tracked; `syncing` — corrections pending; `error` — an event has been tracked for more than 5 minutes |
| **Pending** | Total number of discrepancies currently in the hysteresis tracker |
| **Errors** | Number of event types whose oldest discrepancy exceeds the 5-minute error threshold |

When **Pending > 0** the card also shows an **Active events** table:

| Column | Meaning |
|---|---|
| Event type | The category of discrepancy (see below) |
| Tracked | How many individual instances of this discrepancy are being tracked |
| Age (s) | How long the oldest instance of this discrepancy has been in the tracker |

The card also has a collapsible **Configuration** panel that shows the active reconciler settings without needing shell access.

## Tracker event types

| Event type | Meaning |
|---|---|
| `node_down` | A node that hosts at least one deployment has not sent a heartbeat within `node_timeout_s` seconds |
| `orphan_deployment` | A deployment's assigned node has been removed or is permanently unreachable |

Additional event types may be added in future releases. Unrecognised types are safe to ignore — the reconciler handles them generically.

## State transitions

```
idle  ──(discrepancy appears)──▶  syncing  ──(age > 300 s)──▶  error
                                     │
                           (discrepancy clears)
                                     │
                                     ▼
                                   idle
```

A state of `error` does **not** mean the reconciler has stopped — it is still running and will continue trying. It means that a corrective action was either blocked (e.g. cooldown active) or ineffective, and the discrepancy has persisted long enough to warrant operator attention.

## Configuration reference

These values are set in the control-plane configuration file and reflected read-only in the status widget.

| Field | Default | Description |
|---|---|---|
| `interval_s` | 10 | How often the reconciler runs (seconds) |
| `node_timeout_s` | 45 | How long a node can be silent before being considered down |
| `hysteresis_s` | 30 | Minimum age a discrepancy must reach before the reconciler acts |
| `action_cooldown_s` | 60 | Minimum time between successive corrective actions per event type |

## Operational guidance

**Reconciler stuck in `syncing`:**
Check whether the pending events in the active events table are making progress (age should not grow unboundedly). If age keeps growing, check that the control plane can reach the affected nodes and that the fleet manager is healthy.

**Reconciler in `error` state:**
An event has been tracked for more than 5 minutes without resolving. Check the event type:
- `node_down` — the node may need manual intervention (reboot, network fix).
- `orphan_deployment` — the deployment may need to be re-scheduled or deleted.

**Pending count spikes but recovers quickly:**
This is normal during transient network events. The hysteresis filter (`hysteresis_s`) prevents unnecessary actions for brief blips. If spikes are frequent, consider increasing `hysteresis_s`.

## Checking overall stack health

The `purser-status` tool gives a quick view of every layer of the stack from a
single command and is useful when diagnosing reconciler issues:

```bash
make status
# or
./tools/purser-status.sh
```

The output shows fleet node states (READY / RUNNING / other), active deployments and
which nodes they span, and Control Plane reachability — the same signals the
reconciler acts on, surfaced without digging through logs.

For scripted health checks or CI integration:

```bash
./tools/purser-status.sh --json | python3 -c "
import sys, json
d = json.load(sys.stdin)
nodes = d.get('nodes', [])
ready = sum(1 for n in nodes if 'READY' in n.get('state','') or 'RUNNING' in n.get('state',''))
print('ready: {}/{}'.format(ready, len(nodes)))
"
```
