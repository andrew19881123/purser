#!/bin/sh
# preremove — .deb/.rpm scriptlet for purser-agent.
#
# Runs as root before the files are removed. Stop + disable the service on a
# real removal, but NOT on an upgrade (dpkg passes "upgrade"; rpm passes "1"),
# so upgrades don't bounce a running agent unnecessarily.
set -e

# dpkg: $1 = "remove" | "purge" on removal, "upgrade" on upgrade.
# rpm:  $1 = "0" on final removal, "1" on upgrade.
case "$1" in
    remove|purge|0)
        if command -v systemctl >/dev/null 2>&1; then
            if systemctl is-active --quiet purser-agent 2>/dev/null; then
                systemctl disable --now purser-agent >/dev/null 2>&1 || true
            else
                systemctl disable purser-agent >/dev/null 2>&1 || true
            fi
        fi
        ;;
esac

exit 0
