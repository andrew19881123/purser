# Linux Agent (.deb / .rpm)

The Purser Agent is a native host package that runs as a systemd service. It needs direct access to the node's GPUs/accelerators and supervises an inference engine worker — it does **not** run inside Kubernetes.

## Install from package

Download the package for your distribution from the [latest release](https://github.com/andrew19881123/purser/releases/latest):

=== "Debian / Ubuntu (amd64)"

    ```bash
    sudo apt install ./purser-agent_0.1.0_amd64.deb
    ```

=== "Debian / Ubuntu (arm64 — Graviton / Ampere)"

    ```bash
    sudo apt install ./purser-agent_0.1.0_arm64.deb
    ```

=== "RHEL / Fedora / openSUSE (amd64)"

    ```bash
    sudo yum install ./purser-agent-0.1.0-1.x86_64.rpm
    ```

=== "RHEL / Fedora / openSUSE (arm64)"

    ```bash
    sudo yum install ./purser-agent-0.1.0-1.aarch64.rpm
    ```

The package installs:
- `/usr/local/bin/purser-agent` — the agent binary
- `/etc/systemd/system/purser-agent.service` — the systemd unit
- `/etc/purser/agent.env` — the environment configuration file

## Configure

Edit the environment file `/etc/purser/agent.env`:

```bash
sudoedit /etc/purser/agent.env
```

Set at minimum:

```bash
# gRPC bind address for AgentService (control-plane -> agent traffic)
# SECURITY: bind on a TRUSTED-SUBNET interface, never a public one
PURSER_AGENT_BIND=0.0.0.0:50151

# Port the inference engine serves on
PURSER_INFERENCE_PORT=8000

# Control-plane RegistrationService gRPC address
PURSER_CONTROL_PLANE_ADDR=http://cp.internal:9443

# Must match the control plane's cluster id
PURSER_CLUSTER_ID=default

# One-time join token (mint with: curl -X POST http://<cp>:8080/api/v1/join-token)
PURSER_JOIN_TOKEN=psk_replace-me

# Directory for encrypted secret files (created with mode 0700 on first use)
PURSER_SECRET_STORE_DIR=/var/lib/purser/secrets
```

!!! warning "Security: protect the env file"
    The env file contains the join token. Install it with restricted permissions:
    ```bash
    sudo chmod 640 /etc/purser/agent.env
    sudo chown root:purser /etc/purser/agent.env
    ```

For nodes behind NAT or with multiple interfaces, set the advertised addresses explicitly:

```bash
# Explicit address the Control Plane uses to reach this node's AgentService
PURSER_AGENT_ADVERTISED_ADDR=192.168.1.10:50151

# Explicit address the Gateway uses for inference requests
PURSER_INFERENCE_ADVERTISED_ADDR=192.168.1.10:8000
```

If these are not set, the agent derives them automatically from `PURSER_AGENT_BIND` by detecting the primary local non-loopback IPv4 address.

## Enable and start

```bash
sudo systemctl enable --now purser-agent
```

Check status and logs:

```bash
systemctl status purser-agent
journalctl -u purser-agent -f
```

## Key environment variables

See the complete reference at [Environment Variables Reference](../configuration/env-vars.md#agent). Summary of primary knobs:

| Variable | Default | Description |
|---|---|---|
| `PURSER_AGENT_BIND` | `0.0.0.0:50151` | gRPC bind address for AgentService |
| `PURSER_INFERENCE_PORT` | `8000` | Port the local inference engine listens on |
| `PURSER_CONTROL_PLANE_ADDR` | (none) | Control Plane gRPC endpoint for enrollment and heartbeat |
| `PURSER_CLUSTER_ID` | `default` | Cluster to join |
| `PURSER_JOIN_TOKEN` | (none) | One-time join token |
| `PURSER_AGENT_ADVERTISED_ADDR` | (derived) | Explicit agent address as seen by the Control Plane |
| `PURSER_INFERENCE_ADVERTISED_ADDR` | (derived) | Explicit inference address as seen by the Gateway |
| `PURSER_NODE_ID` | (assigned at Join) | Stable node identity |
| `PURSER_HEALTH_INTERVAL_SECS` | `5` | Heartbeat cadence in seconds |
| `PURSER_ENGINE_BACKEND` | `mock` | Engine backend to use (`mock` or `llamacpp`) |
| `PURSER_SEEDS` | (none) | Comma-separated extra discovery seed peers |
| `PURSER_SECRET_STORE_DIR` | `$HOME/.purser/secrets` | Directory for encrypted-at-rest secret files |
| `PURSER_SECRET_KEY` | (auto-generated) | 32-byte AES-256 key (hex or base64). See [Secret persistence](#secret-persistence). |
| `RUST_LOG` | `info` | Log level |

## GPU nodes

The agent unit intentionally does not lock down `/dev`. If the inference engine cannot see the accelerator, uncomment the `DeviceAllow=` / `SupplementaryGroups=` block inside `/etc/systemd/system/purser-agent.service` — grant only the specific devices needed, don't disable the sandbox wholesale.

## Managing the service

```bash
# Stop
sudo systemctl stop purser-agent

# Restart (e.g. after editing agent.env)
sudo systemctl restart purser-agent

# Disable autostart
sudo systemctl disable purser-agent

# View recent logs
journalctl -u purser-agent --since "1 hour ago"
```

## Fleet-scale deployment

### Internal apt / yum repository

Mirror the `.deb` / `.rpm` packages into an internal repository and push the config with your configuration management tool:

```bash
# Example: reprepro for apt
reprepro -b /var/www/apt/purser includedeb bookworm purser-agent_0.1.0_amd64.deb
```

### Ansible example

```yaml
---
- name: Install and configure Purser Agent
  hosts: fleet_nodes
  become: true

  vars:
    purser_control_plane_addr: "http://cp.internal:9443"
    purser_cluster_id: "default"
    # join_token is fetched from the control plane and stored securely

  tasks:
    - name: Install purser-agent package
      apt:
        deb: "https://releases.example.com/purser/purser-agent_0.1.0_amd64.deb"
      when: ansible_os_family == "Debian"

    - name: Write agent.env
      template:
        src: agent.env.j2
        dest: /etc/purser/agent.env
        owner: root
        group: purser
        mode: "0640"
      notify: restart purser-agent

    - name: Enable and start purser-agent
      systemd:
        name: purser-agent
        enabled: true
        state: started
        daemon_reload: true

  handlers:
    - name: restart purser-agent
      systemd:
        name: purser-agent
        state: restarted
```

`agent.env.j2` template:

```
PURSER_AGENT_BIND=0.0.0.0:50151
PURSER_INFERENCE_PORT=8000
PURSER_CONTROL_PLANE_ADDR={{ purser_control_plane_addr }}
PURSER_CLUSTER_ID={{ purser_cluster_id }}
PURSER_JOIN_TOKEN={{ purser_join_token }}
PURSER_SECRET_STORE_DIR=/var/lib/purser/secrets
```

## Install from binary tarball

If you prefer not to use the native packages, grab the prebuilt binary tarball from the same release and verify the SHA-256 checksum.

=== "linux/amd64"

    ```bash
    TAG=v0.3.0
    curl -LO https://github.com/andrew19881123/purser/releases/download/${TAG}/purser-agent-linux-amd64-${TAG}.tar.gz
    curl -LO https://github.com/andrew19881123/purser/releases/download/${TAG}/SHA256SUMS
    sha256sum -c SHA256SUMS --ignore-missing

    tar -xzf purser-agent-linux-amd64-${TAG}.tar.gz
    sudo install -m 0755 purser-agent /usr/local/bin/purser-agent
    ```

=== "linux/arm64 (Graviton / Ampere)"

    ```bash
    TAG=v0.3.0
    curl -LO https://github.com/andrew19881123/purser/releases/download/${TAG}/purser-agent-linux-arm64-${TAG}.tar.gz
    curl -LO https://github.com/andrew19881123/purser/releases/download/${TAG}/SHA256SUMS
    sha256sum -c SHA256SUMS --ignore-missing

    tar -xzf purser-agent-linux-arm64-${TAG}.tar.gz
    sudo install -m 0755 purser-agent /usr/local/bin/purser-agent
    ```

Then install the unit file and env template from [`packaging/systemd/`](https://github.com/andrew19881123/purser/tree/main/packaging/systemd) and [`packaging/env/agent.env.example`](https://github.com/andrew19881123/purser/tree/main/packaging/env) manually.

## Secret persistence

The agent stores sensitive material — join tokens, mTLS certificates issued
during enrollment — **encrypted at rest** using AES-256-GCM.

### How it works

Each secret is written to `{PURSER_SECRET_STORE_DIR}/{name}.enc` as:

```
[ 12-byte nonce ][ ciphertext + 16-byte GCM authentication tag ]
```

A fresh nonce is generated from the OS CSPRNG on every write, so encrypting
the same plaintext twice always produces a different ciphertext.  The GCM
tag means any bit-flip in the file — whether accidental or malicious — is
detected and rejected on read.

### Key management

The 32-byte AES-256 key is sourced in this order:

1. **`PURSER_SECRET_KEY` env var** (hex- or base64-encoded 32 bytes).
   Set this when you provision the key from an external secrets manager or
   Vault:
   ```bash
   export PURSER_SECRET_KEY=$(vault kv get -field=key secret/purser/agent-key)
   ```

2. **Auto-generated key file** (`{PURSER_SECRET_STORE_DIR}/.secret_key`).
   If `PURSER_SECRET_KEY` is not set the agent reads the key from this file,
   or generates a cryptographically random key and saves it there (mode 0600)
   on first start.  Because the key survives restarts, so do the encrypted
   secrets — enrollment certificates are not lost when the daemon is restarted
   or upgraded.

#### Generating a key manually

```bash
# Hex (64 chars)
openssl rand -hex 32

# Base64 (44 chars)
openssl rand -base64 32
```

Pass the output as `PURSER_SECRET_KEY`.  Keep it in a secrets manager (Vault,
AWS Secrets Manager, etc.) rather than a plain-text file.

### Protecting the key file

The key file and the secrets directory are created with restrictive Unix
permissions (0700 for the directory, 0600 for all files).  If you run the
agent as a dedicated user (`purser` in the systemd example above), only that
user can read the key.

For higher assurance, supply the key via `PURSER_SECRET_KEY` from a secrets
manager and **do not** write it to disk.  Remove `{PURSER_SECRET_STORE_DIR}/.secret_key`
if it exists; the agent will use the env-supplied key instead.

### Rotating the key

1. Export all current secrets (or allow a fresh enrollment after rotation).
2. Set `PURSER_SECRET_KEY` to the new 32-byte key (hex or base64).
3. Delete the existing `.enc` files in the store directory.
4. Restart the agent — it will re-enroll and re-write secrets under the new key.
