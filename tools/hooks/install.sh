#!/usr/bin/env bash
# Activate the versioned hooks directory. Opt-in, run once per clone.
set -eu
root="$(git rev-parse --show-toplevel)"
chmod +x "$root/tools/hooks/pre-push"
git -C "$root" config core.hooksPath tools/hooks
echo "hooks installed: core.hooksPath = tools/hooks"
