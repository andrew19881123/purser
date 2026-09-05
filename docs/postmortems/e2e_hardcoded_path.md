# Post-mortem: E2E scripts have hardcoded ROOT path

**Status: RESOLVED** (fixed in `release/v0.3`)

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

## Previous workaround
The CI workflow (`.github/workflows/e2e.yml`) worked around it with a symlink:
```yaml
- run: sudo mkdir -p /home/andrea/ideas && sudo ln -s "$GITHUB_WORKSPACE" /home/andrea/ideas/purser
```
This was fragile — it depended on the runner having a writable
`/home/andrea/ideas/` path.

## Fix applied
All four `tools/e2e_*.sh` scripts now auto-detect `ROOT` from their own location:
```bash
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
```
This works from any checkout path. The symlink workaround step was removed from
`.github/workflows/e2e.yml` as it is no longer needed.

Files changed:
- `tools/e2e_full.sh`
- `tools/e2e_multinode.sh`
- `tools/e2e_enroll.sh`
- `tools/e2e_deploy.sh`
- `.github/workflows/e2e.yml` (symlink step removed)
