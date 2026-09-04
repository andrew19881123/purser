#!/bin/sh
# postinstall — .deb/.rpm scriptlet for purser-agent.
#
# Runs as root after the files are unpacked. Idempotent: safe on install AND
# upgrade. It does NOT start the service — the agent needs a join token and a
# control-plane address in /etc/purser/agent.env first, so enabling is left to
# the operator.
set -e

# --- system user/group 'purser' (no login, no home) -------------------------
if ! getent group purser >/dev/null 2>&1; then
    groupadd --system purser
fi
if ! getent passwd purser >/dev/null 2>&1; then
    useradd --system --gid purser --no-create-home \
        --home-dir /var/lib/purser --shell /usr/sbin/nologin \
        --comment "Purser Fleet Agent" purser
fi

# --- let systemd pick up the freshly installed unit -------------------------
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

cat <<'EOF'

purser-agent installed. It was NOT started — configuration is required first.

  1. Edit /etc/purser/agent.env and set at least:
         PURSER_CONTROL_PLANE_ADDR   (control-plane gRPC endpoint)
         PURSER_CLUSTER_ID           (must match the control plane)
         PURSER_JOIN_TOKEN           (one-time token from the control plane)
     SECURITY: bind PURSER_AGENT_BIND on a TRUSTED-SUBNET interface only —
     the inference engine worker is NOT sandboxed.

  2. Enable and start the service:
         systemctl enable --now purser-agent

  Status / logs:
         systemctl status purser-agent
         journalctl -u purser-agent -f

EOF

exit 0
