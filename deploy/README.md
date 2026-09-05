# Deploying Purser

This directory holds everything needed to run the Purser control plane on
Kubernetes. The three component images are **published on GHCR** and the chart's
`values.yaml` defaults to them, so a normal install pulls prebuilt images — **no
build step required** (jump to [§2, Installing with Helm](#2-installing-with-helm)).
Section 1 below is the **optional "build your own images"** path for a private
registry or air-gapped mirror.

```
deploy/
├── docker/
│   ├── control-plane.Dockerfile   # Go control plane  -> distroless/static
│   ├── gateway.Dockerfile         # Rust API gateway   -> debian:bookworm-slim
│   ├── ui.Dockerfile              # React SPA          -> nginx:alpine
│   └── nginx.conf                 # SPA routing config for the UI image
└── helm/
    └── purser/                    # Helm chart (control-plane + gateway + ui)
```

## Architecture recap

Purser has three in-cluster components plus an out-of-cluster fleet:

| Component      | Language | Listens on                    | Image (GHCR default)                          |
|----------------|----------|-------------------------------|-----------------------------------------------|
| Control Plane  | Go       | `8080` REST API, `9443` gRPC  | `ghcr.io/andrew19881123/purser-control-plane` |
| API Gateway    | Rust     | `8080` HTTP (OpenAI-compat)   | `ghcr.io/andrew19881123/purser-gateway`       |
| Operator UI    | React    | `80` HTTP (nginx SPA)         | `ghcr.io/andrew19881123/purser-ui`            |
| **Agents**     | Rust     | run **outside** Kubernetes    | host packages (`packaging/`)                  |

The Control Plane owns the SQLite **Registry** and an internal **PKI** (CA that
issues mTLS certs to Agents). The Gateway exposes the OpenAI-compatible
inference plane; the Control Plane pushes route updates to it over HTTP,
authenticated by a **shared internal token**.

---

## 1. Building the images (optional — build your own)

> The published GHCR images (`ghcr.io/andrew19881123/purser-{control-plane,gateway,ui}:0.1.0`)
> are the default; you only need this section to build and push images to your
> **own** registry (private / air-gapped). To use the published images, skip
> straight to [§2](#2-installing-with-helm).

All three images build **locally with Docker** from the **repository root** as
the build context (the Dockerfiles `COPY` sibling module trees, so the context
must be the repo root, not `deploy/docker/`). A repo-root `.dockerignore` keeps
the context lean (excludes `.toolchain/`, `target/`, `node_modules/`, `dist/`,
`docs/`, `.git/`).

> **Docker permissions (this session):** the current shell's user is in the
> `docker` group but that membership is not yet active in the shell, so every
> `docker` command needs `sudo` (passwordless here). The commands below use
> `sudo docker`. To drop the `sudo`, activate the group first with
> `newgrp docker` (or simply open a new login shell), then run plain `docker …`.

```bash
# From the repository root:

sudo docker build -f deploy/docker/control-plane.Dockerfile -t purser/control-plane:0.1.0 .
sudo docker build -f deploy/docker/gateway.Dockerfile       -t purser/gateway:0.1.0 .
sudo docker build -f deploy/docker/ui.Dockerfile            -t purser/ui:0.1.0 .
```

### What each build does

- **control-plane** (multi-stage, `golang:1.27` → `gcr.io/distroless/static:nonroot`).
  The `go/controlplane` module uses the `go.work` workspace plus relative
  `replace` directives (`../gen`, `../planner`, `../../enterprise/license`).
  The build copies `go/` and `enterprise/` preserving the repo layout and
  compiles with **`GOWORK=off`** so the in-module `replace` paths resolve
  without the workspace:
  `GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"`.
  `CGO_ENABLED=0` yields a fully static binary (`modernc.org/sqlite` is pure Go).
  Runs as UID 65532 (`nonroot`); state lives on the `/data` volume.

- **gateway** (multi-stage, `rust:1.98` → `debian:bookworm-slim`).
  The `purser-proto` crate's `build.rs` compiles the protos with a **vendored
  protoc** (`protoc-bin-vendored`), so **no system protoc is required**. The
  build copies `rust/` and `proto/` (build.rs reads `../../../proto`) and runs
  `CARGO_INCREMENTAL=0 cargo build --release -p purser-gateway`. Runtime is
  glibc (`debian:bookworm-slim`, matching the builder's glibc), with
  `ca-certificates`, running as UID 65532.
  Note: `rust/rust-toolchain.toml` pins the `stable` channel (+ clippy/rustfmt),
  so the first build has rustup fetch the stable toolchain.

- **ui** (multi-stage, `node:22` → `nginx:alpine`).
  `npm ci && npm run build` produces the Vite bundle (`base: './'`, fully
  self-contained / air-gap friendly); nginx serves it with an SPA fallback to
  `index.html` (see `deploy/docker/nginx.conf`, which also exposes `/healthz`).

### Verified build results (this environment)

All three images were **built locally with Docker (BuildKit)** from the repo
root and are present in the local image store. Images are **not** pushed to any
registry by these steps.

| Image                       | Base (final stage)             | Disk usage | Content size |
|-----------------------------|--------------------------------|------------|--------------|
| `purser/control-plane:0.1.0`| `gcr.io/distroless/static`     | 31.8 MB    | ~8.2 MB      |
| `purser/gateway:0.1.0`      | `debian:bookworm-slim`         | 135 MB     | ~34 MB       |
| `purser/ui:0.1.0`           | `nginx:alpine`                 | 103 MB     | ~28.9 MB     |

Check sizes locally with:

```bash
sudo docker images 'purser/*'
```

(*Disk usage* is the on-disk footprint including shared base layers; *content
size* is the compressed content. Both are reported by the containerd image
store; use `sudo docker` in this session — see the note above.)

### CI / offline notes

- The **control-plane** build fetches Go modules from the proxy (`go.sum` is
  authoritative); the **gateway** build fetches crates per `Cargo.lock`; the
  **ui** build fetches npm packages per `package-lock.json`. CI needs network
  access, or a configured `GOPROXY` / vendored cargo registry / npm mirror for
  air-gapped builds.
- Generated `*.pb.go` under `go/gen/` is committed, so the Go build needs no
  codegen step.
- The Rust and Go builder stages are large; on a disk-constrained runner build
  one image at a time and run `sudo docker builder prune -af` between builds to
  reclaim the BuildKit cache. The final images (distroless / slim / nginx) stay
  small.

### Pushing to a registry

```bash
REG=registry.example.com/purser
for c in control-plane gateway ui; do
  docker tag purser/$c:0.1.0 $REG/$c:0.1.0
  docker push $REG/$c:0.1.0
done
```

---

## 2. Installing with Helm

The chart is **published as an OCI artifact** on GHCR
(`oci://ghcr.io/andrew19881123/charts/purser`, version `0.1.0`) and validated
with `helm lint` + `helm template`. Its default `values.yaml` points at the
**published GHCR images**, so a one-command install pulls both the prebuilt
chart and the prebuilt images with **no clone and no build step**.

```bash
# Default install — pulls the chart from GHCR (OCI) + the published images:
#   chart:  oci://ghcr.io/andrew19881123/charts/purser:0.1.0
#   images: ghcr.io/andrew19881123/purser-{control-plane,gateway,ui}:0.1.0
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.1.0

# Expose the Control Plane so the out-of-cluster LAN fleet can enroll (see §3):
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.1.0 \
  --set controlPlane.service.type=LoadBalancer
```

The OCI chart package is **public**, so no registry login is needed. For a
**private** chart registry, authenticate first with
`helm registry login ghcr.io -u <user> --password-stdin` (token with
`read:packages`) before installing.

**From source (to customize the chart).** To edit the chart or install without
OCI, use the in-tree path at `deploy/helm/purser`:

```bash
# From a clone of the repo:
helm install purser deploy/helm/purser \
  --set controlPlane.service.type=LoadBalancer

# Point at your OWN registry + tag (e.g. after building your own images, §1):
helm install purser deploy/helm/purser \
  --set image.controlPlane.repository=registry.example.com/purser/control-plane \
  --set image.gateway.repository=registry.example.com/purser/gateway \
  --set image.ui.repository=registry.example.com/purser/ui \
  --set image.controlPlane.tag=0.1.0 \
  --set image.gateway.tag=0.1.0 \
  --set image.ui.tag=0.1.0
```

To re-publish the chart after a version bump: `helm package deploy/helm/purser`
then `helm push purser-<version>.tgz oci://ghcr.io/andrew19881123/charts`
(requires `helm registry login ghcr.io` with a `write:packages` token).

### Image visibility & private registries

The official GHCR images are **public** — an anonymous `docker pull` works — so
the default install needs **no pull secret**. You only need one if you push the
images to a **private** registry of your own (or make your fork's packages
private); create it and reference it via `imagePullSecrets`:

```bash
kubectl create secret docker-registry ghcr \
  --docker-server=ghcr.io --docker-username=<user> --docker-password=<GITHUB_PAT>
helm install purser deploy/helm/purser --set imagePullSecrets[0].name=ghcr
```

### Key values

| Value | Default | Notes |
|-------|---------|-------|
| `replicaCount` | `1` | **Keep at 1.** SQLite Registry + PKI are single-writer; multi-replica HA needs the enterprise Registry Raft backend. |
| `service.type` | `ClusterIP` | Global default; each component inherits it unless overridden. |
| `controlPlane.service.type` | inherits | Set `LoadBalancer`/`NodePort` so LAN Agents can reach it (see networking). |
| `controlPlane.persistence.{enabled,size,storageClass}` | `true`, `2Gi`, default SC | PVC mounted at `/data` for SQLite + PKI. |
| `gateway.internalToken` | `""` (auto-generated) | Shared route-sync secret; stored in a Secret and injected into both pods. |
| `gateway.apiKeys` | `""` | Comma-separated client bearer keys. Empty ⇒ **OPEN DEV MODE**. |
| `license.key` | `""` | Enterprise license (`PURSER_LICENSE_KEY`), mounted as a Secret. Empty ⇒ community edition. |
| `*.resources` | `{}` | Placeholders — set requests/limits for production. |

### The shared gateway token

Control Plane → Gateway route sync is authenticated by one shared secret:

- The Control Plane sends it as `PURSER_GATEWAY_TOKEN` (header
  `X-Purser-Internal-Token`).
- The Gateway validates it as `PURSER_GATEWAY_INTERNAL_TOKEN`.

The chart generates a random token at install time, stores it in the
`*-gateway` Secret, and injects it into **both** Deployments via `secretKeyRef`,
so they always agree. Pin it explicitly with `--set gateway.internalToken=…`.

---

## 3. Networking — reaching an out-of-cluster fleet

**Agents run OUTSIDE Kubernetes** (host packages, see `packaging/`). They must
reach the Control Plane to enroll and heartbeat:

- **RegistrationService** — gRPC/mTLS on port `9443`.
- **Management REST API** — HTTP on port `8080` (also mints join tokens).

With the default `ClusterIP`, the Control Plane is only reachable inside the
cluster and **Agents cannot join**. Expose it to the LAN:

```bash
# LoadBalancer (cloud / MetalLB):
helm upgrade --install purser deploy/helm/purser \
  --set controlPlane.service.type=LoadBalancer

# or NodePort with pinned ports:
helm upgrade --install purser deploy/helm/purser \
  --set controlPlane.service.type=NodePort \
  --set controlPlane.service.httpNodePort=30080 \
  --set controlPlane.service.grpcNodePort=30443
```

Then enroll an Agent (from the Agent host):

```bash
# 1. Mint a join token via the REST API:
curl -sS -X POST http://<control-plane-host>:8080/api/v1/join-token

# 2. Point the Agent at the Control Plane gRPC endpoint
#    (<control-plane-host>:9443) with that token. See packaging/ for the
#    host-package enrollment flow (systemd / launchd / windows).
```

The **inference / data plane** (engine ↔ engine, and Agent ↔ local engine)
stays on the **trusted subnet** and does **not** need to transit Kubernetes —
only the control-plane rendezvous (registration/heartbeat/orchestration) and,
optionally, the Gateway and UI need external exposure.

- Expose the **Gateway** (`LoadBalancer`/`NodePort`/Ingress) only if
  OpenAI-compatible clients live outside the cluster.
- Expose the **UI** similarly (or reach it via `kubectl port-forward`).

---

## 3b. Networking models — Ingress vs. LoadBalancer

Purser supports two ways to expose its components. Choose the one that fits
your environment.

### Model 1 — No Ingress (default, recommended for LAN fleets)

Each component is its own Service. Expose the Control Plane so that
**out-of-cluster Agents** can register (gRPC :9443) and you can manage the
cluster (REST :8080):

```bash
helm install purser deploy/helm/purser \
  --set controlPlane.service.type=LoadBalancer
```

- Agents on the LAN reach the Control Plane's external IP directly.
- The UI and Gateway can be reached via `kubectl port-forward`, `NodePort`, or a
  separate `LoadBalancer` — whatever suits your setup.
- The UI's `apiBaseUrl` / `gatewayBaseUrl` must be **absolute URLs** pointing at
  the exposed Control Plane and Gateway addresses when they are not on the same
  origin as the browser.

### Model 2 — Ingress (optional, single-hostname mode)

All three components are served from **one hostname** via a Kubernetes Ingress:

| Path prefix | Backend           | Port |
|-------------|-------------------|------|
| `/api`      | control-plane     | 8080 |
| `/v1`       | gateway           | 8080 |
| `/`         | ui (nginx)        | 80   |

```bash
helm install purser deploy/helm/purser \
  --set ingress.enabled=true \
  --set ingress.host=purser.example.com \
  --set ingress.className=nginx          # optional, leave empty for cluster default
```

With the Ingress in place, the UI's same-origin defaults (`/api/v1` and `/v1`)
route correctly without any `apiBaseUrl` / `gatewayBaseUrl` overrides. Point
DNS (or `/etc/hosts`) for `purser.example.com` at your Ingress controller's
external IP and open `http://purser.example.com` in a browser.

**TLS example** (cert-manager / manual secret):

```bash
helm install purser deploy/helm/purser \
  --set ingress.enabled=true \
  --set ingress.host=purser.example.com \
  --set ingress.className=nginx \
  --set "ingress.annotations.cert-manager\.io/cluster-issuer=letsencrypt-prod" \
  --set ingress.tls[0].secretName=purser-tls \
  --set ingress.tls[0].hosts[0]=purser.example.com
```

> **Note:** Agents always contact the Control Plane directly (gRPC/mTLS on
> port 9443). Routing gRPC through an HTTP/1.1 Ingress is not supported; keep
> the Control Plane Service accessible on its own IP for Agent enrollment.

---

## 4. Uninstall

```bash
helm uninstall purser
# PVCs are retained by design (they hold the Registry + CA). Remove explicitly:
kubectl delete pvc -l app.kubernetes.io/instance=purser
```
