# Post-mortem: enterprise/LICENSE vs enterprise/license/ collide on macOS (APFS)

## Symptom
On macOS, `git status` in a clean checkout permanently reports a deletion that
nobody made:

```
$ git status --short
 D enterprise/LICENSE
```

`git checkout`, `git restore`, and `git stash` do not clear it. It reappears
immediately in every new clone and every new worktree.

## Root cause
The repository tracks two paths whose names differ only in case:

| Tracked path | Kind | Contents |
|---|---|---|
| `enterprise/LICENSE` | file | Purser Enterprise License text (44 lines) |
| `enterprise/license/` | directory | Go module — `license.go`, `license_test.go`, `go.mod`, `cmd/purser-license/{main.go,main_test.go}` |

macOS APFS is **case-insensitive but case-preserving** by default, so
`enterprise/LICENSE` and `enterprise/license` map to the same filesystem key.
Only one can exist. Git's *index* happily holds both (`git ls-files enterprise/`
lists all six entries); the *working tree* can only materialise one.

Which one wins is deterministic, not random. Git writes index entries in
bytewise path order, and `L` (0x4C) sorts before `l` (0x6C), so
`enterprise/LICENSE` is created first. Git then needs to create the *directory*
`enterprise/license/` to place the Go files, finds a non-directory occupying that
case-folded name, and replaces it. **The directory always wins; the file always
appears deleted.**

The linux/amd64 CI runners use ext4 (case-sensitive), where both paths coexist
normally. This is why the collision never shows up in CI and only bites on macOS
developer machines.

### Verified: the direction does NOT flip in worktrees
An earlier assumption held that a fresh worktree shows the mirror image (the five
`enterprise/license/*.go` files deleted instead). That is **false**. Probed
directly on APFS with `git worktree add --detach`:

```
$ git -C /tmp/probe status --short
 D enterprise/LICENSE          # same as the main tree
$ ls /tmp/probe/enterprise/
license                        # the directory won here too
```

Both the main working tree and a fresh worktree report ` D enterprise/LICENSE`.
Do not expect a mirrored symptom, and do not treat a differing report as a
different problem.

## The real hazard: `git add -A` silently deletes the Enterprise License
The phantom deletion is only cosmetic **until something stages it**. Proven in a
throwaway worktree:

```
$ git add -A
$ git diff --cached --name-status --diff-filter=D
D	enterprise/LICENSE
```

A single `git add -A` (or `git add .`, `git commit -a`, `git stash -u`) records
the removal of the licence text that governs the entire `enterprise/` tree. On a
`release/*` branch that ships the commercial licence terms, this is a
licensing defect, not just a lost file — and because the file *cannot* exist on
the developer's disk, no local check will ever make it look wrong again.

Note the casualty is the **licence text**, not the Go module: the files under
`enterprise/license/` are the ones that survive on disk.

## Rule (mandatory on macOS)
1. **Never** run `git add -A`, `git add .`, `git commit -a`, or `git stash -u`
   in this repository. Stage explicit paths only:
   ```bash
   git add website/docs/getting-started/quickstart.md website/mkdocs.yml
   ```
2. Before every commit, run `git status --short` and confirm the staged column
   (first character) contains only files you intentionally touched. If
   `enterprise/` appears staged, unstage it: `git restore --staged enterprise/`.
3. A leading `` (space) then `D` — ` D enterprise/LICENSE` — is the harmless
   unstaged phantom. `D ` (D then space) in the **first** column means it is
   staged: stop and unstage.

## Do not try to "fix" it
`git checkout enterprise/`, `git restore enterprise/`, and re-cloning all fail to
help: the filesystem cannot represent both names, so git will re-report the
deletion on the next status. Attempts only churn the working tree. The condition
is expected and permanent on macOS — it must be tolerated, not repaired.

A durable repository-level fix means renaming one of the two paths (e.g.
`enterprise/LICENSE` → `enterprise/LICENSE.txt`, updating the
`../../enterprise/LICENSE` reference in `website/docs/enterprise/licensing.md`).
That is a deliberate cross-cutting change, not something to do mid-epic.

## Impact if missed
- `git add -A` on a release branch drops the Enterprise License from the shipped
  tree; the loss is invisible locally and passes CI (ext4 runners see nothing
  wrong).
- Agents waste cycles trying to restore a file that cannot exist, or report a
  clean tree as dirty and refuse to proceed.
