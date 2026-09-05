# Installing the Purser Agent on Linux

The Purser agent is a lightweight daemon that enrolls a node into the cluster,
streams heartbeats, and exposes an OpenAI-compatible inference endpoint for
locally-deployed models. It runs under `systemd` on any modern Linux
distribution.

## Prerequisites

- Linux x86-64 or ARM64 (kernel 5.4+)
- `systemd` 240+
- Network access to the Purser control plane

## Manual installation

### 1 — Download the agent binary

```bash
curl -fsSL https://releases.purser.io/agent/latest/purser-agent-linux-amd64 \
  -o /usr/local/bin/purser-agent
chmod +x /usr/local/bin/purser-agent
```

### 2 — Create the runtime directory

```bash
mkdir -p /etc/purser
```

### 3 — Configure the agent

The agent reads its configuration from environment variables. You can supply
them manually (see [Manual token enrollment](#manual-token-enrollment) below)
or use the one-file bundle from the dashboard (see
[Auto-enrollment via bundle](#auto-enrollment-via-bundle)).

### 4 — Install and start the systemd service

```bash
cat > /etc/systemd/system/purser-agent.service <<'EOF'
[Unit]
Description=Purser fleet agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/purser/agent.env
ExecStart=/usr/local/bin/purser-agent
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now purser-agent
```

---

## Auto-enrollment via bundle

The enrollment bundle is a single `.env` file pre-configured with the control
plane address, cluster ID, and a freshly generated single-use join token. It is
the fastest way to enroll nodes — no manual copy-and-paste required.

### Step 1 — Download the bundle from the dashboard

1. Open the Purser dashboard and navigate to **Get started** → **Node
   enrollment** (`/#/join-token`).
2. Choose a **Bundle lifetime** (how long the token inside remains valid).
3. Click **Download Enrollment Bundle**.

The browser downloads a file named `purser-enrollment.env`.

Alternatively, download it directly with `curl` (replace the URL and TTL as
appropriate):

```bash
curl -fsSO http://<control-plane-host>:8080/api/v1/enrollment-bundle?ttl_seconds=86400 \
  -o purser-enrollment.env
```

### Step 2 — Copy the bundle to the node

```bash
scp purser-enrollment.env root@<node-host>:/etc/purser/agent.env
chmod 600 /etc/purser/agent.env
```

The file looks like this:

```env
# Purser Agent Enrollment Bundle
# Generated: 2026-09-05T12:00:00Z
# Expires:   2026-09-06T12:00:00Z
# Copy this file to /etc/purser/agent.env on each node you want to enroll.

PURSER_CONTROL_PLANE_ADDR=http://10.0.0.1:8080
PURSER_CLUSTER_ID=default
PURSER_JOIN_TOKEN=<single-use-token>
```

### Step 3 — Start (or restart) the agent

If the agent service is not yet installed, follow steps 4 above first. If it is
already running with an old token, restart it so it picks up the new bundle:

```bash
systemctl restart purser-agent
```

The agent connects to the control plane, presents the join token, and receives
a client certificate over mTLS. It then transitions to `READY` and begins
streaming heartbeats. The node should appear in the **Fleet** view within a few
seconds.

> **Token is single-use.** Each bundle contains a unique token that is consumed
> on the first successful enrollment. Distribute a separate bundle to each
> batch of nodes, or rotate the token between batches, to prevent replay.

---

## Manual token enrollment

If you prefer to configure the agent by hand, set the following variables in
`/etc/purser/agent.env`:

```env
PURSER_CONTROL_PLANE_ADDR=http://<control-plane-host>:8080
PURSER_CLUSTER_ID=default
PURSER_JOIN_TOKEN=<token-from-dashboard>
```

Generate a token from the dashboard (**Get started** → **Node enrollment**)
or via the API:

```bash
curl -s -X POST http://<control-plane-host>:8080/api/v1/join-token \
  -H 'Content-Type: application/json' \
  -d '{"ttl_seconds": 3600}' | jq -r .token
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Node stays in `PROVISIONING` | Token expired or already used | Download a new bundle or rotate the token |
| `enrollment failed` in the agent log | Wrong `PURSER_CONTROL_PLANE_ADDR` | Set `PURSER_PUBLIC_ADDR` on the control plane to its externally-reachable address |
| Node disappears from Fleet after restart | `PURSER_NODE_ID` not persisted | Set `PURSER_NODE_ID=<assigned-id>` in `agent.env` after the first enrollment |

View live agent logs:

```bash
journalctl -fu purser-agent
```
