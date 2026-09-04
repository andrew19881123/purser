# Purser packaging (v0.1.0)

Service definitions and environment templates for running the three Purser
daemons as managed services. Build the release binaries first with:

```bash
scripts/build-release.sh    # stages dist/bin + dist/packaging
```

| Component     | Binary          | Language | Key ports (default)                 |
|---------------|-----------------|----------|-------------------------------------|
| Control plane | `control-plane` | Go       | REST `:8080`, gRPC `:9443`          |
| Agent         | `purser-agent`  | Rust     | AgentService `:50151`, inference `:8000` |
| API gateway   | `purser-gateway`| Rust     | OpenAI-compatible HTTP (you choose) |

```
packaging/
├── systemd/     Linux unit files
├── launchd/     macOS daemon (agent)
├── windows/     Windows service install/uninstall scripts
└── env/         environment templates (*.env.example)
```

> **Security.** Purser assumes a **trusted LAN**. Bind the agent
> (`PURSER_AGENT_BIND`) only on a trusted-subnet interface — the inference
> engine worker is **not** sandboxed and must never face the public internet.
> The `.env` files hold join tokens, API keys and (optionally) a license key:
> install them `0640`, owned `root:purser`, never commit them.

---

## Linux (systemd)

1. **Create the service user** (system account, no login, no home):

   ```bash
   sudo useradd --system --no-create-home --shell /usr/sbin/nologin purser
   ```

2. **Install the binaries** to `/usr/local/bin` (paths hard-coded in the units):

   ```bash
   sudo install -m 0755 dist/bin/control-plane   /usr/local/bin/control-plane
   sudo install -m 0755 dist/bin/purser-agent     /usr/local/bin/purser-agent
   sudo install -m 0755 dist/bin/purser-gateway   /usr/local/bin/purser-gateway
   ```

3. **Install the environment files** to `/etc/purser` and edit the secrets
   (join token, API keys, gateway token, license):

   ```bash
   sudo mkdir -p /etc/purser
   sudo install -m 0640 -o root -g purser env/control-plane.env.example /etc/purser/control-plane.env
   sudo install -m 0640 -o root -g purser env/agent.env.example         /etc/purser/agent.env
   sudo install -m 0640 -o root -g purser env/gateway.env.example       /etc/purser/gateway.env
   sudoedit /etc/purser/agent.env      # set PURSER_JOIN_TOKEN, etc.
   ```

   Runtime state lives under `/var/lib/purser`, which systemd creates and owns
   automatically via `StateDirectory=purser` (mode 0700, owner `purser`). The
   control-plane env template already points `PURSER_DB` and `PURSER_PKI_DIR`
   there — the only writable path under `ProtectSystem=strict`.

4. **Install the unit files and start the services:**

   ```bash
   sudo install -m 0644 systemd/purser-control-plane.service /etc/systemd/system/
   sudo install -m 0644 systemd/purser-agent.service         /etc/systemd/system/
   sudo install -m 0644 systemd/purser-gateway.service       /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now purser-control-plane purser-gateway
   # on each node:
   sudo systemctl enable --now purser-agent
   ```

   Check status / logs with `systemctl status purser-agent` and
   `journalctl -u purser-agent -f`.

   **GPU nodes:** the agent unit intentionally does not lock down `/dev`. If the
   engine cannot see the accelerator, uncomment the `DeviceAllow=` /
   `SupplementaryGroups=` block documented inside
   `systemd/purser-agent.service` — grant only the specific devices needed,
   don't disable the sandbox wholesale.

---

## macOS (launchd) — agent

1. Install the binary and create the log directory:

   ```bash
   sudo install -m 0755 dist/bin/purser-agent /usr/local/bin/purser-agent
   sudo mkdir -p /usr/local/var/log/purser
   ```

2. Edit the environment (join token, control-plane address, bind address)
   directly in `launchd/dev.purser.agent.plist` — launchd has no
   `EnvironmentFile` equivalent.

3. Install and load as a system daemon:

   ```bash
   sudo cp launchd/dev.purser.agent.plist /Library/LaunchDaemons/
   sudo chown root:wheel /Library/LaunchDaemons/dev.purser.agent.plist
   sudo chmod 644        /Library/LaunchDaemons/dev.purser.agent.plist
   sudo launchctl load -w /Library/LaunchDaemons/dev.purser.agent.plist
   ```

   `KeepAlive` restarts the daemon on crash; stdout/stderr go to
   `/usr/local/var/log/purser/agent.{out,err}.log`. Unload with
   `sudo launchctl unload -w /Library/LaunchDaemons/dev.purser.agent.plist`.

   The control plane and gateway can be run the same way by adapting this
   plist; only the agent template ships in v0.1.0.

---

## Windows (PowerShell scripts)

> A signed **MSI installer (WiX)** — bundling the binaries, service definitions
> and an uninstall entry — is a planned follow-up. These scripts are the
> interim, scriptable path.

1. Copy the release binaries (`purser-agent.exe`, `control-plane.exe`) into an
   install directory, e.g. `C:\Program Files\Purser\bin`.

2. From an **elevated (Administrator)** PowerShell:

   ```powershell
   cd packaging\windows
   .\install-service.ps1 -BinDir 'C:\Program Files\Purser\bin' -Component all
   ```

   This registers `PurserAgent` and `PurserControlPlane` (via `New-Service`),
   configures automatic crash-restart (`sc.exe failure`, ~2s delay), and writes
   each service's environment as a machine-scoped `Environment` value. There is
   no `EnvironmentFile` on Windows — edit the defaults at the top of
   `install-service.ps1` (or the registry key it writes) **before** starting.

3. Review the environment (set `PURSER_JOIN_TOKEN` and any secrets), then:

   ```powershell
   Start-Service PurserControlPlane
   Start-Service PurserAgent
   ```

4. Uninstall:

   ```powershell
   .\uninstall-service.ps1 -Component all
   ```

State defaults to `C:\ProgramData\Purser` (see `PURSER_DB` / `PURSER_PKI_DIR`
in `install-service.ps1`).

---

## Environment reference

Full, commented variable lists live in `env/*.env.example`. Summary of the
primary knobs:

- **control-plane** — `PURSER_ADDR` (`:8080`), `PURSER_GRPC_ADDR` (`:9443`),
  `PURSER_DB`, `PURSER_PKI_DIR`, `PURSER_CLUSTER_ID`, and optionally
  `PURSER_GATEWAY_ADDR` / `PURSER_GATEWAY_TOKEN`, `PURSER_AGENT_PORT`,
  `PURSER_LICENSE_KEY` (enterprise).
- **agent** — `PURSER_AGENT_BIND` (`0.0.0.0:50151`), `PURSER_INFERENCE_PORT`
  (`8000`), `PURSER_CONTROL_PLANE_ADDR`, `PURSER_CLUSTER_ID`,
  `PURSER_JOIN_TOKEN`, and optionally `PURSER_AGENT_ADVERTISED_ADDR` /
  `PURSER_INFERENCE_ADVERTISED_ADDR`.
- **gateway** — `PURSER_GATEWAY_HOST` + `PURSER_GATEWAY_PORT` (**both
  mandatory**), `PURSER_GATEWAY_INTERNAL_TOKEN`, `PURSER_GATEWAY_API_KEYS`.
