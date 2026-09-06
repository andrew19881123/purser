# Purser — Ansible Automation

Ansible role and playbooks for fleet-scale enrollment of the **Purser Fleet Agent**
(`purser-agent`) onto Linux GPU nodes.

## Directory structure

```
ansible/
  ansible.cfg                        # roles_path and inventory defaults
  roles/
    purser_agent/                    # The installation role
      defaults/main.yml              # All role variables with defaults
      tasks/main.yml                 # Install → configure → enable → verify
      handlers/main.yml              # Restart / daemon-reload handlers
      templates/purser-agent.env.j2  # /etc/purser/agent.env template
      files/purser-agent.service     # systemd unit (binary install method)
      meta/main.yml                  # Galaxy metadata
      README.md                      # Role-specific documentation
  playbooks/
    install_purser_agents.yml        # Install with a pre-existing join token
    enroll_nodes.yml                 # Mint a token then install in one run
```

## Prerequisites

| Requirement | Notes |
|---|---|
| Ansible >= 2.14 | `pip install ansible` |
| Target OS | Debian/Ubuntu 20.04+, RHEL/Rocky/AlmaLinux 8+ |
| systemd | Required on all target nodes |
| Purser control plane reachable | From the Ansible controller (for `enroll_nodes.yml`) |
| SSH access to nodes | Passwordless sudo or `-K` flag |

## Quick start

### 1. Create an inventory

```ini
# ansible/inventory/hosts.ini
[gpu_nodes]
node1.internal  ansible_host=192.168.1.10
node2.internal  ansible_host=192.168.1.11
node3.internal  ansible_host=192.168.1.12

[gpu_nodes:vars]
ansible_user=ubuntu
```

For larger fleets, use `group_vars/gpu_nodes.yml` to set shared variables:

```yaml
# ansible/inventory/group_vars/gpu_nodes.yml
purser_cluster_id: production
purser_engine_backend: llamacpp
purser_llamacpp_bin: /opt/llama/llama-server
```

### 2. Run the enrollment playbook

The `enroll_nodes.yml` playbook mints a join token from the control plane and
installs the agent in a single run:

```bash
cd ansible/

export PURSER_CP_ADDR=http://cp.internal:8080
export PURSER_API_TOKEN=<admin-api-token>     # omit if auth is not required
export PURSER_CLUSTER_ID=production           # optional, default: "default"

ansible-playbook -i inventory/ playbooks/enroll_nodes.yml
```

### 3. Or use a pre-existing token

If you have already minted a join token:

```bash
cd ansible/

export PURSER_CP_ADDR=http://cp.internal:8080
export PURSER_JOIN_TOKEN=psk_your-token-here

ansible-playbook -i inventory/ playbooks/install_purser_agents.yml
```

## GPU node groups

On GPU nodes the agent supervises an inference engine that needs direct device
access. After enrolling, verify the engine can see the GPU:

```bash
# On the node
systemctl status purser-agent
journalctl -u purser-agent -f
```

If the engine logs errors about missing devices, edit the systemd unit on the
node to enable the `DeviceAllow=` block for the specific GPU devices:

```bash
sudo systemctl edit purser-agent
```

Add the override:

```ini
[Service]
DeviceAllow=/dev/nvidia0 rw
DeviceAllow=/dev/nvidiactl rw
DeviceAllow=/dev/nvidia-uvm rw
DeviceAllow=/dev/nvidia-uvm-tools rw
SupplementaryGroups=video render
```

Then restart: `sudo systemctl restart purser-agent`.

## Air-gap / offline installation

Use `purser_install_method: "binary"` and mirror the release tarball internally:

```bash
# Mirror a specific release to an internal server
RELEASE=v0.3.0
curl -LO https://github.com/andrew19881123/purser/releases/download/${RELEASE}/purser-agent-linux-amd64-${RELEASE}.tar.gz
# Upload to http://artifacts.internal/purser/
```

Then in your group_vars or playbook extra-vars:

```yaml
purser_install_method: "binary"
purser_binary_url: "http://artifacts.internal/purser/purser-agent-linux-amd64-v0.3.0.tar.gz"
purser_version: "0.3.0"
```

```bash
ansible-playbook -i inventory/ playbooks/install_purser_agents.yml \
  -e purser_install_method=binary \
  -e purser_binary_url=http://artifacts.internal/purser/purser-agent-linux-amd64-v0.3.0.tar.gz
```

## Role variables

See `roles/purser_agent/defaults/main.yml` for the full list with inline
documentation, or the role [README](roles/purser_agent/README.md).

Key required variables:

| Variable | Description |
|---|---|
| `purser_control_plane_addr` | Control plane gRPC address (e.g. `http://cp.internal:8080`) |
| `purser_join_token` | One-time join token |

## Secrets management

The join token is written to `/etc/purser/agent.env` with permissions `0640
root:purser`. For production, supply the token from a secrets manager rather
than an environment variable:

```yaml
# Using Ansible Vault
purser_join_token: "{{ vault_purser_join_token }}"
```

```bash
ansible-playbook -i inventory/ playbooks/install_purser_agents.yml \
  --vault-password-file ~/.vault_pass
```

For HashiCorp Vault integration use the `hashi_vault` lookup plugin:

```yaml
purser_join_token: "{{ lookup('hashi_vault', 'secret=secret/purser/join-token:token') }}"
```

## Further reading

- [Ansible role documentation](roles/purser_agent/README.md)
- [Linux agent install guide](../website/docs/install/linux-agent.md)
- [Ansible integration guide](../website/docs/integrations/ansible.md)
- [Purser packaging README](../packaging/README.md)
