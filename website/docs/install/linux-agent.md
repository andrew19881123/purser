# Installing the Purser agent on Linux (Debian / Ubuntu)

This guide covers installing the `purser-agent` service on a Debian or Ubuntu host and enrolling it into a running Purser cluster.

## Prerequisites

- A running Purser control plane reachable from the target host.
- A join token. See [Generating a join token](#generating-a-join-token) below.
- `systemd` (standard on Ubuntu 20.04+ and Debian 11+).

---

## Generating a join token

A join token authorises one or more new agents to enrol into the cluster. Tokens are
time-limited and single-use in spirit — generate a fresh one for each enrollment session.

### Using the dashboard (recommended)

The **Add Node** page in the Purser dashboard lets you generate a token and copy a
ready-to-paste environment block without touching the command line.

**Workflow:**

1. Open the Purser dashboard in your browser and click **Add Node** in the left sidebar.
2. Choose a token lifetime (1 h / 8 h / 24 h / 7 d). The default — 24 hours — is suitable
   for most maintenance windows.
3. Click **Generate**. The page shows:
   - The raw join token (one-click copy).
   - A pre-filled environment block:
     ```
     PURSER_CONTROL_PLANE_ADDR=https://<your-control-plane>
     PURSER_JOIN_TOKEN=prsr_join_<...>
     PURSER_CLUSTER_ID=<cluster-id>
     ```
   - Expandable **Connection instructions** with the exact `apt` and `systemctl` commands.
4. Copy the environment block and the install commands. The token is shown **only once** —
   navigate away and it is gone from the UI (the token itself remains valid until it expires).

> **Screenshot description:** The "Add Node" page displays a TTL selector drop-down followed
> by a "Generate" button. After generation, a warning banner ("This token is shown only once.
> Copy it now.") appears above a copyable token row, the three-line env block, and a collapsible
> "Connection instructions" section containing the Debian/Ubuntu shell snippet.

### Using the API directly

```bash
curl -s -X POST https://<control-plane>/api/v1/join-token \
  -H 'Content-Type: application/json' \
  -d '{"ttl_seconds": 86400}' | jq .
```

Response:

```json
{
  "token": "prsr_join_...",
  "cluster_id": "...",
  "expires_at": "2026-09-06T12:00:00Z"
}
```

---

## Installing the agent package

Download the `.deb` package for your architecture from the [releases page](https://github.com/purser/purser/releases)
and install it with `apt`:

```bash
sudo apt install ./purser-agent_VERSION_amd64.deb
```

Replace `VERSION` with the release you downloaded (e.g. `0.2.0`).

---

## Configuring the agent

Populate `/etc/purser/agent.env` with the values from the join token step:

```bash
sudo tee /etc/purser/agent.env > /dev/null <<'EOF'
PURSER_CONTROL_PLANE_ADDR=https://<control-plane-host>
PURSER_JOIN_TOKEN=prsr_join_<token>
PURSER_CLUSTER_ID=<cluster-id>
EOF
```

---

## Starting and enabling the service

```bash
sudo systemctl enable --now purser-agent
```

After a few seconds the agent enrolls and the node appears in the **Fleet** view of the
dashboard with state `enrolled`, transitioning to `ready` once the initial health check passes.

---

## Verifying the enrollment

```bash
# Check the service is running
sudo systemctl status purser-agent

# Follow agent logs
sudo journalctl -fu purser-agent
```

In the dashboard, navigate to **Fleet** — the new node should be visible with its hardware
profile and a `ready` status pill.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `connection refused` in agent log | Wrong `PURSER_CONTROL_PLANE_ADDR` | Verify the address and port; check firewall rules. |
| `token expired` | Token TTL elapsed before enrollment | Generate a new token from the dashboard or API. |
| Node stuck at `enrolled` | Control plane cannot reach agent | Ensure the agent host is not behind a NAT that blocks inbound gRPC. |
| `certificate verify failed` | Self-signed TLS on the control plane | Pass `PURSER_INSECURE_TLS=1` (dev only) or install the CA cert system-wide. |
