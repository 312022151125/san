# Parallel Subagent Execution Plan

## Overview

Add real parallel subagent execution with a concurrency pool, write-safe
serialization, compact fan-in, and failure isolation. The LLM model is taught
to prefer parallel fan-out for independent work.

**What exists today:**
- `executor.RunBackground` already spawns goroutines — multiple background tasks
  run concurrently with no coordination today.
- `task.AgentTask` has a `done` channel and `WaitForCompletion(timeout)`.
- `task.Manager` is fully mutex-safe.
- `broker` routes inter-agent messages.

**What is missing:**
1. A concurrency semaphore — no cap on simultaneous agents.
2. A write-conflict guard — concurrent write-capable workers share the working tree unsafely.
3. Settings field `subagents.maxConcurrency` (default 2).
4. System prompt guidance for parallel-first orchestration.
5. Tests proving real overlap, not just scheduling.

**Non-goals:**
- Distributed scheduler, priority queue, or work-stealing.
- New transport/IPC — existing goroutines + channels suffice.
- Worktree isolation (filesystem-level cloning).
- Any change to foreground (non-background) execution.

---

## Architecture

```
                   setting.SubagentSettings.MaxConcurrency (default 2)
                              │
                              ▼
Executor ─── SetPool(pool) ─► Pool  (internal/subagent/pool.go)
                              │  chan struct{} semaphore
                              │  sync.Mutex   write-slot
                              │
RunBackground(req)            │
  │                           │
  ├── classify(config) ─► readOnly|writeable
  │                           │
  ├── pool.Acquire(ctx, class) ◄── blocks when at capacity
  │     readOnly: semaphore slot
  │     writeable: semaphore slot + exclusive write-lock
  │                           │
  ├── goroutine:              │
  │     executor.Run(ctx, req)│
  │     agentTask.Complete()  │
  │     pool.Release(class) ◄─┘
  │
  └── return agentTask immediately
```

**Priority rules:**
1. `req.Model` — caller override (highest)
2. `modelOverrides[name]` — settings-level override  
3. `config.Model` — agent definition
4. inherit — parent model (lowest)

---

## Decisions

| Topic | Decision |
|---|---|
| Semaphore style | Buffered channel `make(chan struct{}, n)` — idiomatic Go, no extra deps |
| Write serialization | `sync.Mutex` write-slot acquired after semaphore slot (prevents deadlock) |
| Read-only detection | `config.PermissionMode == PermissionExplore` or `AllowTools` excludes all edit-class tools |
| Pool location | `internal/subagent/pool.go` — owned by the subagent package |
| Settings key | `"subagents"` → `SubagentSettings{MaxConcurrency int}` |
| Default concurrency | 2 |
| Queuing | Acquire blocks on semaphore channel — Go scheduler does the queuing, no busy-poll |
| Failure isolation | One task failure does not cancel siblings; only parent context cancellation propagates |
| Write-capable agents | `PermissionAcceptEdits`, `PermissionBypass`, or `AllowTools` contains any edit-class tool |
| Fan-in format | Existing `AgentResult.Content` fields — no new types needed |
| System prompt | Extend agent schema description to prefer parallel fan-out |

---

## Sub-Tasks

---

### Sub-task 1 — `SubagentSettings` in `setting.Data`

**Status:** [ ] pending

**Intent:**
Add `subagents.maxConcurrency` to `settings.json` following the exact
pattern of `AdvisorSettings`.

**Expected Outcomes:**
- `settings.json` accepts `{"subagents": {"maxConcurrency": 4}}`.
- Zero value (unset) → use default of 2 at runtime.
- Settings merge coalesces integers (non-zero overlay wins).
- `Clone()` copies the struct.

**Todo List:**
1. Add `SubagentSettings` struct to `internal/setting/settings.go`:
   ```go
   // SubagentSettings controls concurrent subagent execution.
   type SubagentSettings struct {
       // MaxConcurrency caps how many subagents may run at the same time.
       // 0 means use the default (2). Negative values are treated as 0.
       MaxConcurrency int `json:"maxConcurrency,omitempty"`
   }
   // ResolvedMaxConcurrency returns the configured cap, or 2 when unset.
   func (s SubagentSettings) ResolvedMaxConcurrency() int
   ```
2. Add `Subagents SubagentSettings \`json:"subagents"\`` field to `Data`.
3. Add `mergeSubagentSettings(base, overlay SubagentSettings) SubagentSettings`
   to `merger.go` — `coalesceInt(overlay.MaxConcurrency, base.MaxConcurrency)`.
4. Wire in `mergeSettings` and `Clone()`.

**Relevant Context:**
- [`internal/setting/settings.go`](internal/setting/settings.go) — `AdvisorSettings` pattern
- [`internal/setting/merger.go`](internal/setting/merger.go) — `mergeAdvisor`, `coalesceInt`

---

### Sub-task 2 — Concurrency pool (`internal/subagent/pool.go`)

**Status:** [ ] pending

**Intent:**
Implement a lightweight semaphore + write-mutex pool. The pool is the sole
concurrency control point — the executor acquires a slot before starting and
releases it when done, regardless of success or failure.

**Read vs write:**
- **Read-only** (`explore`, `planner`, `reviewer`): acquires one semaphore slot.
  Multiple read-only agents can run up to `MaxConcurrency` at once.
- **Write-capable** (`worker`, `edit` mode, `bypass` mode, or `AllowTools` includes
  an edit-class tool): acquires one semaphore slot AND the exclusive write-lock.
  At most one write-capable agent runs at a time on the same working tree.

**Deadlock-free acquire order:** semaphore first, then write-lock. Both `Acquire`
calls respect context cancellation so a parent cancel unblocks queued work.

**Expected Outcomes:**
- `Pool` is a small struct with a buffered channel semaphore and a mutex write-slot.
- `NewPool(maxConcurrency int) *Pool` — panics if `maxConcurrency < 1`.
- `pool.Acquire(ctx context.Context, writeCapable bool) error` — blocks until a
  slot is free or ctx is done.
- `pool.Release(writeCapable bool)` — releases the slot(s). Must be called in a
  `defer` after `Acquire` succeeds.
- Concurrent read-only acquisitions succeed up to `maxConcurrency` without
  blocking each other.
- A second write-capable acquisition blocks until the first releases.
- Cancellation during `Acquire` returns `ctx.Err()` and does not consume a slot.
- A zero-value `*Pool` (nil) is treated as unlimited (no semaphore, no lock).

**Todo List:**
1. Create `internal/subagent/pool.go`:
   - `type Pool struct { slots chan struct{}; writeMu sync.Mutex }`.
   - `NewPool(n int) *Pool`.
   - `Acquire(ctx, writeCapable) error`.
   - `Release(writeCapable)`.
   - `isWriteCapable(config *AgentConfig) bool` helper (exported from pool or
     private to package).
2. Create `internal/subagent/pool_test.go` with concurrency tests (see Sub-task 5).

**Relevant Context:**
- [`internal/subagent/executor.go`](internal/subagent/executor.go) — `Executor`, `RunBackground`
- [`internal/subagent/types.go`](internal/subagent/types.go) — `PermissionMode` constants, `ToolList`
- Go `sync` package — `Mutex`, buffered channels

---

### Sub-task 3 — Wire pool into `Executor.RunBackground`

**Status:** [ ] pending

**Intent:**
Add a `pool *Pool` field to `Executor`. The pool is optional — a nil pool
means unlimited (current behaviour unchanged). When set, `RunBackground`
acquires before spawning and releases in a goroutine-deferred call.

**Priority is unchanged:** `Acquire` blocks the spawning goroutine, not the
parent agent's tool-execution turn. `RunBackground` returns the `*task.AgentTask`
immediately (before or after `Acquire` — see design note below).

**Design note — when to return the task ID:**
Return the `*AgentTask` to the caller *before* blocking on `Acquire` so the
parent can hold the task ID immediately and the queue is transparent. The
actual work starts when the slot becomes available.

```
RunBackground(req):
  → validate + resolveConfig         (fast, no blocking)
  → create AgentTask (status=queued) (registers with manager immediately)
  → spawn goroutine:
       pool.Acquire(ctx, writeCapable)   ← may wait here
       task.setRunning()
       executor.Run(ctx, req)
       pool.Release(writeCapable)
       task.Complete(err)
  → return agentTask immediately
```

This means a task can be in a "queued" state before it starts running.

**Expected Outcomes:**
- `Executor` has `pool *Pool` field.
- `SetPool(p *Pool)` method wires it.
- `RunBackground` returns `*AgentTask` immediately (before blocking on pool).
- Task transitions: `queued → running → completed|failed|stopped`.
- A `queued` task has a valid ID; `IsRunning()` returns false while queued.
- When `pool == nil`, behaviour is identical to today.
- Parent context cancellation while queued cancels the task (context propagates).

**Todo List:**
1. Add `StatusQueued TaskStatus = "queued"` to `internal/task/types.go`.
2. Add `setRunning()` transition to `AgentTask` that atomically moves
   `queued → running` (same `finalize` discipline).
3. Add `pool *Pool` field to `Executor`; add `SetPool(p *Pool)` setter.
4. Modify `RunBackground` to:
   a. Resolve config and create the task immediately (status=queued).
   b. Spawn the goroutine with `Acquire → setRunning → Run → Release`.
   c. Return task before the goroutine starts.
5. In `internal/app/agent.go` and `internal/app/run_agent.go`, create
   `NewPool(settings.Subagents.ResolvedMaxConcurrency())` and call
   `executor.SetPool(pool)` at the build site.

**Relevant Context:**
- [`internal/subagent/executor.go`](internal/subagent/executor.go) — `RunBackground`, `Executor` struct
- [`internal/task/agent_task.go`](internal/task/agent_task.go) — `finalize`, `StatusRunning`
- [`internal/task/types.go`](internal/task/types.go) — `TaskStatus` constants
- [`internal/app/agent.go`](internal/app/agent.go) — build site lines 759-777
- [`internal/app/run_agent.go`](internal/app/run_agent.go) — headless build site

---

### Sub-task 4 — System prompt for parallel-first orchestration

**Status:** [ ] pending

**Intent:**
Teach the parent agent to prefer parallel fan-out via the Agent tool schema
and the system prompt. This is purely prompt/doc change — no Go code in the
main path.

The Agent tool schema already says "Launch independent agents concurrently."
Strengthen it and add explicit fan-out guidance.

**Expected Outcomes:**
- Agent tool schema description explicitly shows the fan-out pattern and when
  to wait before synthesising.
- Advisor system prompt mentions that it can be launched asynchronously while
  the parent continues other work.
- `AGENTS.md` notes the `subagents.maxConcurrency` setting.
- `docs/guides/advisor-agent.md` async pattern section is current.

**Todo List:**
1. Update `internal/tool/agent/schema.go` — strengthen the parallel/fan-out
   guidance in `agentSchema()` without making the description excessively long.
   Include a compact fan-out example showing spawn → continue → wait.
2. Update `internal/subagent/builtin_advisor.go` `advisorWhenToUse` to note
   it can be started early while independent work continues.
3. Add `subagents.maxConcurrency` to the `AGENTS.md` "Default Agents" section
   note (or a short "Configuration" subsection).
4. No changes needed to `docs/guides/advisor-agent.md` — it already shows the
   async pattern correctly.

**Relevant Context:**
- [`internal/tool/agent/schema.go`](internal/tool/agent/schema.go) — `agentSchema()`
- [`internal/subagent/builtin_advisor.go`](internal/subagent/builtin_advisor.go) — `advisorWhenToUse`
- [`AGENTS.md`](AGENTS.md)

---

### Sub-task 5 — Concurrency tests

**Status:** [ ] pending

**Intent:**
Prove real parallel overlap, not just asynchronous scheduling. Tests must use
timing or blocking primitives to verify goroutines are genuinely concurrent.

All tests live in `internal/subagent/pool_test.go` (pool unit tests) and
`internal/subagent/pool_concurrency_test.go` (integration-level concurrency
proofs). No real LLM calls — agent execution is stubbed.

**Required test coverage:**

| Test | What it proves |
|---|---|
| `TestPool_ReadOnlyOverlap` | Two read-only tasks overlap (both hold slot simultaneously) |
| `TestPool_MaxConcurrencyEnforced` | Third task blocks until first completes |
| `TestPool_QueuedTaskStartsWhenSlotFrees` | Queued task starts automatically on release |
| `TestPool_WriteSerializes` | Second write-capable task waits for first |
| `TestPool_ReadWriteMix` | Read tasks and a write task — read tasks overlap, write waits |
| `TestPool_CancelDuringAcquire` | Context cancel while queued returns error, no slot leak |
| `TestPool_NilPoolUnlimited` | Nil pool allows unlimited concurrent runs |
| `TestRunBackground_IndependentOverlap` | Two background agents execute concurrently via executor |
| `TestRunBackground_FailureIsolation` | One failed task does not cancel siblings |
| `TestRunBackground_ParentCancelPropagates` | Cancelling parent context cancels all child tasks |
| `TestRunBackground_NoGoroutineLeak` | After completion/cancel, goroutines are released |

**Testing technique for real overlap:**
- Use a `gate := make(chan struct{})` that each fake "agent run" blocks on.
- Confirm both are blocked simultaneously before releasing the gate.
- This proves goroutines are truly concurrent, not sequential.

**Stub pattern** (following existing test conventions):
```go
// stubRun replaces Executor.Run with a function that blocks on a channel,
// simulating an in-flight LLM call without any network or model dependency.
```

Since `executor.Run` is a method, tests can either:
a. Use `internal/subagent` package-internal access (tests are `package subagent`).
b. Replace `Executor` fields with test doubles at construction.

**Relevant Context:**
- [`internal/subagent/executor_test.go`](internal/subagent/executor_test.go) — existing test patterns
- [`internal/task/bash_task_test.go`](internal/task/bash_task_test.go) — `WaitForCompletion` patterns
- [`internal/subagent/pool.go`](internal/subagent/pool.go) — new file from Sub-task 2

---

## Rollout / Dependencies

```
Sub-task 1 (settings)  ──► Sub-task 3 (wire pool) ──► Sub-task 4 (prompts)
Sub-task 2 (pool impl) ──►         ↑
                                   │
Sub-task 5 (tests)  ───────────────┘
```

Sub-tasks 1 and 2 are independent and can proceed in parallel.
Sub-task 3 depends on both 1 and 2.
Sub-task 4 and 5 can proceed after Sub-task 2.

---

## What Does NOT Change

- Foreground (non-background) execution — unchanged.
- `executor.Run` — unchanged.
- `broker` — unchanged.
- `task.Manager` — unchanged except adding `StatusQueued`.
- Dependency rules — `pool.go` stays inside `internal/subagent`.
- The four existing built-in agent conventions (`explore`, `planner`, `worker`,
  `reviewer`) — they are user-defined, nothing changes about them.
- `advisor` built-in — unchanged except `advisorWhenToUse` wording.
