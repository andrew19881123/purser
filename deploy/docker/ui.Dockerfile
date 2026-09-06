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

# Non-root user — UID 65532 matches the distroless nonroot convention used by
# other Purser images (control-plane, gateway).
RUN addgroup -S purser && adduser -S -G purser -u 65532 purser

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

# Allow the non-root user to write nginx runtime files.
# Remove the global `user nginx;` directive (setuid requires root); nginx will
# run as whichever OS user the container starts as.
RUN sed -i '/^user /d' /etc/nginx/nginx.conf \
    && chown -R purser:purser \
         /var/cache/nginx \
         /var/log/nginx \
         /usr/share/nginx/html \
         /etc/nginx/conf.d \
    && touch /var/run/nginx.pid \
    && chown purser:purser /var/run/nginx.pid

EXPOSE 8080

USER purser
