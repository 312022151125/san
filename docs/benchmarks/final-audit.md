# Final Audit — Token-Efficient Runtime Upgrade

> Objective: Make San closer to OMP2 in multi-agent capability while adopting
> Tura's runtime philosophy to reduce model round trips and total task tokens.

---

## Architecture change summary

```
BEFORE                              AFTER
──────────────────────────────────  ──────────────────────────────────────────
MODEL                               MODEL
 ↓ tool                              ↓ one tool call
MODEL                               runtime
 ↓ tool                              ├─ child A  ←── semantic parallelism
MODEL                               ├─ child B        (OMP2 Task Batch)
 ↓ tool                             └─ child C
MODEL                                    ↓ command graph (Tura Batch)
 ↓ bash                                  ├─ cmd 1
MODEL                                    ├─ cmd 2
 ↓ bash                                  └─ cmd 3
MODEL                                        ↓
                                    compact result
                                         ↓
                                    SEMANTIC BOUNDARY
                                         ↓
                                    MODEL
```

**Semantic parallelism:** one `Agent tasks[]` call fans out to N children that
run concurrently. The parent pays one model call, not N.

**Deterministic execution parallelism:** one `Batch` call runs N commands as a
dependency-aware DAG, returning one compact result. The model pays zero
intermediate reasoning steps for deterministic operations.

---

## Files changed / created

### New packages

| File | Description |
|------|-------------|
| `internal/tool/batch/batch.go` | `BatchTool` — DAG executor, wave-parallel execution, fail-fast skip, output compaction, artifact storage |
| `internal/tool/batch/result.go` | Compact result formatter (success=minimal, failure=errors+tail) |
| `internal/tool/batch/schema.go` | JSON schema (`commands[]`, `depends_on`, `timeout_ms`) |
| `internal/tool/batch/batch_test.go` | 26 unit/integration tests |
| `internal/subagent/executor_batch.go` | `RunBatch()` — fan-out with semaphores, Plan Mode enforcement, artifact storage |
| `internal/subagent/executor_batch_test.go` | 9 unit tests |
| `internal/core/metrics.go` | `SessionMetrics` — atomic counters for model calls, tool calls, command steps, subagent calls, tokens, duration |
| `internal/agent/build_params_test.go` | Wiring test: `BatchEnabled` propagation through `Schemas()` |
| `docs/concepts/token-efficiency.md` | Full design documentation |
| `docs/benchmarks/token-efficiency-methodology.md` | Benchmark scenarios and methodology |

### Modified files

| File | Change |
|------|--------|
| `internal/tool/agent.go` | `PlanModeChecker`, `AgentYield`, `AgentBatchItem/Request/Result`, `RunBatch` in `AgentExecutor` interface |
| `internal/tool/types.go` | `BatchAwareTool` interface |
| `internal/tool/schema.go` | `ToolBatch` constant, `BatchEnabled` in `SchemaOptions`, `BatchAwareTool` dispatch |
| `internal/tool/set.go` | `BatchEnabled` field, thread through `defaultTools()` / `agentAllTools()` |
| `internal/tool/register/all.go` | Blank import for `tool/batch` |
| `internal/tool/schema_registry_test.go` | `ToolBatch` in builtin order test |
| `internal/tool/agent/agent.go` | `executeBatch()`, `tasks[]` detection |
| `internal/tool/agent/schema.go` | `BatchAwareTool` impl, `SchemaWithOptions()`, `agentSchema(dir, batchEnabled)` |
| `internal/tool/agent/result.go` | `formatBatchResult()` |
| `internal/tool/agent/schema_test.go` | Updated for new `agentSchema` signature + batch tests |
| `internal/tool/agent/agent_test.go` | `RunBatch()` added to `recordingExecutor` |
| `internal/tool/agent/agent_batch_test.go` | 8 batch routing / output / error-case tests |
| `internal/subagent/adapter.go` | `SetOutputDir`, `SetPlanModeChecker`, `RunBatch` delegation |
| `internal/subagent/executor.go` | `newAgentToolSet` + `batchEnabled` arg; call site updated |
| `internal/subagent/executor_test.go` | Updated 3 `newAgentToolSet` call sites (added `false` arg) |
| `internal/subagent/builtin_advisor_test.go` | Updated 1 `newAgentToolSet` call site |
| `internal/agent/build.go` | `BatchEnabled bool` in `BuildParams`; threaded through `Schemas()` |
| `internal/agent/session.go` | `metrics *core.SessionMetrics`, `Metrics()`, `chainOnEvent()`, `metricsObserver()` |
| `internal/task/manager.go` | `OutputDir()` getter |
| `internal/task/output_store_test.go` | `TestManagerOutputDir` test |
| `internal/app/agent.go` | `BatchEnabled: true` in `promptParams()`; `sessionPlanModeChecker` type; wired `SetOutputDir` + `SetPlanModeChecker` on adapter + batch tool in `ReconfigureAgentTool()` |
| `internal/app/env_test.go` | `TestSessionPlanModeCheckerReflectsReadOnlyMode` |
| `internal/app/update_command.go` | `GetMetrics` callback wired |
| `internal/app/input/slash_command.go` | `GetMetrics` field + `handleDebugCommand` |
| `internal/command/registry.go` | `"debug"` command (hidden) |
| `internal/core/system/prompts/behavior.txt` | Backward-reasoning heuristic, batch guidance, narrow exploration discipline |
| `internal/core/system/prompts/rules.txt` | Completion discipline section |
| `AGENTS.md` | "Token-Efficient Orchestration" section |
| `docs/reference/package-map.md` | `tool/batch` entry, `core` metrics note |

---

## Architecture invariants (all maintained)

| Invariant | Status |
|-----------|--------|
| Go-native, single binary | ✅ no new external dependencies |
| No permanent daemon | ✅ all batch execution is request-scoped |
| No background indexer | ✅ |
| Bounded goroutines | ✅ `concurrencySem` + `writerSem` in executor; `MaxParallel` channel semaphore in batch |
| `go test ./...` passes | ✅ 70 packages |
| `go test -race` clean | ✅ all changed packages |
| `go build ./...` clean | ✅ |
| Dependency-rules.md respected | ✅ app → subagent/tool/agent/core only; no reverse imports |

---

## Token-efficiency mechanisms

### 1. OMP2 Task Batch — `Agent tasks[]`

**Problem solved:** N independent semantic investigations = N sequential model turns.

**Mechanism:**
- Parent sends one `Agent` call with `tasks[]` + shared `context`
- `RunBatch()` fans out to N children concurrently (bounded by `concurrencySem`)
- Each child gets: agent instructions + shared context + individual task (no parent history)
- Large results stored to `<outputDir>/<id>.md` → returned as `agent://<id>` reference
- Parent receives compact `AgentYield` objects

**Token savings:**
- Shared context transmitted once instead of N times
- Parent history never copied to children
- Parallel execution: wall time ≈ max(child) instead of Σ(children)
- One model turn instead of N for independent work

### 2. Tura Command Batch — `Batch commands[]`

**Problem solved:** N sequential deterministic shell operations = N model turns with
no semantic content between them.

**Mechanism:**
- Model sends one `Batch` call with `commands[]` + optional `depends_on`
- `buildWaves()` topological sort → execute each wave concurrently (bounded by `MaxParallel=4`)
- Fail-fast: dependent commands skipped if dependency failed
- Output compaction: error lines + tail kept, success output minimized
- Large output stored to `<outputDir>/batch-<id>.log` → inline extract only

**Token savings:**
- N commands → 1 model turn instead of N
- Successful command output minimized (not dumped into context)
- Failure output filtered to relevant lines only
- Consecutive duplicate lines collapsed

### 3. Dynamic schemas — zero overhead when disabled

- `BatchEnabled=false` → Agent schema has no `tasks[]` / `context` fields
- Disabled tools → zero schema tokens
- Agent batch: `explore`/`advisor`/`reviewer` agents never receive `tasks[]` (read-only mode)

### 4. Session metrics — measure, don't guess

`/debug metrics` reports the full trajectory:
- model calls, tool calls, command steps, subagent calls
- input/cache/output tokens, duration

### 5. Plan Mode enforcement (runtime, not prompt)

- `ModeReadOnly` → `sessionPlanModeChecker.IsPlanMode() = true`
- Batch tool: rejected entirely (no command classification)
- Agent RunBatch: write-capable agents rejected; explore-only batches allowed
- Enforced in Go, not in prompt instructions

### 6. Prompt improvements

Added to `behavior.txt`:
- Backward-reasoning heuristic (identify end state → work backward → execute forward)
- Narrow exploration discipline (Glob/Grep first, targeted Read, not speculative reads)
- Batch orchestration guidance (Agent `tasks[]` for semantic work, Batch for deterministic)

Added to `rules.txt`:
- Completion discipline: verify requested files changed + tests run + no failures

---

## What was deliberately NOT ported

From OMP2:
- Full Agent Hub / IRC-style subsystem
- Complex isolation backends / plugin marketplace discovery  
- Prewalk / autoloadSkills
- Deep recursive swarm

From Tura:
- Full task scheduler / calendar scheduling
- Persona system / GUI
- Full operation-manual framework
- Full command DSL / provider architecture rewrite
- Session database rewrite

---

## Known limitations and future work

### Phase 4 (deferred) — Worktree isolation + agent reuse

When `max_writers > 1`, concurrent write-capable agents currently share the
same working directory. This is safe only if they touch different files.

Future: `IsolationBackend` interface + `GitWorktreeBackend` using `git worktree add`.
Activate only when `max_writers > 1`.

### Benchmark numbers

Actual LLM-call reduction numbers require real provider sessions.
The methodology is documented in `docs/benchmarks/token-efficiency-methodology.md`.
Use `/debug metrics` to measure before/after for any given task.

### Agent reuse (steering)

Currently each batch item creates a fresh child session.
Future: `AgentTask.Steer()` to resume an existing child when it has relevant
context, avoiding repeated repository discovery overhead.

---

## Test count

| Package | Tests |
|---------|-------|
| `internal/tool/batch` | 26 |
| `internal/tool/agent` (including batch) | 8 new + existing |
| `internal/subagent` | 9 new + existing |
| `internal/agent` | 2 new (build_params_test.go) |
| `internal/task` | 1 new (OutputDir) |
| `internal/app` | 1 new (sessionPlanModeChecker) |

Total new tests: **47+**

---

## Final verification

```
go build ./...    ✅ clean
go test ./...     ✅ 70 packages pass
go test -race ... ✅ all changed packages clean
```
