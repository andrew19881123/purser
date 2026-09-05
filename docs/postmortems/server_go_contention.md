# Post-mortem: go/controlplane/server/server.go is a merge contention point

## Symptom
Any two epics that add HTTP endpoints to the control-plane both touch `server.go`
(the single file containing all route registrations and most handler methods).
Git `merge --no-ff` always produces `CONFLICT (content)` here when two epics
land in the same release cycle.

## Root cause
`server.go` is the monolithic routing hub: `routes()` registers all 20+ routes,
and every new endpoint adds both a `HandleFunc` registration and a handler method
to the same file. Two agents editing it in parallel on separate worktree branches
always diverge.

## Patterns that work

### Pattern 1 — Sequential merges with a resolution agent (current approach)
Merge epics that touch `server.go` one at a time. For each conflict:
1. Launch a **foreground** conflict-resolution Agent (not background).
2. Brief it with what each side added — it must keep ALL changes from both sides.
3. Verify `CGO_ENABLED=0 go build ./...` compiles before committing.

This is reliable but serialises the merge phase (not the build phase).

### Pattern 2 — Pre-split ownership (future improvement)
Split `server.go` into domain files (`nodes_handlers.go`, `models_handlers.go`,
`enterprise_handlers.go`, …) with `routes()` only in `server.go`. Then different
epics own different files → true parallel merge. Not yet done as of v0.2.

## What NOT to do
- Do NOT use `git checkout --ours` or `--theirs` — this silently discards one
  side's routes, producing a build that compiles but is missing endpoints.
- Do NOT try to rebase epics onto each other — the worktree branches diverged
  from the same base; rebase produces the same conflicts, just harder to see.

## Conflict anatomy
Typical conflict in `routes()`:
```
<<<<<<< HEAD
    s.mux.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPISpec)  // D1
=======
    s.mux.HandleFunc("POST /api/v1/usage", s.handleRecordUsage)        // F3
>>>>>>> worktree-agent-xxx
```
Resolution: keep both lines. Read the full conflict to check for struct fields,
`New()` wiring, and any helper types — there are usually 2–5 conflict hunks.
