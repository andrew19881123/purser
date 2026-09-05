# Post-mortems index

Read the relevant post-mortem BEFORE acting on the area it covers.
Each one captures a non-obvious root cause that is NOT derivable from the code.

| File | Area | TL;DR |
|---|---|---|
| `worktree_toolchain.md` | Build / toolchain | `.toolchain/` is git-ignored; absent in worktrees |
| `server_go_contention.md` | Go control-plane | `server.go` is shared by all HTTP epics — always conflicts |
| `rust_disk_build.md` | Rust / disk | Full workspace build is 3–4 GB; scope to one crate |
| `e2e_hardcoded_path.md` | E2E / CI | E2E scripts have old `ROOT` path hardcoded |
| `ghcr_visibility.md` | GHCR / release | New packages start private; PATCH API is broken for user packages |
