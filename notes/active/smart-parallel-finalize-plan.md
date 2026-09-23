# Smart Parallel Finalization Plan

## Goal

Reduce end-to-end task completion latency from the last implementation edit to
a trustworthy final answer. The primary lever is collapsing the model's current
habit of running test → model → vet → model → build → model → reviewer →
model into a single semantic round trip via a runtime-orchestrated finalization
pipeline.

## Design Constraints

- Go-native. No external scheduler, no daemon, no database.
- **Reuse the Batch execution engine** via a small exported `BatchExecutor`
  interface. Do NOT let `internal/finalize` call `exec.Command` directly.
  Do NOT duplicate wave scheduling, concurrency, timeout, cancellation, or
  artifact handling.
- `BatchTool` (model-facing, requires permission) and `FinalizeTool`
  (trusted runtime, no model-supplied shell strings) both delegate to the
  same `BatchExecutor` engine.
- **Finalize is NOT a permission bypass.** Commands flowing through the
  `BatchExecutor` from `FinalizePipeline` must be constructed by trusted
  Finalize runtime logic only. If a model supplies arbitrary shell strings
  they must still go through the normal Batch/Bash permission path.
- Reuse `internal/subagent` (`Executor.RunBackground`) for the reviewer
  invocation.
- Reuse `internal/task.Manager` / `outputDir` for artifact storage.
- Reuse `internal/setting` conventions: camelCase JSON keys, tri-state
  bools, `Validate()`, `Resolved*()` helpers.
- `internal/core` must stay dependency-light.
- Feature packages must not import `internal/app`.
- Layer direction: cmd → app → feature → core → infrastructure.

## Architecture

```
                     Batch Engine
                   /              \
                  /                \
         BatchTool               FinalizeTool
            │                        │
     user permission          trusted verification
     check on model-           commands constructed
     supplied commands         by Finalize runtime
            │                        │
            └─────────────┬──────────┘
                          ↓
                   BatchExecutor
                          ↓
              DAG / wave scheduling
              concurrency / semaphore
              timeout / cancellation
              output bounds / artifacts
              dependency fail/skip
```

### Finalization flow

```
worker agent finishes implementation
           ↓
      Finalize tool call  (plan mode → reject)
           ↓
   FinalizePipeline.Run(ctx, req)
           ↓
   1. DetectChangedFiles(cwd)     — git diff --name-only HEAD
   2. ExtractGoPackages(files)    — map paths to ./pkg patterns
   3. ClassifyTier(files)         — FAST / STANDARD / FULL (no model call)
   4. BuildDAG(tier, packages)    — []BatchCommand with correct depends_on
   5. LaunchReviewer(...)         — RunBackground (concurrent, not blocked by DAG)
   6. BatchExecutor.Execute(dag)  — parallel wave execution
   7. Await reviewer result
   8. Aggregate → compact VerificationResult
           ↓
      PASS → finish immediately
      FAIL → compact actionable report + artifact references
```

## New Package: `internal/finalize`

Feature-layer package. Wired by `cmd/san` / `internal/app` (composition
layer). Does not import `internal/app`.

## Exported `BatchExecutor` interface (in `internal/tool/batch`)

```go
// Request is the input to a BatchExecutor run.
type Request struct {
    Commands  []BatchCommand
    CWD       string
    OutputDir string
}

// Executor executes a deterministic command DAG.
// Both BatchTool and FinalizePipeline use this interface.
type Executor interface {
    Execute(ctx context.Context, req Request) (*BatchResult, error)
}
```

`BatchTool` stores an `Executor` internally; its `Execute()` method does the
permission check then delegates to the executor.

`FinalizePipeline` accepts an `Executor` at construction time. The composition
layer (`cmd/san`) wires the same concrete executor to both.

## New Files

| File | Purpose |
| --- | --- |
| `internal/tool/batch/executor.go` | `Executor` interface + `NewEngine()` constructor for the concrete engine |
| `internal/finalize/pipeline.go` | `Pipeline` type, `Run()` entry point |
| `internal/finalize/scope.go` | Changed-file detection, Go package extraction |
| `internal/finalize/tier.go` | Tier classifier (FAST/STANDARD/FULL) + risky-path table |
| `internal/finalize/dag.go` | Verification DAG builder → `[]batch.BatchCommand` |
| `internal/finalize/reviewer.go` | Reviewer integration: compact input, 16 KiB diff bound, async launch |
| `internal/finalize/aggregate.go` | Result aggregation, compact output, artifact storage |
| `internal/finalize/cache.go` | Session-scoped verification cache (Phase 2) |
| `internal/finalize/incremental.go` | Incremental re-verify state tracker (Phase 2) |
| `internal/finalize/metrics.go` | Internal timing/savings metrics (Phase 3) |
| `internal/finalize/pipeline_test.go` | Correctness tests: concurrency, invariants, PASS/FAIL |
| `internal/finalize/scope_test.go` | Package detection, risky-path classification |
| `internal/finalize/tier_test.go` | Tier selection, escalation, reviewer skip rules |
| `internal/finalize/cache_test.go` | Cache hit, miss, invalidation (Phase 2) |
| `internal/finalize/dag_test.go` | DAG shape, dependency order, scope narrowing |
| `internal/tool/finalize/tool.go` | `FinalizeTool` — wraps `Pipeline` as a tool call |
| `internal/tool/register/` | Add blank-import for `internal/tool/finalize` |

## Settings Addition (`internal/setting/settings.go`)

```json
"verification": {
  "mode": "smart",
  "parallel": true,
  "maxConcurrency": 4,
  "affectedFirst": true,
  "incremental": true,
  "cache": true,
  "reviewer": "smart"
}
```

| Field | Values | Default |
| --- | --- | --- |
| `mode` | `"smart"` \| `"fast"` \| `"standard"` \| `"full"` | `"smart"` |
| `parallel` | bool | `true` |
| `maxConcurrency` | int | `4` |
| `affectedFirst` | bool | `true` |
| `incremental` | bool | `true` |
| `cache` | bool | `true` |
| `reviewer` | `"smart"` \| `"always"` \| `"off"` | `"smart"` |

Follow the `SubagentSettings` pattern: `Validate()`, `Resolved*()` helpers,
`omitempty` JSON tags, pointer only for tri-state booleans.

## Verification Tiers

| Tier | Checks |
| --- | --- |
| FAST | `gofmt -l` on changed files; `go build ./affected-pkg` compile-check only |
| STANDARD | affected `go test ./pkg`, affected `go vet ./pkg`, `go build ./...`, reviewer (when needed) |
| FULL | `go test ./...`, `go vet ./...`, `go build ./...`, reviewer |

### Smart Tier Classifier (deterministic, no model call)

| Change pattern | Tier |
| --- | --- |
| Comments / doc strings only | FAST |
| Single-package `_test.go` only | STANDARD affected |
| Single-package implementation | STANDARD affected |
| Files in > 2 packages | FULL |
| `go.mod` / `go.sum` | FULL |
| `internal/core/` | FULL |
| `internal/session/` | FULL |
| `internal/subagent/` | FULL |
| `internal/llm/` | FULL |
| `internal/tool/batch/` | FULL |
| Build config (`Makefile`, `.goreleaser.yml`, `Dockerfile`, etc.) | FULL |
| Any file matching `*_interface.go` or `types.go` in a shared package | FULL |

Rules are a simple path-prefix / filename check. Auditable in `tier.go`.

### Reviewer-needed classifier

In `"smart"` mode, skip reviewer for:
- FAST tier changes (formatting / comments only)
- Test-only changes to a single package with no new exported symbols

Run reviewer for:
- Logic changes in non-test files
- Any file in `internal/subagent/`, `internal/session/`, `internal/llm/`, `internal/core/`
- Multi-package changes
- `concurrent` in filename or path

## Verification DAG (STANDARD example)

```
format (gofmt -l)
      ↓
┌─────────────────────┐
test(./pkg)   vet(./pkg)   build(./...)
                               │
└──────────────────────────────┘
                ↓
           aggregate (Go-side)
```

- `test`, `vet`, `build` each depend on `format` (cheap gate, fails fast).
- `test` and `vet` run concurrently (independent resources).
- `build` runs concurrently with `test` + `vet`.
- Reviewer runs outside the Batch DAG, via `RunBackground`, started
  immediately after scope detection.
- Aggregate waits for both the Batch result and the reviewer goroutine.

## Reviewer Input (16 KiB diff bound)

```
diff size ≤ 16 KiB
→ full diff inline

diff size > 16 KiB
→ truncated diff (preserve all file headers + hunks around modifications)
→ full diff stored as artifact
→ reviewer receives: truncated diff + artifact reference
```

The 16 KiB threshold is a named constant `reviewerDiffLimit = 16 * 1024`.
Not a user-facing setting in Phase 1.

When truncating: keep all `diff --git` headers, all `@@` hunk headers, and
context lines around actual changes. Drop unchanged file bodies first.
Do NOT simply slice the first 16 KiB (that hides later-changed files).

## Verification Cache (Phase 2)

```go
type CacheKey struct {
    Check     string // "go-test", "go-vet", "go-build", "reviewer"
    Scope     string // "./internal/finalize", "./..."
    InputHash string // sha256 hex of relevant file contents
}
```

- In-memory `map[CacheKey]CachedResult` under `sync.RWMutex`.
- Only PASS results cached. Failures never cached as valid.
- PASS reused only when `InputHash` matches (all files in scope unchanged).
- Cache disappears with the process.

## Incremental Re-Verify (Phase 2)

On each `Run()` call the pipeline stores `PreviousRun{checks, changedFiles}`.

On a subsequent `Run()` (after a fix):
1. Compute `newChangedFiles` = all files changed since last finalization.
2. For each PASS check from the previous run, test if scope ∩ newChangedFiles
   is empty. If empty → reuse (cache hit + NOT_RUN the re-run).
3. Re-run only invalidated checks.
4. Any change touching `internal/core/` or a shared interface → invalidate ALL.

## Result States

Every check reports exactly one of:

```
PASS     — executed and succeeded
FAILED   — executed and failed
SKIPPED  — dependency failed; this check was not started
CACHED   — reused from session cache (inputs unchanged since prior PASS)
NOT_RUN  — excluded: tier below threshold, reviewer off, or incremental reuse
```

## Compact Output Formats

### PASS

```
Verification PASS
  tests:  ./internal/finalize (PASS), ./internal/subagent (CACHED)
  vet:    ./internal/finalize (PASS)
  build:  PASS
  review: approve
  4.2s
```

### FAIL (actionable only)

```
Verification FAILED

Tests FAILED — internal/finalize
  pipeline_test.go:88: expected status PASS, got FAILED

Vet:    PASS
Build:  PASS
Review: approve-with-notes
  pipeline.go:44 — missing context cancellation on early return (minor)

1 test failure, 1 review note.
Full log: log:/path/to/session/finalize-test-finalize.log
```

Deterministic extraction (not an LLM call). Full logs stored via `outputDir`
artifact mechanism. Reference returned when size exceeds `LargeOutputThreshold`.

## TUI (Phase 3)

Settings panel under `/config → Verification`:

```
Verification
  Mode              Smart
  Parallel          On
  Max concurrency   4
  Affected first    On
  Incremental       On
  Reviewer          Smart
```

Live status during finalization (tool-block subtitle line):

```
Finalize  ✓ format  ● tests  ● vet  ✓ build  ● review
```

Reuse existing Batch progress display pattern.

## Model Prompt Update (Phase 3)

Replace sequential verification instructions in `workerSystemPrompt`:

```
When implementation is complete, call the Finalize tool instead of running
verification commands individually. Finalize runs parallel affected-scope
verification and returns one aggregated result. On PASS, finish immediately.
On FAIL, treat the output as actionable implementation work.
```

Do not teach the model how to orchestrate verification. Runtime handles it.

---

## Phases

---

### Phase 1 — Smart Parallel Finalize

**Intent:** Build the complete first-useful version: scope detection, tier
classification (merged from old Phase 2), DAG construction, parallel
execution via `BatchExecutor`, speculative reviewer, and compact aggregation.
This includes affected-scope narrowing from the start — a pipeline that always
runs `./...` would be slower than the status quo on weak machines.

**Expected Outcomes:**
- `Finalize` tool call replaces sequential test → vet → build → review.
- Independent checks execute concurrently via the `BatchExecutor` engine.
- `test` and `vet` run on affected packages only for STANDARD tier.
- Reviewer launches concurrently with Batch checks.
- FAST/STANDARD/FULL tier correctly selected from changed files.
- Risky paths (`internal/core/`, `go.mod`, etc.) → FULL.
- Trivial changes (comments only) → FAST with reviewer skipped.
- Compact PASS / FAIL output returned to model.
- No model round trip between deterministic checks.
- Plan Mode → rejected.
- Model-supplied shell strings cannot flow through `FinalizePipeline`.
- `go test ./internal/finalize/...` and `go test ./internal/tool/batch/...` pass.
- `go vet ./...` passes.

**Todo List:**
1. In `internal/tool/batch/executor.go`:
   - Extract `executeBatch()` logic into a concrete `engine` struct.
   - Define exported `Executor` interface + `Request` / (reuse `BatchResult`) types.
   - Export `NewEngine() Executor` constructor.
   - Update `BatchTool` to hold an `Executor` field; wire in `NewBatchTool()`.
   - No behavior change; all existing batch tests must still pass.
2. Create `internal/finalize/` package.
3. Write `scope.go`:
   - `DetectChangedFiles(ctx, cwd string) ([]string, error)` — runs
     `git diff --name-only HEAD` via `exec.CommandContext`. Returns empty
     slice (not error) when not a git repo.
   - `ExtractGoPackages(cwd string, files []string) []string` — maps
     changed `.go` file paths to `./relative/pkg` patterns suitable for
     `go test`.
4. Write `tier.go`:
   - `Tier` type with constants `TierFast`, `TierStandard`, `TierFull`.
   - `ClassifyTier(files []string) Tier` — deterministic prefix-table rules.
   - `IsReviewerNeeded(files []string, mode string) bool` — simple heuristics.
   - Risky-path table is the canonical auditable list.
5. Write `dag.go`:
   - `BuildDAG(tier Tier, packages []string, cwd string) []batch.BatchCommand`
   - Produces correctly wired `depends_on` graph: format → {test, vet, build}.
   - FAST: format + compile-check only.
   - STANDARD: format → {test(pkgs), vet(pkgs), build(./...)}.
   - FULL: format → {test(./...), vet(./...), build(./...)}.
6. Write `reviewer.go`:
   - `reviewerDiffLimit = 16 * 1024` constant.
   - `BuildDiff(ctx, cwd string) (string, ArtifactRef, error)` — runs
     `git diff HEAD`, truncates intelligently at the limit, stores full
     diff as artifact when over limit.
   - `BuildReviewerPrompt(task, diff string, ref ArtifactRef, files []string) string`
   - `LaunchReviewer(ctx context.Context, exec SubagentExecutor, prompt, outputDir string) ReviewerHandle`
     where `ReviewerHandle` is a small wrapper around `*task.AgentTask` with
     a `Wait() ReviewerResult` method.
   - `SubagentExecutor` interface (defined in `internal/finalize`) with only
     `RunBackground(...)` — keeps `internal/finalize` from importing
     `internal/subagent` directly (respects dependency direction via interface).
7. Write `aggregate.go`:
   - `Aggregate(batchRes *batch.BatchResult, reviewRes ReviewerResult, outputDir string) Result`
   - `Result.String()` returns the compact PASS or FAIL output.
   - Stores large logs as artifacts via `outputDir`.
   - Clearly marks each check with its state (PASS/FAILED/SKIPPED/NOT_RUN).
8. Write `pipeline.go`:
   - `Pipeline` struct: holds `batch.Executor`, `SubagentExecutor`, settings
     snapshot, `outputDir`.
   - `func NewPipeline(batchExec batch.Executor, subagentExec SubagentExecutor, outputDir string, settings VerificationSettings) *Pipeline`
   - `func (p *Pipeline) Run(ctx context.Context, req Request) (Result, error)`
     where `Request` holds: `CWD`, `Task`, `AcceptanceCriteria`.
   - Pipeline constructs all verification commands itself; no
     model-supplied strings accepted.
9. Write `internal/tool/finalize/tool.go`:
   - `FinalizeTool` implementing `tool.Tool`.
   - Holds a `*finalize.Pipeline`.
   - `Execute()` rejects in Plan Mode (same as `BatchTool`).
   - Input params: `task` (string), `acceptance_criteria` (string, optional).
   - Register in `internal/tool/register/`.
10. Write tests:
    - `scope_test.go`: package extraction from file paths, empty-repo handling.
    - `tier_test.go`: each entry in the risky-path table, multi-package → FULL,
      comments-only → FAST, reviewer-skip for trivial.
    - `dag_test.go`: STANDARD produces scope-narrowed commands, FULL produces
      `./...` commands, `format` always has no deps, `test`/`vet`/`build` all
      depend on `format`.
    - `pipeline_test.go`: concurrent execution (spy BatchExecutor counting
      max concurrency), reviewer launched before Batch completes, PASS
      compact output, FAIL actionable output, Plan Mode rejection.
11. Update `internal/subagent/builtin_worker.go` system prompt (one sentence
    pointing to Finalize; do not add orchestration detail).
12. Run `go test ./internal/finalize/...`, `go test ./internal/tool/batch/...`,
    `go vet ./...`, `go build ./...`.

**Relevant Context:**
- [`internal/tool/batch/batch.go`](internal/tool/batch/batch.go) — current
  `executeBatch()`, `BatchCommand`, `BatchResult` to extract into engine.
- [`internal/subagent/executor.go`](internal/subagent/executor.go) — `RunBackground()` signature.
- [`internal/subagent/builtin_worker.go`](internal/subagent/builtin_worker.go) — prompt to update.
- [`internal/tool/register/`](internal/tool/register/) — blank-import pattern.
- [`internal/task/manager.go`](internal/task/manager.go) — `outputDir` / artifact storage.
- [`docs/reference/dependency-rules.md`](docs/reference/dependency-rules.md) — layer rules.
- `internal/setting/settings.go` lines 193–234 — `SubagentSettings` pattern to follow.

**Status:** [x] done

Phase 1 implementation notes (for Phase 2 reference):
- `batch.Engine` interface + `concreteEngine` in `internal/tool/batch/executor.go`
- `internal/finalize/` package: doc.go, scope.go, tier.go, dag.go, reviewer.go, aggregate.go, pipeline.go
- `internal/tool/finalize/tool.go` registered via `internal/tool/register/all.go`
- `tool.ToolFinalize = "Finalize"` added to `internal/tool/schema.go` + `builtinToolOrder`
- `internal/finalize/SubagentExecutor` + `ReviewerTask` interfaces (consumer-side, avoid cross-layer imports)
- Worker `AllowTools` includes `tool.ToolFinalize`
- `docs/reference/package-map.md` updated; layercheck: no violations
- All tests pass including `-race`

---

### Phase 2 — Incremental Re-Verify + Session Cache

**Intent:** After a verification failure and subsequent fix, re-run only the
checks invalidated by the new changes. Add a session-scoped PASS cache so
unchanged packages are not re-verified on the second `Finalize` call.

**Expected Outcomes:**
- Second `Finalize` call with unchanged files reuses cached PASS results
  (shown as `CACHED` in output, not re-executed).
- Fix to a single test file reruns only that package's tests; other PASS
  results from the previous run are reused.
- Fix touching `internal/core/` invalidates ALL cached results.
- Cache is never reused after relevant files change.
- Cache disappears with the process (in-memory).
- `go test -race ./internal/finalize/...` passes.

**Todo List:**
1. Write `cache.go`:
   - `CacheKey{Check, Scope, InputHash string}`.
   - `Cache` with `sync.RWMutex`, `Get(k CacheKey) (CachedResult, bool)`,
     `Set(k CacheKey, r CachedResult)`, `InvalidateScope(scope string)`,
     `InvalidateAll()`.
   - `InputHash` = hex-encoded `sha256` of concatenated sorted file contents
     for all `.go` files in scope. Use `os.ReadFile`; skip unreadable files
     with a logged warning.
2. Write `incremental.go`:
   - `State` tracks `lastRun map[string]CheckResult` keyed by `"check:scope"`.
   - `SelectInvalidated(newChangedFiles []string) []string` — returns
     check-scope keys whose scope overlaps with `newChangedFiles`.
   - `RecordRun(results []CheckResult)` — updates state after each `Run()`.
3. Wire `Cache` and `State` into `pipeline.go`:
   - Before executing a check, consult `Cache.Get()`. If hit → mark CACHED,
     skip execution.
   - After all checks, call `State.RecordRun()`.
   - On next `Run()`, call `State.SelectInvalidated()` to prune the DAG
     before building it.
4. Write `cache_test.go`:
   - Hit: same files → CACHED, command not executed.
   - Miss: file changed → re-executed.
   - Stale: file content changes → hash mismatch → re-executed.
   - Full invalidation: risky-path change → all PASS results discarded.
   - No goroutine leak under `-race`.
5. Extend `pipeline_test.go`:
   - Incremental re-verify: first run FAIL on pkg A; fix pkg A; second run
     reruns only pkg A, reuses pkg B PASS.
6. Add `VerificationSettings` to `internal/setting/settings.go`:
   - Follow `SubagentSettings` pattern exactly.
   - Wire snapshot into `Pipeline` at construction.
   - `Validate()` checks allowed `mode` and `reviewer` values.
7. Run `go test -race ./internal/finalize/...`, `go vet ./...`, `go build ./...`.

**Relevant Context:**
- `internal/finalize/pipeline.go` — from Phase 1.
- `internal/finalize/scope.go` — file enumeration for hashing.
- `internal/setting/settings.go` lines 193–234 — `SubagentSettings` pattern.
- Spec sections 7, 8, 23.

**Status:** [ ] pending

---

### Phase 3 — Performance Polish + Benchmarks + Prompt Cleanup

**Intent:** Make the latency improvement measurable and visible. Add
resource-aware scheduling guidance, improve deterministic error extraction,
clean up the model prompt, and add TUI settings panel.

**Expected Outcomes:**
- `VerificationSettings` visible and editable in `/config` TUI panel.
- Live Finalize status shown in tool-block subtitle during execution.
- Benchmark report (`docs/benchmarks/smart-parallel-finalize.md`) covering
  scenarios A–E with before/after wall-clock, model calls, commands, tokens.
- Worker prompt updated; no sequential verification instructions remain.
- `go test ./...`, `go vet ./...`, `go build ./...` pass.

**Todo List:**
1. Add `internal/finalize/metrics.go`:
   - `Metrics` struct: `ImplementationDuration`, `FinalizeDuration`,
     per-check durations, `ChecksCached int`, `ChecksSkipped int`,
     `ReviewerCalls int`.
   - Attached to `Result`; not injected into model prompts.
   - Exposed only for benchmarks and debug logging.
2. Add TUI Verification panel:
   - Follow the `Subagents` panel pattern in `internal/app/`.
   - Show `Mode`, `Parallel`, `MaxConcurrency`, `AffectedFirst`,
     `Incremental`, `Reviewer` fields.
   - Writes `VerificationSettings` back to settings on save.
3. Add live status to `FinalizeTool` result rendering:
   - Reuse Batch progress display pattern.
   - Show `✓ format  ● tests  ● vet  ✓ build  ● review` in subtitle.
4. Resource-aware scheduling note in `dag.go`:
   - Add a comment documenting the scheduling rationale: reviewer API
     request + go test is good parallelism (different resources); multiple
     CPU-heavy `go test ./...` on a weak machine is not. The `MaxConcurrency`
     setting is the primary knob; document defaults.
5. Update `internal/subagent/builtin_worker.go` prompt fully:
   - Remove all "run go test, then go vet, then go build" sequential
     instructions.
   - Add one sentence pointing to Finalize.
6. Write benchmark harness `internal/finalize/bench_test.go`:
   - Scenarios A (tiny localized), B (one-package), C (multi-package),
     D (core/runtime), E (fail → fix → reverify).
   - Uses a fake `batch.Executor` and fake `SubagentExecutor` for
     deterministic wall-clock simulation.
   - Reports: wall-clock time, commands executed, checks cached.
7. Write `docs/benchmarks/smart-parallel-finalize.md`:
   - Baseline (sequential) vs. Smart Parallel Finalize for each scenario.
   - Include measured data from bench_test.go.
8. Update `docs/reference/package-map.md`:
   - Add `internal/finalize` and `internal/tool/finalize` entries.
9. Update `docs/packages/index.md` with finalize feature entry.
10. Update `AGENTS.md` repository shape section.
11. Run `go test ./...`, `go vet ./...`, `go build ./...`, `go test -race ./...`.

**Relevant Context:**
- `internal/app/` — TUI panel pattern (follow Subagents panel).
- `internal/subagent/builtin_worker.go` — full prompt rewrite.
- `docs/benchmarks/` — existing benchmark directory.
- Spec sections 14, 19, 20, 21, 22, 27.

**Status:** [ ] pending

---

### Phase 4 — Worktree Integration

**Intent:** When workers use isolated Git worktrees, verify each worker's
changes in its worktree before merge. After integration into the main
checkout, run only integration-level checks not already validated per-worker.
Do NOT implement speculative early verification while other workers are still
modifying related work — that is deferred.

**Expected Outcomes:**
- When a worker batch uses worktrees (detected via `git worktree list`),
  each worker's finalization runs in its own worktree before merge.
- After merge into main checkout, only integration checks that may have been
  invalidated by the merge run (not full re-verification of all workers).
- A `WorktreeHandle` interface is defined in `internal/finalize` to support
  future early per-worker verification — but the speculative behavior is not
  enabled yet.
- All existing tests still pass.
- `go test ./...`, `go vet ./...`, `go build ./...` pass.

**Todo List:**
1. Write `internal/finalize/worktree.go`:
   - `DetectWorktrees(cwd string) ([]string, error)` — runs
     `git worktree list --porcelain`, returns paths.
   - `WorktreeHandle` interface: `Path() string`, `ChangedFiles() []string`.
     Defined here for future early-verification hookup, not yet used for
     speculative work.
2. Extend `pipeline.go`:
   - `RunForWorktree(ctx, worktreePath string, req Request) (Result, error)`
     — runs finalization scoped to one worktree's changes.
   - `RunIntegration(ctx, worktreePaths []string, req Request) (Result, error)`
     — runs only checks invalidated by the merge (diff between merged result
     and union of already-verified worker scopes).
3. Wire `RunForWorktree` into `internal/subagent/executor_batch.go`:
   - After each worker completes, record its changed files + worktree path.
   - On overall batch completion, `FinalizePipeline.RunIntegration()` runs.
4. Write `worktree_test.go`:
   - Worktree detection, per-worktree result reuse, integration check
     scoping, no redundant re-runs of already-verified worker checks.
5. Update benchmark scenarios with worktree cases.
6. Run `go test ./...`, `go vet ./...`, `go build ./...`.

**Relevant Context:**
- `internal/subagent/executor_batch.go` — worker batch fan-out to extend.
- `internal/finalize/pipeline.go` — Phase 1 base.
- Spec sections 18, 16 (deferred part).

**Status:** [ ] pending

---

## Correctness Invariants (enforced throughout all phases)

- Tests reported PASS only when actually executed or safely cached with
  matching `InputHash`.
- Cached PASS is never reused after relevant files change.
- Reviewer result is never silently ignored.
- Cross-package changes always trigger FULL-scope verification.
- Concurrency/race-related file changes always trigger FULL.
- Command failures are never hidden by output compaction.
- Final answer never claims verification that did not occur.
- Every check result is exactly one of: PASS, FAILED, SKIPPED, CACHED, NOT_RUN.
- Finalize never executes model-supplied shell strings without permission.

## Files Changed / Created Across All Phases

### New
- `internal/tool/batch/executor.go`
- `internal/finalize/*.go` (pipeline, scope, tier, dag, reviewer, aggregate,
  cache, incremental, metrics, worktree)
- `internal/finalize/*_test.go`
- `internal/tool/finalize/tool.go`
- `docs/benchmarks/smart-parallel-finalize.md`

### Modified
- `internal/tool/batch/batch.go` — wire `BatchTool` to new `Executor` field
- `internal/tool/register/` — blank-import `internal/tool/finalize`
- `internal/setting/settings.go` — add `VerificationSettings`
- `internal/subagent/builtin_worker.go` — update system prompt
- `internal/subagent/executor_batch.go` — Phase 4 worktree integration hooks
- `internal/app/` — TUI panel (Phase 3)
- `docs/reference/package-map.md` — add new packages
- `docs/packages/index.md` — add finalize entry
- `AGENTS.md` — repository shape update
