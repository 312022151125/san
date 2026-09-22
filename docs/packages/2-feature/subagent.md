---
package: github.com/genai-io/san/internal/subagent
layer: feature
---

# subagent

Registry of agent definitions (markdown-defined personas with their own
system prompt, tool subset, and permission mode) plus the **Executor**
that spawns foreground or background `core.Agent` instances from within the
main agent's tool loop.

## Purpose

Where [`packages/agent.md`](agent.md) owns the *foreground* agent session,
this package owns the subagents that session spawns via the Agent tool.
Each subagent runs its own `core.Agent` loop (event observation via
`OnEvent`; no outbox) with an isolated conversation and its own
tool/permission set, sharing the session's working directory. A foreground subagent blocks the
spawning turn and returns its result as the tool result; a background
subagent runs in a goroutine under a `task.AgentTask` and registers its task
id with the [`broker`](broker.md) for the length of the run — main can
`SendMessage` it while it runs, and its completion is routed to the `main`
address when it finishes.

## Contract

The package exposes the concrete `*Registry` directly. The four
production caller sites use four different subsets of its surface, so
no producer-side role interface earns its keep — TEMPLATE Rule 3.

| Caller | Methods used |
|---|---|
| `cmd/san agent` | `Get` (CLI argument validation) |
| TUI view | `ListConfigs` (color enumeration) |
| Agent build site | `PromptSection` (twice) |
| TUI selector adapter | full surface — `ListConfigs`, `IsEnabled`, `SetEnabled`, `GetDisabledAt` for the `/agent` menu |

Executor construction goes through the package-level `NewExecutor`
free function (in `executor.go`), not a method on the registry. The
free function takes `hook.Handler`, keeping subagent decoupled from
the concrete `*hook.Engine`.

```go
package subagent

// Registry is an opaque handle to the agent registry. It is exported so callers can hold and pass *Registry values; all fields
// are unexported so internal state is reached only through methods.
type Registry struct { /* internal fields */ }

// Query
func (r *Registry) ListConfigs() []*AgentConfig
func (r *Registry) Get(name string) (*AgentConfig, bool)
func (r *Registry) IsEnabled(name string) bool

// State mutation (used by the TUI selector adapter)
func (r *Registry) SetEnabled(name string, enabled bool, userLevel bool) error
func (r *Registry) GetDisabledAt(userLevel bool) map[string]bool

// System prompt
func (r *Registry) PromptSection() string

// Loader bootstrapping
func (r *Registry) Register(config *AgentConfig)
func (r *Registry) InitStores(cwd string) error

// Executor construction (package-level free function)
func NewExecutor(provider llm.Provider, cwd, parentModelID string, hooks hook.Handler) *Executor

// Package-level access
func Initialize(opts Options) error
func Default() *Registry
func SetDefaultRegistry(r *Registry)  // test-only
func ResetDefaultRegistry()           // test-only
```

## Internals

- `Registry` (`registry.go`) — `AgentConfig` map keyed by name, plus
  enable state stores (user + project).
- `Executor` (`executor.go`) — spawns a `core.Agent` for one subagent
  invocation, manages its lifecycle (workspace, permission gate, hooks,
  session persistence), and returns the aggregated result.
  `SetModelOverride(name, model)` wires per-agent settings-level model
  preferences (e.g. advisor → stronger reasoning model) without coupling
  the executor to specific agent names.
- `executor_prompt.go` / `executor_run.go` / `executor_session.go` —
  split executor concerns (charter assembly, run loop, session attribution).
- `builtin_advisor.go`, `builtin_explore.go`, `builtin_worker.go`,
  `builtin_reviewer.go` — compiled-in default agent definitions.
  `advisor` and `explore`: `PermissionExplore`, `Read/Grep/Glob`, `MaxSteps: 30`.
  `reviewer`: `PermissionExplore`, `Read/Grep/Glob/Bash`, `MaxSteps: 30`.
  `worker`: `PermissionAcceptEdits`, `Read/Grep/Glob/Edit/Write/Bash`, `MaxSteps: 50`.
  All registered at lowest priority; any user/project `<name>.md` overrides the
  built-in with the same name.
- `loader.go` — reads markdown agent definitions from `.san/agents/`
  (project, then user), `.claude/agents/` (Claude Code compatible), and
  plugin paths; lower-priority sources load first so higher ones win by
  name. Accepts alias frontmatter keys (`tools`, `allowed-tools`,
  `permission-mode`) alongside the canonical ones. Built-in agents
  (`Source: "builtin"`) register before `LoadAgents()` so filesystem
  definitions win.
- `match.go` — `ToolList` pattern matching (allow/deny semantics) shared
  with the permission gate.
- `activity_tools.go` — the gate that streams each call into the
  parent-visible activity trail. It refuses nothing; it is a gate rather than a
  wrapper because that is what the loop asks once per call.

## Lifecycle

- Construction: `Initialize(Options{CWD, PluginAgentPaths})` registers
  built-in agents (`advisor`, `explore`, `worker`, `reviewer`), then loads
  user/project/plugin definitions, then initializes state stores. Built-ins
  register first so filesystem definitions win; a `.san/agents/<name>.md`
  with the same name overrides the built-in.
- Per-invocation: `NewExecutor(provider, cwd, model, hookEngine)` →
  `Executor.Run(ctx, req)` spawns a `core.Agent`, blocks until end of
  turn, returns the aggregated `AgentResult`. `RunBackground(req)` wraps
  the same run in a `task.AgentTask` goroutine and registers with the broker under
  the task id so main can message it mid-run.
- Every exit path — success, cancel, error — fires `SubagentStop` with
  the same agent id `SubagentStart` carried. Subagents are one-shot: a run
  is not resumable; to continue a line of work, spawn a fresh subagent.
- The agent model is flat: only the main conversation spawns subagents. The
  `Agent` tool is parent-only in `tool.Set`, so a subagent never sees it —
  nothing to enforce at runtime.
- Concurrency: `RunBackground` enforces two semaphore limits via buffered
  channels. `maxBackgroundConcurrency = 3` caps total concurrent background
  agents. `maxBackgroundWriters = 1` serializes agents that hold write
  permissions (`PermissionMode != PermissionExplore`). Read-only agents
  (`explore`, `advisor`, `reviewer`) run in parallel up to the concurrency
  cap but do not consume a writer slot. The caller blocks until a slot is
  available; `Run` (foreground) is unaffected. The registry is
  RWMutex-protected separately.

## Tests

```
internal/subagent/executor_test.go      — end-to-end run scenarios.
internal/subagent/lazy_loading_test.go  — config files read on demand.
internal/subagent/scenarios_test.go     — common invocation shapes.
```

## See Also

- Code: `internal/subagent/`
- Message routing: [`broker`](broker.md)
- Parent agent: [`packages/agent.md`](agent.md)
- Spawning tools: [`packages/tool.md`](tool.md) (Agent, SendMessage)
- Background tasks: [`packages/task.md`](task.md)
- Layer: `feature`
