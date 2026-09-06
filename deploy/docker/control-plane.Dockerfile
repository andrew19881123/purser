# syntax=docker/dockerfile:1
#
# Purser Control Plane (Go).
#
# Build context = repo root:
#   docker build -f deploy/docker/control-plane.Dockerfile -t purser/control-plane:0.1.0 .
#
# The module at go/controlplane uses the go.work workspace plus relative
# `replace` directives:
#     ../gen  ../planner  ../../enterprise/license
# Those resolve WITHOUT the workspace as long as GOWORK=off, so we copy the
# sibling module trees in the same repo layout and build with GOWORK=off.

# ── Builder ──────────────────────────────────────────────────────────────
FROM golang:1.27 AS builder

WORKDIR /src

# Copy only the module trees the control-plane build needs, preserving the repo
# layout so the relative `replace` paths resolve correctly:
#   go/controlplane/go.mod -> ../gen, ../planner, ../../enterprise/license
# (Generated *.pb.go under go/gen is committed, so no codegen is required.)
COPY go/ ./go/
COPY enterprise/ ./enterprise/

# Prepare the writable state dir now so it can be COPYed into the distroless
# final stage owned by the nonroot user (65532) — distroless has no shell.
RUN install -d -o 65532 -g 65532 /data

WORKDIR /src/go/controlplane

# GOWORK=off    -> ignore go.work; rely on in-module `replace` directives.
# CGO_ENABLED=0 -> fully static binary (modernc.org/sqlite is pure Go, no cgo).
# -trimpath + -ldflags "-s -w" -> smaller, reproducible binary.
# Modules are fetched from the proxy at build time (go.sum is authoritative);
# CI needs network access or a configured GOPROXY/module cache.
RUN GOWORK=off CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags "-s -w" -o /out/control-plane .

# ── Final ────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static:nonroot

# 8080 = management REST API (/api/v1); 9443 = RegistrationService gRPC (Agents).
EXPOSE 8080 9443

# Persistent state (SQLite registry + internal PKI) lives under /data. The code
# defaults are CWD-relative and would target "/" (not writable by nonroot), so
# point them at the /data volume that the Helm chart mounts as a PVC.
# Security: set PURSER_DB_INTEGRITY_CHECK=1 to run SQLite PRAGMA integrity_check
# at startup. Recommended in production for early corruption detection.
# Set PURSER_PKI_KEY_PASSPHRASE to encrypt the CA private key at rest.
# See: website/docs/operations/security-at-rest.md
ENV PURSER_DB=/data/purser-registry.db \
    PURSER_PKI_DIR=/data/pki-state \
    PURSER_ADDR=:8080 \
    PURSER_GRPC_ADDR=:9443

# /data owned by the distroless nonroot user (UID 65532).
COPY --from=builder --chown=65532:65532 /data /data
COPY --from=builder /out/control-plane /control-plane

USER nonroot:nonroot
ENTRYPOINT ["/control-plane"]
