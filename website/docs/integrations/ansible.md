# Ansible Role — Fleet Enrollment

The `purser_agent` Ansible role automates installation and enrollment of the
Purser Fleet Agent across any number of Linux GPU nodes. It handles repository
configuration, binary installation, env-file templating, and service management
in a single idempotent role.

The role lives in [`ansible/roles/purser_agent/`](https://github.com/andrew19881123/purser/tree/main/ansible/roles/purser_agent)
in the Purser repository.

## Requirements

| Requirement | Notes |
|---|---|
| Ansible >= 2.14 | Install with `pip install ansible` |
| Target OS | Debian/Ubuntu 20.04+, RHEL/Rocky/AlmaLinux 8+ |
| systemd | Required on all target nodes |
| Control plane reachable | From the Ansible controller (for `enroll_nodes.yml`) |

## Role variables

All variables have defaults except the two marked **Required**.

### Connection and identity

| Variable | Default | Description |
|---|---|---|
| `purser_control_plane_addr` | `""` | **Required.** Control plane gRPC address, e.g. `http://cp.internal:8080` |
| `purser_join_token` | `""` | **Required.** One-time join token |
| `purser_cluster_id` | `"default"` | Logical cluster this node joins |

### Agent configuration

| Variable | Default | Description |
|---|---|---|
| `purser_agent_bind` | `"0.0.0.0:50151"` | AgentService gRPC bind address. Bind on a trusted-subnet interface only. |
| `purser_inference_port` | `8000` | Inference engine HTTP port |
| `purser_engine_backend` | `"mock"` | Engine backend: `mock` (no GPU) or `llamacpp` |
| `purser_llamacpp_bin` | `""` | Path to `llama-server` binary (when `engine_backend=llamacpp`) |
| `purser_agent_advertised_addr` | `""` | Explicit address control plane uses to reach this node (useful behind NAT) |
| `purser_inference_advertised_addr` | `""` | Explicit address gateway uses for inference (multi-homed hosts) |

### Secret store

| Variable | Default | Description |
|---|---|---|
| `purser_secret_store_dir` | `"/var/lib/purser/secrets"` | Directory for AES-256-GCM encrypted secret files |
| `purser_secret_key` | `""` | 32-byte key (hex or base64). Auto-generated when empty. |

### Discovery and observability

| Variable | Default | Description |
|---|---|---|
| `purser_health_interval_secs` | `5` | Heartbeat cadence in seconds |
| `purser_seeds` | `""` | Comma-separated extra seed peers beyond LAN mDNS |
| `purser_rust_log` | `"info"` | Log level (RUST_LOG) |

### Installation

| Variable | Default | Description |
|---|---|---|
| `purser_version` | `"0.3.0"` | Package or tarball version to install |
| `purser_install_method` | `"package"` | `"package"` (apt/yum) or `"binary"` (tarball) |
| `purser_apt_repo_url` | `"https://dl.cloudsmith.io/public/andrew19881123/purser"` | Cloudsmith repository base URL |
| `purser_binary_url` | `""` | Override tarball URL for air-gap installs |

### Service control

| Variable | Default | Description |
|---|---|---|
| `purser_service_enabled` | `true` | Enable the service at boot |
| `purser_service_state` | `"started"` | Desired service state: `started`, `stopped`, `restarted` |

## Quick start

### 1. Clone the Purser repository

```bash
git clone https://github.com/andrew19881123/purser.git
cd purser/ansible
```

### 2. Create an inventory

```ini
# ansible/inventory/hosts.ini
[gpu_nodes]
node1.internal  ansible_host=192.168.1.10
node2.internal  ansible_host=192.168.1.11
node3.internal  ansible_host=192.168.1.12

[gpu_nodes:vars]
ansible_user=ubuntu
```

### 3. Enroll in one command

The `enroll_nodes.yml` playbook mints a join token from the control plane,
then installs and enrolls every node in one run:

```bash
export PURSER_CP_ADDR=http://cp.internal:8080
export PURSER_API_TOKEN=<admin-api-token>   # omit if not required

ansible-playbook -i inventory/ playbooks/enroll_nodes.yml
```

### 4. Or install with a pre-existing token

```bash
export PURSER_CP_ADDR=http://cp.internal:8080
export PURSER_JOIN_TOKEN=psk_your-token-here

ansible-playbook -i inventory/ playbooks/install_purser_agents.yml
```

## Inventory layout for a large fleet

Use `group_vars` to share configuration across node groups:

```
ansible/
  inventory/
    hosts.ini
    group_vars/
      gpu_nodes.yml     # shared vars for all GPU nodes
      dc1.yml           # per-datacenter overrides
      dc2.yml
```

Example `group_vars/gpu_nodes.yml`:

```yaml
purser_cluster_id: production
purser_engine_backend: llamacpp
purser_llamacpp_bin: /opt/llama/llama-server
purser_rust_log: warn
```

## Install methods

### Package (default)

Configures the Cloudsmith-hosted apt or yum repository and installs the
package. Internet access to `dl.cloudsmith.io` is required (or a local mirror):

```yaml
purser_install_method: "package"
purser_version: "0.3.0"
```

### Binary (air-gap)

Downloads the prebuilt tarball from GitHub Releases (or a custom URL) and
installs the binary to `/usr/local/bin`. Use this when the nodes cannot
reach the Cloudsmith repository:

```yaml
purser_install_method: "binary"
purser_version: "0.3.0"
# Optional: mirror URL for fully air-gapped environments
purser_binary_url: "http://artifacts.internal/purser/purser-agent-linux-amd64-v0.3.0.tar.gz"
```

Mirror the release tarball from GitHub to your internal server:

```bash
TAG=v0.3.0
ARCH=amd64  # or arm64 for Graviton / Ampere
curl -LO https://github.com/andrew19881123/purser/releases/download/${TAG}/purser-agent-linux-${ARCH}-${TAG}.tar.gz
# Upload to http://artifacts.internal/purser/
```

## Secrets management

The join token is written to `/etc/purser/agent.env` as `0640 root:purser`.
For production, supply the token from a secrets manager:

=== "Ansible Vault"

    ```yaml
    # group_vars/gpu_nodes.yml (encrypted with ansible-vault)
    purser_join_token: "{{ vault_purser_join_token }}"
    ```

    ```bash
    ansible-playbook -i inventory/ playbooks/install_purser_agents.yml \
      --vault-password-file ~/.vault_pass
    ```

=== "HashiCorp Vault"

    ```yaml
    purser_join_token: "{{ lookup('hashi_vault', 'secret=secret/purser/join-token:token') }}"
    ```

=== "AWS Secrets Manager"

    ```yaml
    purser_join_token: "{{ lookup('amazon.aws.aws_secret', 'purser/join-token', region='us-east-1') }}"
    ```

## Example playbook

```yaml
---
- name: Deploy Purser Fleet Agent to all GPU nodes
  hosts: gpu_nodes
  become: true

  roles:
    - purser_agent

  vars:
    purser_control_plane_addr: "http://cp.internal:8080"
    purser_join_token: "{{ lookup('env', 'PURSER_JOIN_TOKEN') }}"
    purser_cluster_id: production
    purser_engine_backend: llamacpp
    purser_llamacpp_bin: /opt/llama/llama-server
    purser_agent_bind: "0.0.0.0:50151"
    purser_rust_log: warn
```

## What the role does

The role runs these steps on each target node (all idempotent):

1. **Assert** — fail fast if `purser_control_plane_addr` or `purser_join_token` is empty.
2. **System user/group** — create `purser` system account (binary install only; package handles this).
3. **Install** — add repository and install package, or download and extract tarball.
4. **Configure** — write `/etc/purser/agent.env` from the `purser-agent.env.j2` template (permissions `0640 root:purser`). Changes trigger a service restart.
5. **Service** — enable and start `purser-agent` via the `systemd` module.
6. **Verify** — wait up to 30 s for port 50151 to be listening.

## GPU nodes

After enrollment, confirm the inference engine can access GPUs:

```bash
systemctl status purser-agent
journalctl -u purser-agent -f
```

If the engine logs errors about missing device nodes, add a systemd override:

```bash
sudo systemctl edit purser-agent
```

```ini
[Service]
DeviceAllow=/dev/nvidia0 rw
DeviceAllow=/dev/nvidiactl rw
DeviceAllow=/dev/nvidia-uvm rw
SupplementaryGroups=video render
```

Then restart: `sudo systemctl restart purser-agent`.

See the comments in `packaging/systemd/purser-agent.service` for the full
device list and rationale.

## Related

- [Linux Agent installation](../install/linux-agent.md)
- [Environment variables reference](../configuration/env-vars.md#agent)
- [Helm chart for the control plane](../install/kubernetes.md)
