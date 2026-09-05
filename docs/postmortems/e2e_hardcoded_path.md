# Post-mortem: E2E scripts have hardcoded ROOT path

## Symptom
`tools/e2e_full.sh` and `tools/e2e_multinode.sh` fail immediately on a fresh
checkout or after the repo is moved, with "No such file or directory" on binary
paths even when binaries exist.

## Root cause
Both scripts pin the repo root as a hardcoded absolute path:
```bash
ROOT=/home/andrea/ideas/purser   # ← old location, now stale
```
The repo moved to `/home/andrea/Projects/purser`. Every binary path (`$ROOT/bin/…`,
`$ROOT/rust/target/…`) resolves to the old location and fails.

## Current state
Not yet fixed in the scripts themselves (fixing them would be the correct solution).
The CI workflow (`.github/workflows/e2e.yml`) works around it with a symlink:
```yaml
- run: sudo mkdir -p /home/andrea/ideas && sudo ln -s "$GITHUB_WORKSPACE" /home/andrea/ideas/purser
```
This is a fragile workaround — it depends on the runner having a writable
`/home/andrea/ideas/` path.

## Correct fix (pending)
Replace the hardcoded `ROOT=` with:
```bash
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
```
This derives the repo root from the script's own location, works from any
checkout path, and makes the symlink workaround unnecessary.

## Impact
- Local runs: always run from the repo root after `source ./env.sh`.
  The scripts only fail if the `ROOT` variable resolves to a non-existent path.
- CI: the symlink workaround is in place; E2E CI job works.
- New contributors: will hit this on first run if their checkout is not at the
  exact old path. Fix the scripts before v1.0.
