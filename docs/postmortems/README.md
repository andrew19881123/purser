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
| `macos_case_collision.md` | Git / macOS | `enterprise/LICENSE` vs `enterprise/license/` collide on APFS — never `git add -A` |
| `macos_toolchain_bootstrap.md` | Build / toolchain | `make setup` fetches linux-amd64; `env.sh` "ready" is not proof; `GOPROXY=direct` behind the proxy |
| `demo_stack_fragility.md` | Local demo / gateway | Gateway routes are push-only and die on restart; single-file bind mounts pin the inode; compose + `ui:80` were both broken |
| `test_architecture.md` | Testing (decision doc) | Every v0.6→v0.7 defect sat on a component junction; registry-driven contract tests + Go E2E harness + local pre-push gate, zero new CI jobs |
