# Default Agents + Parallel Scheduler Plan

## Overview

Two focused tasks to complete before context compaction or new tooling work:

1. **Built-in default agents** — compile `explore`, `worker`, `reviewer` into the binary alongside the already-built `advisor`, following the exact same pattern as `builtin_advisor.go`. Drop `planner` as per design intent (main + advisor covers it). Update `AGENTS.md` and `docs/packages/2-feature/subagent.md` to reflect the new state.

2. **Parallel scheduler** — add a semaphore-based concurrency limiter inside `Executor.RunBackground` so the system enforces `max_concurrency = 3` total concurrent background agents and `max_writers = 1` concurrent write-permitted agents. This makes orchestration safe by default without breaking the existing goroutine/task model.

3. **Read double-read fix** — minor lean cleanup: eliminate the redundant `os.ReadFile` call in `internal/tool/fs/read.go` by computing `fullContent` from `allLines` (already in memory from the scanner).

Scope is narrow: no new tools, no TUI settings page, no config file changes. These are compile-time defaults and a runtime semaphore.

---

## Sub-Task 1 — Built-in agents: explore, worker, reviewer

**Intent**  
Add three new compiled-in `AgentConfig` values following the exact structure of `builtin_advisor.go`. Register them in `service.go` `Initialize()`. Update docs and `AGENTS.md` to reflect that four agents are now built-in.

**Design decisions**
- `explore`: `PermissionExplore`, tools `Read/Grep/Glob`, `MaxSteps: 30`. Read-only, fast, cheap. System prompt: codebase exploration assistant.
- `worker`: `PermissionAcceptEdits`, all built-in coding tools (inherits from default allow), `MaxSteps: 50`. System prompt: focused implementation agent.
- `reviewer`: `PermissionExplore`, tools `Read/Grep/Glob`, `MaxSteps: 30`. Read-only like advisor but focused on correctness review, not architectural trade-offs. System prompt: correctness + style reviewer.
- `planner` is NOT added — main agent + advisor covers that role.
- All four built-ins register before `LoadAgents()` so user-defined `.san/agents/<name>.md` overrides them as before.

**Expected Outcomes**
- `subagent.Initialize()` registers 4 built-in agents: `advisor`, `explore`, `worker`, `reviewer`.
- Each has a correct `Source: "builtin"` tag, system prompt, tools list, and permission mode.
- `AGENTS.md` table updated: advisor/explore/worker/reviewer all marked as built-in.
- `docs/packages/2-feature/subagent.md` updated to list all four built-ins.
- `go test ./internal/subagent/...` passes.

**Todo List**
1. Create `internal/subagent/builtin_explore.go` — `BuiltinExploreConfig()`, `PermissionExplore`, `Read/Grep/Glob`, `MaxSteps: 30`.
2. Create `internal/subagent/builtin_worker.go` — `BuiltinWorkerConfig()`, `PermissionAcceptEdits`, empty AllowTools (inherits default), `MaxSteps: 50`.
3. Create `internal/subagent/builtin_reviewer.go` — `BuiltinReviewerConfig()`, `PermissionExplore`, `Read/Grep/Glob`, `MaxSteps: 30`.
4. Edit `internal/subagent/service.go` `Initialize()` — call `Register(BuiltinExploreConfig())`, `Register(BuiltinWorkerConfig())`, `Register(BuiltinReviewerConfig())` alongside the existing `BuiltinAdvisorConfig()` call.
5. Update `AGENTS.md` default agents table — mark explore/worker/reviewer as `built-in`, remove planner row.
6. Update `docs/packages/2-feature/subagent.md` — list all four built-ins in Lifecycle section.

**Relevant Context**
- `internal/subagent/builtin_advisor.go` — exact template to follow.
- `internal/subagent/service.go:26-44` — `Initialize()` registration order.
- `internal/subagent/types.go:21-46` — `PermissionMode` constants.
- `internal/tool/tool.go` — `ToolRead`, `ToolGrep`, `ToolGlob` constants.
- `AGENTS.md:57-73` — current agents table.

**Status**: [x] done

---

## Sub-Task 2 — Parallel scheduler: max_concurrency + max_writers

**Intent**  
Add a semaphore pair to `Executor` that caps concurrent background agents at 3 total and caps write-permitted agents (anything with `PermissionMode != PermissionExplore`) at 1. This enforces the orchestration topology described in the design:

```
MAIN
  ├── explore   ┐
  ├── explore   ├─ parallel, max 3 total
  └── advisor   ┘
        ↓
     worker     ← serialized, max 1 writer
        ↓
     reviewer
```

**Design decisions**
- Semaphore via `chan struct{}` — zero deps, idiomatic Go.
- `maxConcurrency = 3`, `maxWriters = 1` as package-level constants (not config file yet).
- Acquire happens before goroutine launch in `RunBackground`; release happens in the goroutine after `Run()` returns (deferred).
- Writer semaphore is acquired additionally if `config.PermissionMode != PermissionExplore`.
- Blocking is context-aware: `select { case sem <- struct{}{}: ... case <-ctx.Done(): ... }` so a cancelled request does not permanently block a slot.
- No TUI settings yet — that is a separate future sub-task.
- `Run()` (foreground) does NOT use semaphores — foreground calls are always bounded by the model's own tool call serialization.

**Expected Outcomes**
- `Executor` has `concurrencySem chan struct{}` (cap 3) and `writerSem chan struct{}` (cap 1).
- `RunBackground` acquires slots before launching goroutine and releases on goroutine exit.
- A 4th concurrent `RunBackground` blocks until one slot frees.
- Two concurrent writer agents are serialized: second waits for first to complete.
- Cancellation during semaphore wait returns an error (not a hang).
- `go test ./internal/subagent/...` passes.

**Todo List**
1. Add constants `maxBackgroundConcurrency = 3` and `maxBackgroundWriters = 1` to `executor.go` (package-level).
2. Add `concurrencySem chan struct{}` and `writerSem chan struct{}` fields to `Executor` struct.
3. Initialize both channels in `NewExecutor()` using `make(chan struct{}, maxBackgroundConcurrency)` and `make(chan struct{}, maxBackgroundWriters)`.
4. Write a helper `func (e *Executor) acquireSemaphores(ctx context.Context, needsWriteLock bool) error` that acquires `concurrencySem` then optionally `writerSem`, both context-aware.
5. Write a paired `func (e *Executor) releaseSemaphores(needsWriteLock bool)` that releases in reverse order.
6. In `RunBackground`, determine `needsWriteLock` from `config.PermissionMode != PermissionExplore`.
7. Call `acquireSemaphores(ctx, needsWriteLock)` before the goroutine launch; return error if context cancelled.
8. Inside the goroutine, defer `releaseSemaphores(needsWriteLock)`.
9. Update `docs/packages/2-feature/subagent.md` — document the concurrency model and limits in the Lifecycle section.

**Relevant Context**
- `internal/subagent/executor.go:35-55` — `Executor` struct.
- `internal/subagent/executor_run.go:234-297` — `RunBackground` implementation.
- `internal/subagent/types.go:21-46` — `PermissionMode` values; `PermissionExplore` is the read-only mode.
- `internal/task/manager.go:28-55` — `Manager` struct; note tasks are kept forever, so releasing semaphores promptly matters.

**Status**: [x] done

---

## Sub-Task 3 — Read tool: eliminate double os.ReadFile

**Intent**  
`internal/tool/fs/read.go` already reads all lines via `bufio.Scanner` into `allLines`, then calls `os.ReadFile` a second time on line 219 to build `fullContent` for hash computation and summary building. This is a double disk read. Fix it by reconstructing `fullContent` from `allLines` using `strings.Join(allLines, "\n")` — same content, one I/O.

**Design decisions**
- `scanner.Text()` strips `\r` from `\r\n` lines, so `strings.Join(allLines, "\n")` gives the same normalized content that the current code produces after `strings.ReplaceAll(fullContent, "\r\n", "\n")`.
- The BOM strip (`strings.TrimPrefix(string(fullBytes), "\ufeff")`) needs to be handled: the scanner does NOT strip the BOM from the first line. So check `len(allLines) > 0` and trim the BOM prefix from `allLines[0]` before joining, or trim the joined result once at the front.
- After this change, `os.ReadFile` on line 219 is removed and `fullBytes` is no longer needed.
- This fix touches only the read tool internals and has no observable behavioral change.

**Expected Outcomes**
- `internal/tool/fs/read.go` no longer calls `os.ReadFile` after the scanner loop.
- `fullContent` is built from `allLines` with BOM handling preserved.
- All existing `read.go` tests pass (`go test ./internal/tool/...`).

**Todo List**
1. After the scanner loop (after line 206), replace the `os.ReadFile` + BOM strip block with:
   ```go
   // Build fullContent from the already-read lines to avoid a second disk read.
   if len(allLines) > 0 {
       allLines[0] = strings.TrimPrefix(allLines[0], "\ufeff")
   }
   fullContent := strings.Join(allLines, "\n")
   ```
2. Remove the now-unused `fullBytes` variable and the `strings.ReplaceAll` call.
3. Run `go test ./internal/tool/...` to confirm no regressions.

**Relevant Context**
- `internal/tool/fs/read.go:195-222` — scanner loop and current `os.ReadFile` block.
- `buildSummary(allLines, relPath, fullContent)` at line 229 — consumes both `allLines` and `fullContent`.
- `ComputeFileHash(fullContent)` (somewhere below) — only consumer of raw `fullContent` bytes.

**Status**: [x] done

---

## Plan Validation Checklist

- [ ] Sub-task order correct: 1 (agents) → 3 (read fix, tiny) → 2 (scheduler)?
- [ ] Worker agent: should AllowTools be empty (inherit all) or an explicit list of coding tools?
- [ ] Scheduler constants `max_concurrency=3` and `max_writers=1` are hardcoded — confirm no config file needed at this stage.
- [ ] `PermissionDontAsk` mode: leave as-is (not wired), not part of this plan.
- [ ] TUI per-agent model/concurrency settings: explicitly out of scope for this plan.
