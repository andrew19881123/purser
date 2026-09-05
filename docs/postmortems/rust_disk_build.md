# Post-mortem: Rust full-workspace build exhausts disk

## Symptom
`cargo build` of the full workspace in a new directory (worktree or fresh checkout)
fills the disk. Link step fails with `SIGBUS` or `No space left on device`.
Subsequent `git` and `cargo` operations also fail.

## Root cause
The Purser Rust workspace has several crates with heavy dependencies (axum, tonic,
opentelemetry, foca). A full debug build of all crates produces 3–4 GB of object
files, intermediate artifacts, and the final binaries in `target/`. On the dev
machine (36 GB disk, shared with Docker layer cache and OS), disk fills up when
multiple builds run concurrently or after a fresh worktree build.

## Fixes applied

### 1. Shared `CARGO_TARGET_DIR` (primary fix)
All subagents are instructed to set:
```bash
export CARGO_TARGET_DIR=/tmp/purser-shared-target
```
This points all cargo builds at one shared cache on `/tmp` (tmpfs, separate
filesystem), so worktrees don't create independent `target/` trees.
The cache is shared across concurrent builds; Cargo's internal locking prevents
corruption. This alone reduces per-worktree overhead to ~0.

### 2. Per-crate builds (scope reduction)
Never `cargo build` the full workspace; always scope:
```bash
cargo build -p purser-agent
cargo build -p purser-gateway
cargo test  -p purser-agent
```
The `purser-proto` generated crate is built as a side-effect and cached.

### 3. Lean Rust profile (`rust/Cargo.toml`)
```toml
[profile.dev]
debug = 0      # no DWARF; halves the target/ size
[profile.release]
strip = true
lto   = false  # lto=true triples link time and memory
```

### 4. Reclaim before a large build
```bash
df -h /           # check free space — proceed only if > 4 GB
sudo docker system prune -f   # reclaim Docker build cache if needed
```

## Impact if ignored
Build appears to succeed silently then fails at link with `SIGBUS` (mmap on full
filesystem). Corrupted artifacts may be left behind. `cargo clean` is safe but
slow; deleting `rust/target/debug` from the main tree is sufficient.
