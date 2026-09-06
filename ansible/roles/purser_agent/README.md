# purser_agent — Ansible Role

Installs, configures, and enrolls the **Purser Fleet Agent** (`purser-agent`) on
Linux nodes running systemd (Debian/Ubuntu, RHEL/Rocky/AlmaLinux).

The role handles the full lifecycle:

1. Install via Cloudsmith-hosted apt/yum repository **or** binary tarball (air-gap friendly).
2. Create `/etc/purser/agent.env` with restricted permissions (`0640 root:purser`).
3. Enable and start the `purser-agent` systemd service.
4. Verify the AgentService gRPC port is listening before the play ends.

## Requirements

- Ansible >= 2.14
- Target nodes running Linux with systemd
- A reachable Purser control plane
- A valid join token (obtain from the control plane dashboard or via `POST /api/v1/join-token`)

## Role Variables

All variables have safe defaults. The two **required** variables have no default and
**must** be supplied:

| Variable | Default | Description |
|---|---|---|
| `purser_control_plane_addr` | `""` | **Required.** Control plane gRPC address, e.g. `http://cp.internal:8080` |
| `purser_join_token` | `""` | **Required.** One-time join token |
| `purser_cluster_id` | `"default"` | Cluster to join |
| `purser_agent_bind` | `"0.0.0.0:50151"` | AgentService gRPC bind address |
| `purser_inference_port` | `8000` | Inference engine HTTP port |
| `purser_engine_backend` | `"mock"` | Engine backend: `mock` or `llamacpp` |
| `purser_llamacpp_bin` | `""` | Path to llama.cpp binary (when `engine_backend=llamacpp`) |
| `purser_agent_advertised_addr` | `""` | Explicit address for control plane to reach agent |
| `purser_inference_advertised_addr` | `""` | Explicit address for gateway inference requests |
| `purser_secret_store_dir` | `"/var/lib/purser/secrets"` | Encrypted secret store directory |
| `purser_secret_key` | `""` | 32-byte AES-256 key (hex/base64); auto-generated when empty |
| `purser_health_interval_secs` | `5` | Heartbeat cadence in seconds |
| `purser_seeds` | `""` | Extra discovery seed peers (comma-separated `host:port`) |
| `purser_rust_log` | `"info"` | Log level |
| `purser_version` | `"0.3.0"` | Package or tarball version to install |
| `purser_install_method` | `"package"` | `"package"` or `"binary"` |
| `purser_apt_repo_url` | `"https://dl.cloudsmith.io/public/andrew19881123/purser"` | Cloudsmith repo base URL |
| `purser_binary_url` | `""` | Override tarball URL for air-gap installs |
| `purser_service_enabled` | `true` | Whether to enable the service at boot |
| `purser_service_state` | `"started"` | Desired service state after play |

## Example Playbook

```yaml
- hosts: gpu_nodes
  become: true
  roles:
    - purser_agent
  vars:
    purser_control_plane_addr: "http://cp.internal:8080"
    purser_join_token: "psk_your-token-here"
    purser_cluster_id: "production"
```

## Install Methods

### Package (default)

Configures the Cloudsmith-hosted apt or yum repository and installs
`purser-agent={{ purser_version }}`. Requires internet access to
`dl.cloudsmith.io` or a mirrored internal repository.

### Binary (air-gap)

Downloads the release tarball directly from GitHub Releases (or from
`purser_binary_url` when set), extracts the binary to `/usr/local/bin/`, and
installs the bundled systemd unit file. Use this method for air-gapped
environments by setting `purser_binary_url` to an internal mirror URL.

```yaml
purser_install_method: "binary"
purser_binary_url: "http://artifacts.internal/purser/purser-agent-linux-amd64-v0.3.0.tar.gz"
```

## Security Notes

- The env file `/etc/purser/agent.env` is installed as `0640 root:purser` and contains
  the join token. Never commit it or expose it in logs.
- Bind `purser_agent_bind` to a **trusted-subnet interface only** — the inference engine
  worker is not sandboxed and must never face the public internet.
- On GPU nodes, if the inference engine cannot access accelerators, manually edit
  `/etc/systemd/system/purser-agent.service` to uncomment the `DeviceAllow=` /
  `SupplementaryGroups=` block — see the comments inside the unit file.

## License

Apache-2.0
