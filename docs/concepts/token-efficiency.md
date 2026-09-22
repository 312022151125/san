# Token-Efficiency Architecture

San optimises the entire task trajectory, not just individual tool outputs:

```
total tokens ≈ repeated context × model round trips + useful reasoning/output
```

Two distinct batching layers reduce both factors.

---

## Two batching layers

### Agent batch — parallel semantic reasoning

The `Agent` tool accepts an optional `tasks[]` array.
One tool call fans out N independent subagents concurrently.
The shared `context` field is sent once; each task contains only its delta.

```json
{
  "context": "We are refactoring provider routing.\n\n# Goal\n...",
  "tasks": [
    { "agent": "explore", "name": "ProviderExplore", "task": "Map provider resolution." },
    { "agent": "explore", "name": "SettingsExplore",  "task": "Map model settings flow." }
  ]
}
```

**When to use:** 2+ genuinely independent semantic slices.
**When NOT to use:** tasks whose results depend on each other (sequence those instead).

### Batch tool — deterministic command graph

The `Batch` tool accepts a `commands[]` array with optional `depends_on` edges.
Independent commands run concurrently; dependent commands wait.
One tool call, one compact result — no model re-entry between steps.

```json
{
  "commands": [
    { "id": "build", "command": "go build ./..." },
    { "id": "test",  "command": "go test ./...",  "depends_on": ["build"] },
    { "id": "vet",   "command": "go vet ./...",   "depends_on": ["build"] }
  ]
}
```

**When to use:** multiple deterministic shell operations where the control flow is known.
**When NOT to use:** operations that require model judgment between them.

### The critical distinction

```
Agent tasks[] = parallel semantic reasoning = multiple model children
Batch         = deterministic command DAG   = zero model reasoning between steps
```

---

## Full tree optimisation

Savings happen at every level:

```
Main
 └─ Agent batch ─────── 1 model turn
      ├─ explore
      └─ worker
           ├─ Edit
           └─ Batch ─── 1 tool round
                ├─ gofmt
                ├─ go test
                ├─ go vet
                └─ go build
```

Without batch: `N` Agent calls × `M` Bash calls = many model round trips.
With batch: 1 Agent call + 1 Batch call = 2 tool invocations.

---

## Semantic boundary

The runtime re-enters the model only when:

- New information requires judgment
- An unexpected failure occurs
- A choice cannot be deterministically resolved
- User input is required
- A task completes

It does **not** re-enter merely because one command succeeded.

---

## Output compaction

Batch tool results are deterministically compacted before reaching the model:

- **Success:** exit code + duration only (no stdout).
- **Failure:** exit code + duration + error lines + last 5 lines.
- **Large output:** stored to `<taskDir>/<id>.log`; inline result contains a `log:` reference.
- **Repeated lines:** collapsed (`error: x (×42)`).

Agent batch results similarly suppress the child's activity trace.
The parent sees only: agent name, model, steps, tokens, duration, content summary.

---

## Session metrics

`SessionMetrics` tracks per-session:

| Metric | What it counts |
|--------|----------------|
| `model_calls` | Full San turns (model round trips) |
| `tool_calls` | Individual tool invocations |
| `command_steps` | Commands executed inside Batch |
| `subagent_calls` | Agents executed inside a batch |
| `input_tokens` | Uncached input tokens |
| `cache_tokens` | Cache-read tokens |
| `output_tokens` | Model output tokens |

Use `/debug metrics` to inspect the current session.
Metrics are **never injected into model prompts**.

---

## Permission model

### Agent batch — Plan Mode
Only `explore`-mode agents are allowed in Plan Mode.
Write-capable agents in a batch are rejected atomically before any child starts.

### Batch tool — Plan Mode
Rejected entirely in Plan Mode.
No command classification — the conservative policy avoids mutation-detection complexity.

### Batch tool — subagent access
`Batch` is NOT parent-only.
Write-capable agents (`worker`, `PermissionAcceptEdits`, `PermissionBypass`) receive it.
Read-only agents (`explore`, `advisor`, `reviewer`) do not — their `AllowTools` list
does not include `Bash` or `Batch`.

---

## Dynamic schemas

When a feature is disabled, its schema tokens disappear entirely:

- `Agent tasks[]` / `context`: absent when `SchemaOptions.BatchEnabled = false`
- Memory tools: absent when `memory.backend ≠ hindsight`
- Evolve tool: absent when self-learning is disabled

---

## Files

| File | Role |
|------|------|
| `internal/tool/agent/` | Agent tool + batch invocation path |
| `internal/tool/batch/` | Batch tool (DAG executor, compact formatter) |
| `internal/subagent/executor_batch.go` | `RunBatch` — fan-out, preflight, artifact storage |
| `internal/core/metrics.go` | `SessionMetrics` struct |
| `internal/agent/session.go` | Metrics wiring via `OnEvent` |
| `internal/core/system/prompts/behavior.txt` | Backward reasoning + batch guidance |
| `internal/core/system/prompts/rules.txt` | Completion discipline |
