#!/bin/sh
# postremove — .deb/.rpm scriptlet for purser-agent.
#
# Runs as root after the files are removed. Reload systemd so it forgets the
# removed unit. The 'purser' system user/group is intentionally left behind
# (it may still own /var/lib/purser state); removing users on package removal
# is discouraged.
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

exit 0
