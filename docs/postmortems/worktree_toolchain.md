# Post-mortem: .toolchain/ absent in git worktrees

## Symptom
Subagent in a worktree runs `source ./env.sh` → `cargo`, `go`, `buf` not found.
Build fails immediately.

## Root cause
`.toolchain/` is in `.gitignore`. Git worktrees check out only tracked files;
untracked/ignored directories do NOT appear in the worktree. Every worktree is
a separate checkout directory, so `.toolchain/` doesn't exist there even though
it does in the main working tree.

## Fix applied
Instruct every worktree agent to source the **main tree's** env.sh with an
absolute path:
```bash
source /home/andrea/Projects/purser/env.sh
```
This adds the main tree's `.toolchain/bin` to `PATH`. The toolchain binaries
(`rustc`, `cargo`, `go`, `buf`, `helm`, `nfpm`) are self-contained executables
with no relative-path dependencies, so they work correctly from any working directory.

## How to apply in subagent briefs
```
## Environment (CRITICAL)
export PATH="/home/andrea/Projects/purser/.toolchain/bin:$PATH"
```
Never `source ./env.sh` in a worktree — always use the absolute path above.

## Impact if missed
Every cargo/go/buf command fails with "command not found".
The worktree silently produces no output and the task appears to hang.
