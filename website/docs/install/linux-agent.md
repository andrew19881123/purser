# Installing the Purser agent on Linux

The Purser agent (`purser-agent`) is a lightweight daemon that runs on each
fleet node and communicates with the control plane over gRPC.

## Prerequisites

- Linux x86-64 or aarch64
- Kernel 4.4 or newer
- `systemd` (recommended for service management)

## Installation

### From the release tarball

```bash
curl -LO https://github.com/purser-ai/purser/releases/latest/download/purser-agent-linux-amd64.tar.gz
tar -xzf purser-agent-linux-amd64.tar.gz
sudo install -m 755 purser-agent /usr/local/bin/
```

### From source

```bash
cargo build --release -p purser-agent
sudo install -m 755 target/release/purser-agent /usr/local/bin/
```

## Basic configuration

The agent is configured entirely through environment variables. The minimum
required for a managed node:

```bash
export PURSER_CONTROL_PLANE_ADDR=https://cp.your-cluster.internal:50150
export PURSER_JOIN_TOKEN=<token-issued-by-control-plane>
```

See [Environment variables](../configuration/env-vars.md) for the full
reference.

## Running as a systemd service

```ini
# /etc/systemd/system/purser-agent.service
[Unit]
Description=Purser fleet agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/purser-agent
Restart=on-failure
RestartSec=5s
EnvironmentFile=/etc/purser/agent.env
# Run as a dedicated user for privilege isolation.
User=purser
Group=purser
# The secret store directory must be owned by the service user.
StateDirectory=purser

[Install]
WantedBy=multi-user.target
```

Create the environment file:

```bash
sudo mkdir -p /etc/purser
sudo tee /etc/purser/agent.env <<'EOF'
PURSER_CONTROL_PLANE_ADDR=https://cp.your-cluster.internal:50150
PURSER_JOIN_TOKEN=<token>
PURSER_SECRET_STORE_DIR=/var/lib/purser/secrets
EOF
sudo chmod 600 /etc/purser/agent.env
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now purser-agent
sudo journalctl -u purser-agent -f
```

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
