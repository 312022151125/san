# Token-Efficient Runtime Plan

## Overview

Upgrade San into a more token-efficient coding-agent runtime by selectively
adopting the highest-leverage ideas from OMP2 (oh-my-pi) and Tura, while
preserving San's lightweight native-Go architecture.

**Objective:**
> OMP2-quality delegation + Tura-style low-round-trip execution + San's
> lightweight Go core.

**Primary optimization target — total task trajectory:**
```
total tokens ≈ repeated context × model round trips + useful reasoning/output
```
Optimize BOTH context size per request AND number of model requests per task.

**Hard constraints:**
- Go-native, single binary, no external runtime.
- No permanent daemon, no background indexer.
- Bounded goroutines using existing `context.Context` and channel semaphores.
- Dependency rules from `docs/reference/dependency-rules.md` must not be violated.
- All phases must pass `go test -race ./...` before advancing.

---

## Confirmed Design Decisions

These decisions were confirmed after initial plan review and lock in the
implementation choices for all sub-tasks.

### D1 — `Batch` is a separate tool from `Bash`
`Bash` remains the primitive for one shell command.
`Batch` is a distinct tool for deterministic multi-command execution / dependency graphs.
The schema separation is deliberate: the model must understand clearly when it wants
one command vs when it wants to hand an entire execution graph to the runtime.
The token overhead of one extra tool schema is accepted because it eliminates far
more tokens at the execution level.

```text
Bash("go test ./...")       ← primitive
Batch([gofmt, test, vet])   ← orchestration
```

### D2 — Plan Mode: conservative policy
- Phase 1 Agent batch: only read-only subagents allowed in Plan Mode.
  Write-capable agents in a batch are rejected while Plan Mode is active.
- Phase 2 Batch tool: rejected entirely in Plan Mode. No shell-command
  classification. Plan Mode already has Read/Grep/Glob; allowing shell through
  Batch provides little value while introducing mutation-detection complexity.
  A safe read-only shell policy can be added later if a concrete need is shown.

### D3 — Phase ordering: 1 → 2 → 3 → 4, no skipping
Priority: Task Batch → Command Batch → Context Economy → Advanced Subagents.
Benchmark after every phase. Phase 3 (Context Economy) must not be postponed
indefinitely; total token reduction, not just parallelism, is the objective.
Do not proceed to the next phase unless all tests pass and benchmarks are recorded.
Do not ask for confirmation between phases unless a benchmark shows a regression
or an architectural conflict requires a decision.

### D4 — Artifact storage: reuse `task.Manager.outputDir`
Do not create a separate artifact store, database, or lifecycle manager.
Large results and logs live under the existing task output directory.
Add a thin reference abstraction (a string type or small struct) so callers do
not depend directly on filesystem layout. No new storage subsystem.

### D5 — `Batch` is available to write-capable subagents (e.g. `worker`), not all agents
This is required to apply Tura's philosophy at every level of the tree:

```text
Main
 └─ Agent batch ───────── 1 model turn
      ├─ explore
      └─ worker
           ├─ Edit
           └─ Batch ────── 1 tool round
                ├─ gofmt
                ├─ go test
                ├─ go vet
                └─ go build
```

`Batch` is NOT parent-only. It is available to agents that have Bash permission
(i.e. write-capable agents: `worker`, or any agent with `PermissionAcceptEdits`
or `PermissionBypass`). It is excluded from `explore`, `advisor`, and `reviewer`
by default (those agents' `AllowTools` lists do not include `Batch`).

`Batch` never bypasses permissions: every command executes with the same effective
capability and policy as the calling agent. Plan Mode still rejects it.

The tool-set filtering rule changes from "parent-only" to "permission-gated":
- `Batch` is in the default tool set for agents with Bash access.
- `Batch` is absent from the schema for agents whose `AllowTools` excludes `Bash`
  or who have `PermissionExplore`.

The critical design distinction must be preserved in all prompts and docs:
```text
Agent tasks[] = parallel semantic reasoning = multiple model children
Batch         = deterministic command DAG   = zero model reasoning between steps
```


---

## Feature Mapping

| Feature | Source | Problem solved | Token/latency benefit | San equivalent | Decision | Proposed implementation |
|---|---|---|---|---|---|---|
| Task Batch (`context` + `tasks[]`) | OMP2 | Multiple sequential Agent calls for independent work | Eliminates N-1 spawn round trips; shared context sent once not N times | Agent tool (one call each) | **Keep + Extend** | New `tasks[]` param on existing Agent tool; shared `context` field |
| Clean child sessions | OMP2 | Parent history leaks into child, inflating input tokens | Children pay only for task-specific context | Agent already does this | **Already done** | Document and strengthen via agent system prompt |
| Structured yield / compact result | OMP2 | Child transcripts returned verbatim inflate parent context | Parent sees compact JSON, not raw transcript | `formatForegroundAgentResult` (partial) | **Improve** | Structured `AgentYield` result type; summary+findings+files+changes+tests+risks |
| Artifact references for large outputs | OMP2 | Large child output inlined in parent context | Reference `agent://id` replaces megabytes of inlined output | None | **Add** | Store large results as files; return reference path |
| Spawn preflight (validate before model cost) | OMP2 | Invalid spawn configs waste one model turn | Batch rejected atomically before any agent starts | `PreparePermission` per-call | **Extend** | Validate entire batch before spawning any child |
| IDs allocated before execution | OMP2 | Race between reporting and completion | Deterministic reporting | Task ID after start | **Add** | Pre-allocate IDs in batch handler |
| Command Batch (`commands[]` + `depends_on`) | Tura | Multiple sequential Bash→MODEL→Bash cycles | N shell commands = 1 tool call + 1 model re-entry instead of N | Bash (one command) | **Add** | New `Batch` tool (or upgrade Bash) with `commands[]` and `depends_on` |
| Step-based concurrency for commands | Tura | Sequential commands that could run in parallel | Independent commands run in parallel, reducing wall time | None | **Add** | Inside command batch executor: parallel goroutines per independent set |
| Fail-fast / skip propagation | Tura | Dependent command runs after prerequisite fails | Avoid wasting time on commands doomed to fail | None | **Add** | Dependency graph traversal; mark skipped when parent failed |
| Compact command result | Tura | Full stdout of `go test` fills context | Only exit code, duration, errors, relevant tail returned | Bash result (raw) | **Improve** | Deterministic truncation: head + errors + tail; store full log to file |
| Semantic boundary — no re-entry for deterministic work | Tura | MODEL called after each successful command | Each success = wasted round trip | None | **Core principle** | One `Batch` tool call handles entire DAG; model re-enters only at boundary |
| Backward-reasoning prompt heuristic | Tura | Forward drift → wrong changes | Better change planning | None | **Add to prompt** | Add 4-line planning heuristic to behavior.txt |
| Completion discipline | Tura | Premature done claims | Catches scope gaps before claiming done | None | **Add to prompt** | Add lightweight audit check to rules.txt |
| Deterministic tool-result pruning | Tura/OMP2 | Old Read/Grep/Bash results stay in context forever | Reduce input token cost for long sessions | Auto-compaction (coarse) | **Add** | Per-tool deterministic pruning before compaction |
| Round-trip budget metrics | Both | No visibility into what optimizations save | Enables measurement of improvements | None | **Add** | Per-turn metrics struct; exported in debug mode |
| Dynamic schemas (disabled features = zero schema tokens) | OMP2 | Tool schemas sent even when feature is disabled | Reduce base prompt size | ExtraTools pattern (partial) | **Extend** | `Batch` tool omitted from schema when batch feature is disabled |
| Plan Mode — enforce read-only in task batch | OMP2 | Planning tasks write when they shouldn't | Wrong changes during plan phase | Setting exists; not enforced in batch | **Add** | Plan Mode flag propagated into batch; mutating commands rejected |
| Worktree isolation for parallel writers | OMP2 | Concurrent write agents corrupt shared tree | Multiple workers safe | `max_writers` semaphore (prevents >1) | **Skip Phase 1, Phase 4** | Phase 4 only; use `max_writers=1` for now |
| Agent steering/resume | OMP2 | Re-discovering context on every re-spawn | Avoid repeated repo discovery | `SendMessage` (partial) | **Skip Phase 1** | Phase 4; `SendMessage` covers urgent cases today |
| Full Agent Hub / IRC subsystem | OMP2 | Complex agent registry | — | Not needed | **Skip** | Too complex for San's goals |
| Full Tura scheduler / calendar | Tura | — | — | Not needed | **Skip** | Not San's use case |
| Tura persona system / GUI / session DB rewrite | Tura | — | — | Not needed | **Skip** | Out of scope |

---

## Architecture Target

```
                         SAN
                          │
                    MODEL DECISION
                          │
            ┌─────────────┴─────────────┐
            │                           │
      semantic work              deterministic work
            │                           │
         AGENT                        BATCH
          tasks[]                      TOOL
            │                           │
     ┌──────┼──────┐              ┌─────┼─────┐
     ↓      ↓      ↓              ↓     ↓     ↓
 explore  worker advisor        cmd A cmd B cmd C
     │      │      │              │     │     │
     │      │      │              └──┬──┘     │
     │      │                  dependency graph
     │      ↓                        │
     │  [Edit]                compact result
     │      │
     │      ↓
     │  BATCH tool  ◄── write-capable agents only (D5)
     │  ├─ gofmt        Plan Mode rejects (D2)
     │  ├─ go test
     │  ├─ go vet
     │  └─ go build
     │      │
     │  compact result
     ↓      ↓
structured yield
     └──────┬──────┘
            └─────────────┬────────────┘
                          ↓
                  SEMANTIC BOUNDARY
                          │
                          ↓
                    MODEL DECISION
```

Token savings happen at BOTH levels:
- Main → fewer spawn turns (Agent tasks[])
- Worker → fewer tool turns (Batch)

---

## Implementation Phases

Do NOT begin a phase unless the previous phase passes all tests.

---

## Sub-Task 1 — OMP2-Style Agent Batch (Phase 1)

**Status:** `[ ] pending`

**Intent**
Upgrade the existing `Agent` tool to support a batch invocation mode: one tool
call that fans out N independent subagents concurrently with a shared context
block, allocates IDs before execution, validates the batch preflight atomically,
and returns compact structured results. This eliminates N-1 extra model round
trips when the model needs multiple independent agents.

**Problem it solves:**
Today the model must call `Agent` → wait → `Agent` → wait → `Agent` for N
independent tasks. With batch, it issues one call, the runtime fans out, and it
receives one compact combined result.

**Expected Outcomes**
- `Agent` tool accepts optional `tasks[]` array + `context` string.
- When `tasks[]` is present, the entire batch is validated (preflight) before
  any agent starts. If validation fails, the whole batch is rejected.
- IDs are allocated before the first goroutine starts.
- Independent tasks run concurrently, respecting `concurrencySem` and
  `writerSem` from `subagent.Executor`.
- Each child session receives: `agent instructions + shared context + individual
  task`. It does NOT receive parent conversation history.
- Results are returned as a compact structured yield:
  `{summary, findings, files, changes, tests, risks}` per agent.
- Large individual results are stored to a file; the model receives a reference.
- The tool schema exposes `tasks[]` only when batch feature is enabled (dynamic
  schema).
- All tests pass including `go test -race ./...`.
- Benchmark Scenario B (see § Benchmarks) can be measured before/after.

**Todo List**
1. **Define `AgentYield` type** in `internal/tool/agent.go`:
   ```
   type AgentYield struct {
       Summary  string
       Findings []string
       Files    []string
       Changes  []string
       Tests    []string
       Risks    []string
   }
   ```
   Export from `internal/tool` so the executor can produce it.

2. **Extend `AgentExecResult`** in `internal/tool/agent.go`:
   - Add `Yield *AgentYield` field.
   - Add `ResultRef string` field (non-empty when result stored to file, value
     is a reference like `agent://<id>`).

3. **Add `AgentBatchRequest` + `AgentBatchResult`** in `internal/tool/agent.go`:
   ```
   type AgentBatchItem struct {
       ID    string  // pre-allocated
       Name  string
       Agent string
       Task  string
       Mode  string
   }
   type AgentBatchRequest struct {
       Context string
       Tasks   []AgentBatchItem
   }
   type AgentBatchResult struct {
       Results []AgentExecResult
       Duration time.Duration
   }
   ```

4. **Extend `AgentExecutor` interface** in `internal/tool/agent.go`:
   - Add `RunBatch(ctx context.Context, req AgentBatchRequest) (*AgentBatchResult, error)`.
   - Add `AllocateAgentID() string` for pre-allocation.

5. **Implement `RunBatch` in `internal/subagent/executor.go`** (new file
   `executor_batch.go`):
   - Validate all items upfront (preflight): resolve agent config, check spawn
     permission, validate mode. If any fails, return error before starting any
     goroutine.
   - Pre-allocate IDs for all items.
   - Fan out items concurrently using `errgroup` or `sync.WaitGroup`, acquiring
     `concurrencySem` and `writerSem` for each child.
   - Each child receives: `agent system prompt + shared context + individual task`.
     No parent history.
   - Collect `AgentExecResult` per item; build `AgentBatchResult`.
   - If individual result is larger than a configurable threshold (e.g. 8KB),
     store to a temp file in task output dir and set `ResultRef`.

6. **Update `AgentTool.execute` in `internal/tool/agent/agent.go`**:
   - Detect `tasks[]` in params.
   - If present, parse into `AgentBatchRequest` and call `executor.RunBatch`.
   - Format compact combined result for model.

7. **Update `agentSchema`** in `internal/tool/agent/schema.go`:
   - Add `tasks` property (array of task objects) and `context` string to
     schema, guarded by a `SchemaOptions.BatchEnabled bool` flag.
   - When `BatchEnabled = false`, `tasks` and `context` are absent from schema
     — zero overhead.

8. **Update `result.go`** in `internal/tool/agent/`:
   - `formatBatchAgentResult`: compact per-agent lines + summary. Do NOT include
     activity trace in parent context. Include `ResultRef` when present.

9. **Plan Mode enforcement in Agent batch** (D2): each `AgentBatchItem` carries
   an effective `PermissionMode` resolved at preflight. If Plan Mode is active
   (checked via `PlanModeChecker`), reject any item whose resolved mode is
   write-capable (`PermissionAcceptEdits`, `PermissionBypass`, `PermissionDontAsk`).
   Only `PermissionExplore` items are allowed. If any item fails this check, the
   entire batch is rejected atomically before any goroutine starts.
   Error: `"batch contains write-capable agents; only explore mode is allowed in Plan Mode"`.

10. **Large result storage** (D4): `RunBatch` receives `outputDir string`
    from the executor. When a single agent result exceeds 8KB, write it to
    `<outputDir>/<agentID>.md` and set `ResultRef = "agent://<agentID>"` in
    `AgentExecResult`. The `ResultRef` string is the only coupling to storage —
    no new storage layer.

11. **Add batch tests** in `internal/tool/agent/agent_batch_test.go`:
    - Shared context propagated to each child.
    - Multiple children run concurrently (timing-verified).
    - Preflight fails atomically (no child started if any item invalid).
    - Concurrency limit respected.
    - Writer limit respected.
    - Large results stored under task output dir; `ResultRef` returned.
    - Structured yield is compact; no raw activity trace leaked.
    - Context cancellation terminates running children.
    - Plan Mode: write-capable items rejected; explore items allowed.
    - Plan Mode: batch with mixed items rejected atomically.

12. **Update Agent schema description** to guide the model toward batch:
    ```
    Use tasks[] for genuinely independent semantic work in one call.
    Provide shared context once; each task contains only its specific delta.
    Do not batch tasks that depend on each other's semantic results — sequence those.
    tasks[] = parallel reasoning. Use Batch tool for deterministic commands.
    ```

13. Run `go test -race ./internal/tool/... ./internal/subagent/...` — must pass.

**Relevant Context**
- `internal/tool/agent/agent.go` — existing single-agent tool implementation.
- `internal/tool/agent/schema.go` — existing schema; add `tasks[]` here.
- `internal/tool/agent.go` — `AgentExecutor` interface and `AgentExecRequest/Result` types.
- `internal/subagent/executor.go` — `Executor` struct, semaphores, `Run()`.
- `internal/subagent/executor_run.go` — child session construction.
- `internal/task/manager.go` — `outputDir` for large result storage (D4).
- `internal/tool/set.go:24-32` — parent-only tools excluded from subagent.
- `docs/reference/dependency-rules.md` — feature packages must not import app.
- D2: `PlanModeChecker` interface — define in `internal/tool/batch/` or a shared
  `internal/tool/planmode/` package to avoid circular imports between `agent/`
  and `batch/`.

---

## Sub-Task 2 — Tura-Style Command Batch Tool (Phase 2)

**Status:** `[ ] pending`

**Intent**
Add a new `Batch` tool — separate from both `Agent` and `Bash` (D1) — that
accepts a `commands[]` array with optional `depends_on` fields, executes
independent commands concurrently (up to `max_parallel`), propagates fail-fast
skip for dependent commands, and returns one compact result without returning
to the model in between. Implements Tura's "runtime handles the DAG, model
re-enters only at semantic boundary" principle.

`Batch` is NOT parent-only (D5). It is available to write-capable agents
(`worker`, any agent with `PermissionAcceptEdits` or `PermissionBypass`) so
that the full tree — Main → Task batch → worker → Batch — benefits from the
optimization. It is absent for `explore`, `advisor`, and `reviewer` (those
agents do not have `Bash` access and therefore cannot use `Batch`). Plan Mode
rejects `Batch` entirely (D2) — no command classification is attempted.

**Problem it solves:**
Today `go test`, `go vet`, `go build` each require MODEL→Bash→MODEL→Bash→MODEL
cycles whether called from Main or from a worker subagent. With `Batch`, all
three execute in one call with one compact return — at every level of the tree.

**Expected Outcomes**
- New `Batch` tool registered in `internal/tool/batch/`.
- `Batch` is NOT in the parent-only list. It IS excluded from `explore`,
  `advisor`, and `reviewer` via their `AllowTools` (those lists don't include
  `Bash` or `Batch`). Write-capable agents get it automatically.
- `BatchTool` holds a `PlanModeChecker` interface. When plan mode is active,
  `Execute` returns an error immediately — no command runs (D2).
- Schema: `{commands: [{id, command, depends_on?, timeout?}]}`. Kept small.
- Hard limits: `max_commands=8`, `max_parallel=4`, per-command `max_output_bytes`.
- Commands without `depends_on` or with all deps satisfied run concurrently.
- Commands whose dependency failed are marked `skipped`.
- Result: compact block per command — `id`, `exit`, `duration`, relevant output.
- Full stdout stored under `task.Manager.outputDir` (D4); model receives a
  `log:<path>` reference for large outputs. No separate artifact store.
- Timeout per command; overall batch timeout.
- All context cancellation paths work correctly (`go test -race`).
- Benchmark Scenario C measured before/after.

**Todo List**
1. **Create `internal/tool/batch/` package** with:
   - `batch.go` — `BatchTool` struct, `Name()="Batch"`, `Execute()`,
     `PlanModeChecker` interface field.
   - `schema.go` — schema definition with `commands[]` array (kept small).
   - `executor.go` — DAG builder and concurrent executor.
   - `result.go` — compact result formatter.

2. **Define command types** in `batch.go`:
   ```go
   type BatchCommand struct {
       ID        string
       Command   string
       DependsOn []string
       Timeout   time.Duration  // 0 = default (120s)
   }
   // ArtifactRef is a thin reference to a stored log file (D4).
   // Format: "log:<absolute-path>" — callers must not parse the path.
   type ArtifactRef string
   type CommandResult struct {
       ID         string
       ExitCode   int
       Duration   time.Duration
       Output     string      // compacted inline output
       LogRef     ArtifactRef // non-empty when output was stored to file
       Skipped    bool
       SkipReason string
   }
   ```

3. **Implement `PlanModeChecker` interface** in `batch.go`:
   ```go
   type PlanModeChecker interface {
       IsPlanMode() bool
   }
   ```
   `BatchTool.Execute` checks this first; if `IsPlanMode()` returns true,
   return an error: `"Batch is not available in Plan Mode"`. (D2)

4. **Implement DAG executor** in `executor.go`:
   - Build adjacency map from `depends_on`.
   - Detect cycles — return error before starting any command.
   - Topo-sort into execution waves: wave N contains all commands whose deps
     are in waves 0..N-1.
   - Execute each wave concurrently up to `max_parallel` using a `chan struct{}`
     semaphore.
   - After each wave: if any required dependency failed, mark dependents `skipped`.
   - Context cancellation: `context.WithTimeout` per command; parent ctx cancel
     propagates to all running commands.
   - Log storage: when raw output > 4KB, write to
     `<task.Manager.outputDir>/<batchID>-<cmdID>.log` and return `ArtifactRef`.
     The `BatchTool` receives `outputDir string` at construction from the app layer
     (same wiring pattern as other tools that use `task.Manager`). (D4)

5. **Implement compact result formatter** in `result.go`:
   - Success: `id\n  exit: 0\n  duration: 1.2s\n` (minimal, no stdout).
   - Failure: `id\n  exit: 1\n  duration: 3.4s\n  errors:\n    <relevant lines>\n`.
   - Skipped: `id\n  skipped: <reason>\n`.
   - Relevant lines = lines matching `(?i)error:|FAIL|panic:|\.go:\d+`, plus
     last 5 lines. Collapse consecutive identical lines.
   - If raw output > 4KB: inline only the relevant extract + `log: <ArtifactRef>`.

6. **Implement output deduplication**:
   - `collapseRuns(lines []string) []string` — merge N identical consecutive
     lines into `<first line> (×N)`.
   - Cap total inline output per command at `MaxInlineBytes = 8192`.

7. **Add `Batch` to `builtinToolOrder`** in `internal/tool/schema.go`:
   - Insert after `ToolBash`.
   - Add `ToolBatch = "Batch"` constant.

8. **Do NOT add `Batch` to the parent-only list** in `internal/tool/set.go` (D5).
   Instead, its presence in a subagent's tool set is governed by whether that
   agent has `Bash` access (same permission gate). The tool's own `PlanModeChecker`
   enforces the Plan Mode restriction at execution time, not at schema-filter time.
   Document this in a comment in `set.go` near the parent-only list.

9. **Register tool** via `init()` in `batch.go` and blank-import in
   `internal/tool/register/all.go`.

10. **Wire `PlanModeChecker` and `outputDir`** from the app layer:
    - In `internal/app/agent.go` (where other tool executors are wired), find
      the registered `BatchTool` and call `SetPlanModeChecker(checker)` and
      `SetOutputDir(dir)`.
    - This follows the same pattern as `AgentTool.SetExecutor`.

11. **Write tests** in `batch/batch_test.go`:
    - Single command: correct exit, duration, compact output.
    - Parallel independent commands: verify true concurrency via wall-clock timing.
    - Dependency chain: A→B→C executes sequentially.
    - Fan-out: A→{B,C} — B and C run in parallel after A.
    - Fail-fast: A fails → B (depends on A) skipped; C (independent) continues.
    - Timeout per command: command exceeding timeout is killed, result marked failed.
    - Context cancellation: parent cancel propagates to all running commands.
    - Output bounds: >4KB writes to file, inline contains only extract + ref.
    - Deduplication: repeated identical lines collapsed.
    - Cycle detection: returns error, no command runs.
    - Plan Mode rejection: `PlanModeChecker` returning true → error, no execution.

12. Run `go test -race ./internal/tool/batch/... ./internal/tool/...` — must pass.
    Run `go build ./...` — must produce a clean binary.

**Relevant Context**
- `internal/tool/fs/bash.go` — existing single-command Bash tool: execution,
  timeout, process group kill pattern. Reuse `proc` package patterns.
- `internal/tool/set.go:24-32` — parent-only list: `Batch` is NOT added here.
- `internal/tool/schema.go:70-76` — `builtinToolOrder`: add `ToolBatch` after `ToolBash`.
- `internal/tool/register/all.go` — blank-import registration.
- `internal/task/manager.go` — `outputDir` field: same storage used for Bash task logs.
- `internal/subagent/types.go:19-46` — `PermissionMode` constants:
  `explore`/`dont_ask` agents do not have Bash, therefore cannot reach `Batch`.
- D1: `Bash` = primitive, `Batch` = orchestration. Keep schema descriptions distinct.
- D4: `ArtifactRef` is the only coupling to storage; no second store.
- D5: `worker` + write-capable agents get `Batch` automatically.

---

## Sub-Task 3 — Context Economy (Phase 3)

**Status:** `[ ] pending`

**Intent**
Implement deterministic per-tool result pruning so that old tool outputs stop
occupying active model context long after they are no longer needed. Complement
with round-trip budget metrics, large-log artifact references, and dynamic
schema suppression for disabled features. These are cost reductions that happen
transparently, without changing model behavior.

**Problem it solves:**
In a long session, prior `Read`/`Grep`/`Bash` results accumulate in context.
These are not pruned until auto-compaction at 90% of input limit — far too late.
Deterministic per-tool pruning removes them earlier, reducing input tokens
on subsequent requests.

**Expected Outcomes**
- `ResultPruner` interface in `internal/core` (or `internal/tool`) that can
  mark old tool results for compaction hint.
- Tool-specific policies implemented:
  - `Read`: after file is edited or re-read, drop old full body, retain path+range+hash.
  - `Grep/Glob`: retain query + selected hits + count; drop full listing after use.
  - `Bash`: retain command + exit status + error lines; drop full stdout on success.
  - `Agent/Batch`: retain summary + compact yield; never inline child transcript.
- Round-trip metrics struct `SessionMetrics` tracked per-session:
  `ModelCalls`, `ToolCalls`, `CommandSteps`, `SubagentCalls`,
  `InputTokens`, `OutputTokens`, `CacheTokens`, `Duration`.
  Exposed in `/debug metrics` command or similar; not injected into prompts.
- Dynamic schema: `Batch` tool schema absent when batch feature not enabled.
  Memory tools schema absent when hindsight backend not configured (already
  partially done via `ExtraTools`).
- Large artifact file references: any tool result exceeding a threshold (8KB)
  stored to `<taskDir>/<id>.log`; model receives compact summary + `log:<path>`.

**Todo List**
1. **Define `SessionMetrics` struct** in `internal/subagent/metrics.go`
   (or `internal/core/metrics.go` if multiple packages need it):
   ```go
   type SessionMetrics struct {
       ModelCalls     int
       ToolCalls      int
       CommandSteps   int
       SubagentCalls  int
       InputTokens    int
       OutputTokens   int
       CacheTokens    int
       Duration       time.Duration
   }
   ```
   Add `RecordModelCall`, `RecordToolCall`, `RecordSubagentCall`, `RecordTokens`
   methods. Thread-safe (atomic or mutex).

2. **Wire metrics into agent lifecycle**: `internal/agent/session.go` creates a
   `SessionMetrics`; `OnEvent` callback increments on `inference.responded` and
   `tool.result` events. Expose via `Session.Metrics()`.

3. **Add `/debug metrics` slash command** in `internal/command/` that prints
   current session metrics to the conversation. Does not inject into model prompt.

4. **Implement tool-result pruning policies** in `internal/tool/prune/`:
   - `PrunePolicy` interface: `ShouldPrune(toolName string, result toolresult.ToolResult, age int) bool`
   - `PruneRead`: prune Read results that have been superseded (same file read
     again or file was edited).
   - `PruneGrep/Glob`: prune after N subsequent turns.
   - `PruneBash`: prune successful full stdout after 2 turns, keep summary.
   - `PruneAgent`: never inline child transcript; enforce compact yield always.

5. **Integrate pruning into compaction pipeline**: in `internal/core/compact.go`,
   before running the LLM summarizer, run deterministic pruning pass over messages.
   Pruned tool results are replaced with their compact form. This reduces what
   the summarizer must handle, and also reduces input tokens for regular turns.

6. **Large artifact references** in Bash and Batch tools:
   - When result output > 8KB, write to `<taskOutputDir>/<id>.log`.
   - Return compact summary in tool result + `"log: <path>"` line.
   - Ensure `task.Manager` output directory is available in both contexts.

7. **Dynamic schema for Batch tool**: in `internal/tool/schema.go`, add
   `SchemaOptions.BatchEnabled bool`. When false, skip `ToolBatch` in
   `builtinToolOrder` iteration. Wire `BatchEnabled` from settings.

8. Run `go test ./...` and `go test -race ./...` — must pass. Check that metrics
   count correctly in a fake agent loop test.

**Relevant Context**
- `internal/core/compact.go` — existing compaction pipeline; pruning hooks here.
- `internal/tool/toolresult/` — `ToolResult` struct, result rendering.
- `internal/task/manager.go` — `outputDir` for large file storage.
- `internal/tool/schema.go:49-59` — `SchemaOptions`; extend with `BatchEnabled`.
- `internal/command/` — slash command registration for `/debug metrics`.

---

## Sub-Task 4 — Prompt Optimization (Phase 3, concurrent with Sub-Task 3)

**Status:** `[ ] pending`

**Intent**
Rewrite San's main coding-agent prompt to reflect runtime-enforced behaviors,
add Tura's backward-reasoning heuristic, add a lightweight completion discipline,
and add batch-aware orchestration guidance. Remove prompt instructions that are
now handled by the runtime. Keep the final prompt concise.

**Problem it solves:**
Runtime enforcement is more reliable than prompting, but the model still needs
guidance for decisions the runtime cannot make. The current prompt does not
mention batching, backward reasoning, or completion discipline. These gaps cause
unnecessary round trips and scope drift.

**Expected Outcomes**
- `behavior.txt` updated with:
  - Backward-reasoning heuristic (4 lines max).
  - Batch orchestration guidance: use `tasks[]` for independent semantic work;
    use `Batch` for deterministic command sequences.
  - Exploration discipline: Grep/Glob → targeted Read → Edit; not speculative reads.
- `rules.txt` updated with:
  - Lightweight completion discipline: before done, verify requested changes
    made + required tests run + no known failures.
- No batch-operation manual injected per-turn.
- Total system prompt token count after update is equal to or less than before.
- Existing tests for system prompt building pass.

**Todo List**
1. **Update `behavior.txt`** in `internal/core/system/prompts/`:
   - Add backward-reasoning paragraph:
     ```
     Before a non-trivial change: identify the observable end state; identify
     what must be true immediately before it; work backward to the minimal
     required changes; then execute forward.
     ```
   - Add batch guidance paragraph:
     ```
     Use tasks[] on Agent for genuinely independent semantic work — provide
     shared context once and self-contained task deltas. Use Batch for multiple
     deterministic commands that do not require reasoning between them.
     Do not delegate trivial work. Do not batch dependent tasks.
     ```
   - Add exploration discipline:
     ```
     Explore narrowly: Grep/Glob first, then targeted Read on located files.
     ```

2. **Update `rules.txt`**:
   - Add completion check (≤5 lines):
     ```
     Before claiming completion: verify the requested changes are present,
     required tests have run and passed, and no known failures remain.
     ```

3. **Remove from `behavior.txt`** any instructions now enforced by runtime:
   - Concurrency limits (runtime enforces via semaphores).
   - Spawn policy details (runtime enforces preflight).
   - Output truncation instructions (runtime does this deterministically).

4. **Check total prompt token count**: estimate token count of updated prompts.
   Target: not larger than current. If larger, trim elsewhere.

5. Run `go test ./internal/core/...` — must pass.

**Relevant Context**
- `internal/core/system/prompts/behavior.txt` — main behavior prompt.
- `internal/core/system/prompts/rules.txt` — safety + protocol rules.
- `internal/core/system/prompts/identity.txt` — minimal identity; leave as-is.
- Sub-task 1 adds batch guidance to `agentSchema` description.
- Sub-task 2 adds batch guidance to `Batch` tool schema description.
- These two + this prompt update form the complete model-facing interface.

---

## Sub-Task 5 — Advanced Subagent Features (Phase 4)

**Status:** `[ ] pending`

**Intent**
Implement worktree isolation for concurrent write workers and agent reuse/steering
for substantial follow-up work. These are Phase 4 features — do not implement
until Phases 1–3 pass all benchmarks and tests.

**Note:** This sub-task may be split or deferred based on Phase 1-3 results.
Only implement what is demonstrated to help by benchmarks.

**Expected Outcomes**
- When `max_writers > 1`, concurrent write agents run in isolated git worktrees.
- Agent steering: `SendMessage` to a running agent works correctly (already
  partially implemented); add lifecycle management to keep an agent alive for
  a configurable TTL after completion.
- Tests for isolation, concurrency, and steering.

**Todo List**
1. **Design worktree isolation API** in `internal/subagent/`:
   - `IsolationBackend` interface: `Prepare(ctx, cwd, id) (isolatedCWD string, cleanup func(), error)`.
   - `GitWorktreeBackend` implementation using `git worktree add`.
   - Activated only when `max_writers > 1` (not by default).

2. **Wire isolation into `RunBatch`**:
   - For each write-capable task in a batch, call `IsolationBackend.Prepare`.
   - Pass `isolatedCWD` as the subagent's working directory.
   - After subagent completes, merge changes back (or report diffs for user review).
   - Cleanup worktree on context cancellation or completion.

3. **Add keep-alive TTL to AgentTask**:
   - After task completes (not failed), hold `AgentTask` alive for `keepAliveTTL`
     (default 0 = no keep-alive; configurable per `AgentConfig`).
   - `RunBatch` checks if an agent with the same name is still alive before spawning
     a new one for follow-up tasks.

4. **Write tests** for isolation and keep-alive.
   - Verify two write agents do not corrupt each other's files.
   - Verify keep-alive agent receives follow-up message without re-discovering context.

**Relevant Context**
- `internal/subagent/executor.go` — `writerSem` enforces max 1 writer currently.
- `internal/task/agent_task.go` — add TTL field here.
- `docs/design/principles.md` — isolation backends belong in feature layer, not app.

---

## Sub-Task 6 — Benchmarks

**Status:** `[ ] pending`

**Intent**
Create repeatable benchmark scenarios that measure actual token and round-trip
savings from the new features. Benchmarks must be run before Phase 1 begins
(baseline) and after each phase. Claims about savings must come from San's own
numbers, not from OMP2/Tura marketing.

**Expected Outcomes**
- Benchmark harness in `internal/bench/` or `cmd/san/bench_test.go`.
- Four scenarios measured (see below).
- Before/after numbers recorded in `notes/active/token-efficient-runtime-plan.md`
  under "Benchmark Results" section.
- No phase declared complete without benchmark data.

**Benchmark Scenarios**

**Scenario A — Repository exploration (baseline single-agent):**
Task: "Find how provider/model routing works in this repository."
Measure: model calls, input tokens, output tokens, files read, duration.
Compare: before batch (sequential Agents) vs after (Agent batch with tasks[]).

**Scenario B — Independent investigation (parallel vs sequential):**
Task: "Inspect provider routing + settings + session wiring independently."
Compare: 3 sequential Agent calls vs 1 Agent batch with 3 tasks[].
Measure: model calls, total tokens, wall-clock time.

**Scenario C — Validation workflow (command batch vs individual):**
Task: `go test ./...` + `go vet ./...` + `go build ./...`
Compare: 3 separate Bash→MODEL cycles vs 1 Batch call.
Measure: model calls, tokens, wall time.

**Scenario D — Full implementation cycle:**
Task: explore → edit → format → test → vet.
Compare: baseline (sequential) vs optimized (batch where applicable).
Measure: input tokens, output tokens, cache read, model calls, tool calls,
wall time, success/failure.

**Todo List**
1. Create `internal/bench/harness.go` with a minimal `BenchmarkSession` struct
   that replays a scripted task against a real or mocked agent loop and captures
   `SessionMetrics` at completion.
2. Write Scenario A script (read-only exploration with mocked LLM responses).
3. Write Scenario B script (parallel vs sequential agent spawn comparison).
4. Write Scenario C script (command batch vs sequential Bash).
5. Write Scenario D script (full cycle).
6. Add a `cmd/san/benchmark` subcommand or `go test -bench` harness that runs
   the four scenarios and prints a comparison table.
7. Record baseline numbers (before Phase 1) in this plan file under
   "Benchmark Results".
8. Record after-Phase-1 numbers.
9. Record after-Phase-2 numbers.
10. Record after-Phase-3 numbers.

**Relevant Context**
- `internal/subagent/` — `SessionMetrics` added in Sub-Task 3.
- San's `core.Agent` interface supports replaying scripted messages via `Inbox`.
- OMP2/Tura claim 70–90% savings; do not cite these for San without San's own data.

---

## Sub-Task 7 — Documentation Updates

**Status:** `[ ] pending`

**Intent**
Update documentation to reflect the new architecture. Add new packages to
package-map, dependency-rules review, and feature docs. This is a concurrent
sub-task that should be completed alongside Sub-Tasks 1–3.

**Expected Outcomes**
- `docs/reference/package-map.md` updated to include `internal/tool/batch/`,
  `internal/tool/prune/`, `internal/bench/`.
- `AGENTS.md` updated: note that `Agent` tool supports `tasks[]` batch mode.
- `docs/packages/` entry for batch tool and command executor.
- `docs/concepts/` entry for token-efficiency design: semantic boundary concept,
  task batch vs command batch distinction.
- `notes/active/token-efficient-runtime-plan.md` updated with benchmark results
  as each phase completes.

**Todo List**
1. Update `docs/reference/package-map.md` with new packages.
2. Create `docs/packages/2-feature/batch.md` describing the Batch tool.
3. Create `docs/concepts/token-efficiency.md` describing the two-layer batching
   architecture and semantic boundary concept.
4. Update `AGENTS.md` to mention `tasks[]` batch mode in the Agent tool row.
5. Update `docs/packages/2-feature/subagent.md` to describe batch spawn.
6. Record benchmark results here as each phase completes.

---

## Benchmark Results

*(To be filled in as phases complete.)*

### Baseline (before Phase 1)
- Scenario A: TBD
- Scenario B: TBD
- Scenario C: TBD
- Scenario D: TBD

### After Phase 1 (Agent Batch)
- Scenario A: TBD
- Scenario B: TBD

### After Phase 2 (Command Batch)
- Scenario C: TBD
- Scenario D: TBD

### After Phase 3 (Context Economy)
- All scenarios: TBD

---

## Known Limitations and Risks

1. **Batch tool is parent-only**: subagents cannot use `Batch`. This is
   intentional — subagents that need parallel commands should structure their
   work differently. Revisit in Phase 4 if needed.

2. **Child result references require task output dir**: `Executor` must have
   a valid `task.Manager` output directory for file storage. Check that
   subagent sessions always have one wired.

3. **Plan Mode enforcement in Batch**: the initial heuristic (reject all shell
   commands in plan mode) is conservative. A more nuanced allowlist for
   read-only commands can be added later.

4. **Dynamic schema requires schema rebuild on settings change**: when
   `BatchEnabled` changes at runtime (settings reload), the tool schema must be
   rebuilt. Verify that San's existing schema refresh path handles this.

5. **Worktree isolation (Phase 4) is git-only**: only git repositories benefit.
   Non-git workspaces use the existing `max_writers=1` safety.

6. **Benchmark harness requires careful mock design**: to avoid LLM API costs
   in CI, the harness must support mocked LLM responses. This requires
   San's `core.Agent` to support injecting a fake client.

---

## Success Targets

- Same or better correctness vs. current San.
- Fewer model round trips per task (measurable via `SessionMetrics.ModelCalls`).
- Lower total input tokens (measurable via `SessionMetrics.InputTokens`).
- Same or lower wall-clock time for parallelizable work.
- Negligible idle overhead (no background goroutines when batch is not active).
- Binary stays single, fast-starting, low-RSS.
- All existing tests continue to pass.
- `go test -race ./...` passes after each phase.
