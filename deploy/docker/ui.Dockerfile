# syntax=docker/dockerfile:1
#
# Purser Operator UI (React + Vite SPA, served by nginx).
#
# Build context = repo root:
#   docker build -f deploy/docker/ui.Dockerfile -t purser/ui:0.1.0 .
#
# The Vite build uses base './' (relative asset URLs), so the static bundle is
# self-contained and can be served from any mount path.

# ── Builder ──────────────────────────────────────────────────────────────
FROM node:22 AS builder

WORKDIR /app/ui

# Install against the lockfile first for reproducible, cache-friendly builds.
COPY ui/package.json ui/package-lock.json ./
RUN npm ci

# Build the SPA -> /app/ui/dist.
COPY ui/ ./
RUN npm run build

# ── Final ────────────────────────────────────────────────────────────────
FROM nginx:alpine AS final

# SPA routing config: unknown routes fall back to index.html (client-side
# react-router deep links / reloads work); also serves env.js uncached.
COPY deploy/docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/ui/dist /usr/share/nginx/html

# Runtime config generator. nginx:alpine's default entrypoint runs every
# /docker-entrypoint.d/*.sh before starting nginx, so this regenerates
# /usr/share/nginx/html/env.js from environment variables (PURSER_API_BASE_URL,
# …) at container START — configuring the API base URL without a rebuild, since
# Vite bakes build-time env. We keep the base image's ENTRYPOINT/CMD.
COPY deploy/docker/docker-entrypoint.d/40-purser-runtime-config.sh /docker-entrypoint.d/40-purser-runtime-config.sh
RUN chmod +x /docker-entrypoint.d/40-purser-runtime-config.sh

EXPOSE 80
