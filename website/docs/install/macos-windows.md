# macOS / Windows Agent

The Purser Agent can run as a launchd daemon on macOS or a Windows service. Both are supported via scripts in the [`packaging/`](https://github.com/andrew19881123/purser/tree/main/packaging) directory.

!!! note "Less tested than Linux"
    The Linux systemd path (`.deb` / `.rpm` packages) is the primary and most tested deployment method. The macOS launchd and Windows service paths ship the necessary service definitions and scripts, but real-world GPU testing on these platforms is still in progress.

---

## macOS (launchd)

### Prerequisites

- macOS with `launchctl`
- The `purser-agent` binary from the [latest release](https://github.com/andrew19881123/purser/releases/latest)

#### Apple Silicon (M1 / M2 / M3 — darwin/arm64)

Starting with v0.3.0, a native `darwin/arm64` binary is published for Apple Silicon Macs. Download it directly from the release page:

```bash
TAG=v0.3.0
curl -LO https://github.com/andrew19881123/purser/releases/download/${TAG}/control-plane-darwin-arm64-${TAG}.tar.gz
curl -LO https://github.com/andrew19881123/purser/releases/download/${TAG}/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing

tar -xzf control-plane-darwin-arm64-${TAG}.tar.gz
```

!!! note "darwin/arm64 purser-agent"
    The `purser-agent` darwin/arm64 binary is published on a best-effort basis. If the release does not include it, the amd64 binary runs under Rosetta 2. The Linux agent (`.deb`/`.rpm`) is the primary and most tested path for fleet nodes.

### Install

1. Install the binary:

    ```bash
    sudo install -m 0755 dist/bin/purser-agent /usr/local/bin/purser-agent
    sudo mkdir -p /usr/local/var/log/purser
    ```

2. Download the launchd plist from [`packaging/launchd/dev.purser.agent.plist`](https://github.com/andrew19881123/purser/tree/main/packaging/launchd) and edit the environment keys:

    ```xml
    <key>EnvironmentVariables</key>
    <dict>
        <key>PURSER_AGENT_BIND</key>      <string>0.0.0.0:50151</string>
        <key>PURSER_INFERENCE_PORT</key>  <string>8000</string>
        <key>PURSER_CONTROL_PLANE_ADDR</key> <string>http://cp.internal:9443</string>
        <key>PURSER_CLUSTER_ID</key>      <string>default</string>
        <key>PURSER_JOIN_TOKEN</key>      <string>psk_replace-me</string>
    </dict>
    ```

    !!! note "No EnvironmentFile on macOS"
        launchd has no `EnvironmentFile` equivalent. Edit the environment variables directly in the plist before installing.

3. Install and load as a system daemon:

    ```bash
    sudo cp dev.purser.agent.plist /Library/LaunchDaemons/
    sudo chown root:wheel /Library/LaunchDaemons/dev.purser.agent.plist
    sudo chmod 644 /Library/LaunchDaemons/dev.purser.agent.plist
    sudo launchctl load -w /Library/LaunchDaemons/dev.purser.agent.plist
    ```

    `KeepAlive` is set to `true` — launchd restarts the daemon on crash. Stdout and stderr go to `/usr/local/var/log/purser/agent.out.log` and `agent.err.log`.

### Manage

```bash
# Stop
sudo launchctl stop dev.purser.agent

# Unload (disable autostart)
sudo launchctl unload -w /Library/LaunchDaemons/dev.purser.agent.plist
```

---

## Windows (PowerShell service)

### Prerequisites

- Windows 10/Server 2019 or later
- PowerShell running as Administrator
- The `purser-agent.exe` binary from the release

!!! note "MSI installer is planned"
    A signed MSI installer (WiX) bundling the binaries, service definitions and an uninstall entry is planned. The PowerShell scripts below are the interim scriptable path.

### Install

1. Copy the binary to an install directory:

    ```powershell
    New-Item -ItemType Directory -Force "C:\Program Files\Purser\bin"
    Copy-Item purser-agent.exe "C:\Program Files\Purser\bin\"
    ```

2. From an **elevated (Administrator)** PowerShell, run the install script from [`packaging/windows/`](https://github.com/andrew19881123/purser/tree/main/packaging/windows):

    ```powershell
    cd packaging\windows
    .\install-service.ps1 -BinDir 'C:\Program Files\Purser\bin' -Component all
    ```

    This registers the `PurserAgent` service via `New-Service`, configures automatic crash-restart, and writes the service environment to the registry.

3. Review and set the environment variables in the registry (or edit the defaults in `install-service.ps1` before step 2):

    The script writes environment under:
    `HKLM:\SYSTEM\CurrentControlSet\Services\PurserAgent`

    Key variables to set:

    | Variable | Example value |
    |---|---|
    | `PURSER_CONTROL_PLANE_ADDR` | `http://cp.internal:9443` |
    | `PURSER_JOIN_TOKEN` | `psk_replace-me` |
    | `PURSER_CLUSTER_ID` | `default` |
    | `PURSER_AGENT_BIND` | `0.0.0.0:50151` |
    | `PURSER_INFERENCE_PORT` | `8000` |

4. Start the service:

    ```powershell
    Start-Service PurserAgent
    ```

    Check status:

    ```powershell
    Get-Service PurserAgent
    Get-EventLog -LogName Application -Source PurserAgent -Newest 20
    ```

### Uninstall

```powershell
cd packaging\windows
.\uninstall-service.ps1 -Component all
```

State defaults to `C:\ProgramData\Purser` (see `PURSER_DB` / `PURSER_PKI_DIR` in `install-service.ps1`).

---

## Environment variables reference

The same environment variables apply on all platforms. See the full reference at [Environment Variables Reference](../configuration/env-vars.md#agent).

Primary knobs for the agent:

| Variable | Default | Description |
|---|---|---|
| `PURSER_AGENT_BIND` | `0.0.0.0:50151` | gRPC bind address |
| `PURSER_INFERENCE_PORT` | `8000` | Inference engine port |
| `PURSER_CONTROL_PLANE_ADDR` | (required) | Control Plane gRPC address |
| `PURSER_JOIN_TOKEN` | (required) | One-time enrollment token |
| `PURSER_CLUSTER_ID` | `default` | Must match the cluster |
| `PURSER_AGENT_ADVERTISED_ADDR` | (derived) | Override if behind NAT |
| `PURSER_INFERENCE_ADVERTISED_ADDR` | (derived) | Override if behind NAT |
| `RUST_LOG` | `info` | Log level |
