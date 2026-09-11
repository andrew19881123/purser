# syntax=docker/dockerfile:1
#
# Purser API Gateway (Rust / axum).
#
# Build context = repo root:
#   docker build -f deploy/docker/gateway.Dockerfile -t purser/gateway:0.1.0 .
#
# The purser-proto crate's build.rs compiles the v1 protos with a *vendored*
# protoc (crate `protoc-bin-vendored`) — NO system protoc is required. build.rs
# reads the proto tree at ../../../proto relative to rust/crates/purser-proto,
# i.e. the repo-root proto/, so both trees are copied preserving that layout.

# ── Builder ──────────────────────────────────────────────────────────────
FROM rust:1.98 AS builder

WORKDIR /src

# rust/ (workspace + crates) and proto/ (build.rs input). Cargo.lock is present,
# so the release build is reproducible; crates are fetched at build time (CI
# needs network access or a vendored/offline cargo registry).
COPY rust/ ./rust/
COPY proto/ ./proto/

WORKDIR /src/rust

# Cross-compilation tools for musl static targets.
# musl-tools provides the x86_64 musl linker; gcc-aarch64-linux-gnu is the
# cross-linker used for the aarch64-unknown-linux-musl target.
RUN apt-get update \
 && apt-get install -y --no-install-recommends musl-tools gcc-aarch64-linux-gnu \
 && rm -rf /var/lib/apt/lists/*

# Select Rust target based on TARGETARCH (set automatically by docker buildx).
# Both targets produce fully-static (musl) binaries — no glibc dependency at runtime.
# The binary is placed at /purser-gateway so the COPY in the final stage is arch-agnostic.
ARG TARGETARCH=amd64
RUN case "$TARGETARCH" in \
    amd64) \
        rustup target add x86_64-unknown-linux-musl && \
        CARGO_INCREMENTAL=0 cargo build --release --target x86_64-unknown-linux-musl -p purser-gateway && \
        cp target/x86_64-unknown-linux-musl/release/purser-gateway /purser-gateway ;; \
    arm64) \
        rustup target add aarch64-unknown-linux-musl && \
        CARGO_INCREMENTAL=0 \
        CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=aarch64-linux-gnu-gcc \
        cargo build --release --target aarch64-unknown-linux-musl -p purser-gateway && \
        cp target/aarch64-unknown-linux-musl/release/purser-gateway /purser-gateway ;; \
esac

# ── Final ────────────────────────────────────────────────────────────────
# The binary is fully static (musl) and runs on any Linux — base image choice
# is for ca-certificates and a non-root user; no glibc runtime required.
FROM debian:bookworm-slim AS final

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 65532 --user-group --no-create-home nonroot

# REQUIRED by the binary — Config::from_env fails loudly if unset (no implicit
# defaults). Provide in-container defaults; override in Kubernetes via env.
#   PURSER_GATEWAY_HOST  bind IP   (0.0.0.0 to accept LAN/cluster traffic)
#   PURSER_GATEWAY_PORT  bind port
# Other (optional) env consumed by the gateway, set via the Helm chart:
#   PURSER_GATEWAY_INTERNAL_TOKEN  shared secret for route-sync from the
#                                  Control Plane (X-Purser-Internal-Token);
#                                  must equal the Control Plane's
#                                  PURSER_GATEWAY_TOKEN.
#   PURSER_GATEWAY_API_KEYS        comma-separated client API keys (bearer).
ENV PURSER_GATEWAY_HOST=0.0.0.0 \
    PURSER_GATEWAY_PORT=8080

EXPOSE 8080

COPY --from=builder /purser-gateway /usr/local/bin/purser-gateway

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/purser-gateway"]
