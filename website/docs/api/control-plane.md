# Control-Plane API

The control-plane management API is served under `/api/v1/`. It is a standard
HTTP/1.1 REST surface (no gRPC, no WebSocket) and is intentionally minimal —
the inference data path is fully separate.

## Live Metrics — `GET /api/v1/metrics`

Returns a **Server-Sent Events (SSE)** stream of real-time hardware metrics for
every node in the cluster. The control plane pushes one frame every **2 seconds**;
clients keep the connection open and process frames as they arrive.

### Request

```
GET /api/v1/metrics HTTP/1.1
Accept: text/event-stream
```

No query parameters. The connection is long-lived until the client closes it.

### SSE Frame Format

Each frame carries a single JSON object on the `data:` line:

```
data: {"at":"2026-09-05T12:00:00Z","aggregate_decode_tok_s":86.5,"nodes":[...]}

```

(Two newlines end each frame, per the SSE spec.)

### Top-Level Fields

| Field | Type | Description |
|---|---|---|
| `at` | string (RFC 3339) | Emission timestamp (UTC). |
| `aggregate_decode_tok_s` | number | Sum of `decode_tok_s` across all nodes currently producing tokens. |
| `nodes` | array | Per-node hardware metrics (see below). Always present; empty when no nodes are enrolled. |

### Per-Node Entry (`nodes[*]`)

| Field | Type | Description |
|---|---|---|
| `node_id` | string | Unique node identifier assigned at enrollment. |
| `state` | string | Node lifecycle state, e.g. `NODE_STATE_RUNNING`, `NODE_STATE_READY`. |
| `metrics` | object | Hardware metrics sub-object (see below). |

### Metrics Sub-Object (`nodes[*].metrics`)

All fields default to `0` when the node has not yet sent a heartbeat or when
the agent has not yet loaded a model. **Zero values are honest** — the control
plane never omits a known node, it emits zeros for nodes that have not yet
reported.

| Field | Type | Unit | Description |
|---|---|---|---|
| `prefill_tok_s` | number | tokens/s | Prefill (prompt-processing) throughput for the current request. |
| `decode_tok_s` | number | tokens/s | Decode (generation) throughput, the primary throughput signal. |
| `ram_used_gb` | number | GB | Host RAM consumed by the agent and loaded model weights. |
| `vram_used_gb` | number | GB | GPU VRAM consumed (0 for CPU-only nodes). |
| `queue_depth` | integer | requests | Number of inference requests currently queued on this node. |
| `accepted_tokens_ratio` | number | 0–1 | Speculative-decoding acceptance ratio; 0 when speculative decoding is not active. |

### Example Frame

```json
{
  "at": "2026-09-05T12:00:00Z",
  "aggregate_decode_tok_s": 90.5,
  "nodes": [
    {
      "node_id": "node-gpu-01",
      "state": "NODE_STATE_RUNNING",
      "metrics": {
        "prefill_tok_s": 680,
        "decode_tok_s": 46,
        "ram_used_gb": 22,
        "vram_used_gb": 74,
        "queue_depth": 1,
        "accepted_tokens_ratio": 0.71
      }
    },
    {
      "node_id": "node-gpu-02",
      "state": "NODE_STATE_READY",
      "metrics": {
        "prefill_tok_s": 0,
        "decode_tok_s": 0,
        "ram_used_gb": 0,
        "vram_used_gb": 0,
        "queue_depth": 0,
        "accepted_tokens_ratio": 0
      }
    }
  ]
}
```

`node-gpu-02` appears with zeros because it is enrolled and ready but has not
yet received a deployment.

### Update Cadence

Frames are emitted every **2 seconds** (configurable at server startup via
`Config.MetricsInterval`). Metrics values reflect the most recent heartbeat
received from each agent; agents heartbeat on a similar cadence so values are
typically no more than a few seconds stale.

### Data Source

Metrics come from the `EngineMetrics` field of agent heartbeats
(`purser.v1.RegistrationService/Heartbeat` gRPC stream). The control plane
caches the latest sample per node in an in-memory store (`fleet.LiveMetrics`)
and merges it with the registry node list on each SSE tick. Nodes that have
enrolled but not yet started heartbeating are included with zero metrics.

### Client Example (JavaScript)

```javascript
const source = new EventSource('/api/v1/metrics');
source.onmessage = (ev) => {
  const snap = JSON.parse(ev.data);
  console.log('aggregate tok/s:', snap.aggregate_decode_tok_s);
  for (const node of snap.nodes) {
    console.log(node.node_id, node.metrics.decode_tok_s);
  }
};
```
