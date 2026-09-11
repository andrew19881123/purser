# ARM64 Platform Support

Purser ships multi-architecture (`linux/amd64`, `linux/arm64`) container images for
all components. ARM64 support covers Apple Silicon Macs running Linux containers,
AWS Graviton instances, and Raspberry Pi 5 boards.

## Supported platforms

| Architecture | Examples | Status |
|---|---|---|
| `linux/amd64` | x86-64 servers, Intel/AMD cloud VMs | Fully supported |
| `linux/arm64` | Apple Silicon (M1–M4), AWS Graviton 2/3/4, Raspberry Pi 5 | Fully supported |

## Container images

All GHCR images are multi-arch manifests. Docker and Kubernetes automatically pull
the correct variant for the node architecture — no extra flags are required:

```bash
# amd64 and arm64 nodes both pull the right image automatically
docker pull ghcr.io/andrew19881123/purser-control-plane:latest
docker pull ghcr.io/andrew19881123/purser-gateway:latest
docker pull ghcr.io/andrew19881123/purser-ui:latest
```

## Helm chart

The Helm chart pulls images from GHCR OCI. Because the images are multi-arch manifests,
a single `helm install` works on both amd64 and arm64 clusters:

```bash
helm install purser oci://ghcr.io/andrew19881123/charts/purser \
  --namespace purser --create-namespace \
  --version 0.6.0
```

No node selectors or architecture-specific overrides are needed for standard deployments.

## Feature support on arm64

| Feature | arm64 | Notes |
|---|---|---|
| Control plane (registry, PKI, REST API) | Yes | Pure-Go, fully static binary |
| API gateway (OpenAI-compatible) | Yes | musl-static Rust binary |
| Operator UI | Yes | nginx/alpine has an arm64 image |
| Fleet agent (basic) | Yes | musl-static Rust binary |
| Fleet agent mDNS discovery (`--features mdns`) | Yes | |
| Fleet agent HTTP weight fetch (`--features http-fetch`) | Yes | |
| Fleet agent llama.cpp backend (`--features llamacpp`) | Conditional | Requires a native arm64 llama.cpp build — see below |

## llama.cpp on arm64

The `llamacpp` feature is not compiled into the default agent binary. To enable it
on an arm64 node, compile the agent from source against an arm64 llama.cpp build:

```bash
# On the arm64 node (or cross-compile with an aarch64 toolchain)
cargo build --release -p purser-agent --features llamacpp \
  --target aarch64-unknown-linux-gnu
```

Set `PURSER_LLAMACPP_BIN` to the path of the resulting binary in your agent
configuration. Apple Silicon (NEON/Metal) and AWS Graviton (NEON/SVE) both have
first-class llama.cpp support and deliver strong inference throughput.

## Binary packages (`.deb` / `.rpm`)

ARM64 `.deb` and `.rpm` packages for the fleet agent are published to the apt/yum
repository alongside the amd64 packages. Install via the package manager on arm64
Debian/Ubuntu or RHEL/Fedora systems:

```bash
# Debian / Ubuntu (arm64)
apt-get install purser-agent

# RHEL / Fedora (arm64)
dnf install purser-agent
```

See [Linux Agent installation](linux-agent.md) for repository configuration.

## Verifying the architecture of a pulled image

```bash
docker inspect --format '{{.Architecture}}' \
  ghcr.io/andrew19881123/purser-control-plane:latest
# → arm64  (on an arm64 host)
# → amd64  (on an amd64 host)
```

To inspect the full multi-arch manifest:

```bash
docker manifest inspect ghcr.io/andrew19881123/purser-control-plane:latest \
  | python3 -m json.tool | grep -A2 '"architecture"'
```
