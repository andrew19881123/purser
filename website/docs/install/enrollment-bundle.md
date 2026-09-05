# Enrollment Bundle

The **enrollment bundle** is a pre-filled environment file that contains everything a new node needs to join your Purser cluster. Instead of manually copying three environment variables to each new node, the dashboard generates a ready-to-use file that you `scp` into place and reload.

## What it contains

The enrollment bundle is a plain-text file with three environment variables:

| Variable | Example | Description |
|---|---|---|
| `PURSER_CONTROL_PLANE_ADDR` | `http://10.0.0.1:9443` | gRPC address of the Control Plane `RegistrationService` |
| `PURSER_JOIN_TOKEN` | `psk_…` | One-time join token (expires after 1 hour by default) |
| `PURSER_CLUSTER_ID` | `default` | Logical cluster this node will join |

The bundle file is re-generated each time you call the endpoint, minting a fresh join token. The previous token is not revoked — unused tokens expire naturally.

## Downloading the bundle

### From the dashboard

1. Open the Purser Dashboard and navigate to **Get started → Add Node**.
2. Click **Download enrollment bundle**.
3. The browser downloads `purser-enrollment.env`.

### Via API

```bash
curl -s http://<control-plane>:8080/api/v1/enrollment-bundle \
  -H "Authorization: Bearer <admin-key>" \
  -o purser-enrollment.env
```

!!! note "Admin key required"
    Downloading the enrollment bundle requires an API key with the `admin` role. The bundle contains a one-time join token — treat it with the same care as a password.

## Installing on the new node

Copy the bundle to the new node and restart the agent:

```bash
# From your workstation
scp purser-enrollment.env root@new-node:/etc/purser/agent.env
ssh root@new-node systemctl restart purser-agent
```

The agent reads the file at startup, calls `RegistrationService::Join`, receives an mTLS certificate, and transitions to `READY`. The node appears in the fleet within a few seconds.

## Verifying enrollment

```bash
# On your workstation — poll until the node appears
curl -s http://<control-plane>:8080/api/v1/nodes | jq '.nodes[] | select(.state == "NODE_STATE_READY")'
```

## Security notes

- The join token in the bundle is **single-use**: once the agent uses it to enroll, subsequent Join attempts with the same token fail.
- Tokens expire after 1 hour by default. If enrollment takes longer, mint a fresh bundle.
- The bundle file contains a bearer secret. Restrict its permissions (`chmod 600`) and use encrypted transfers (`scp`/`sftp`, never plain HTTP).
- After enrollment the agent stores its mTLS certificate in the encrypted secret store (`PURSER_SECRET_STORE_DIR`). The join token is no longer needed.

## Generating a token manually

For scripted provisioning (Ansible, cloud-init) you can mint a token via the API without downloading the full bundle:

```bash
curl -sS -X POST http://<control-plane>:8080/api/v1/join-token \
  -H "Authorization: Bearer <admin-key>" \
  -H "Content-Type: application/json" \
  -d '{"ttl_seconds": 3600}'
```

Response:

```json
{
  "token":      "psk_…",
  "expires_at": "2026-09-05T01:00:00Z",
  "cluster_id": "default"
}
```

Then set the three variables on the target node by any mechanism (cloud-init user data, Ansible `copy` module, Kubernetes ConfigMap + Secret, etc.).
