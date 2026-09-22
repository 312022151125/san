# Advisor Agent Plan

## Overview

Add a built-in `advisor` subagent — a read-only reasoning consultant the parent
or worker can invoke for difficult architectural, concurrency, correctness, or
multi-approach decisions. The advisor provides a second opinion and returns a
concise recommendation with reasoning and relevant file/risk references, without
modifying the repository.

**Scope:**

1. Embed the advisor's agent definition as a compiled-in default — no user file
   required, lowest priority so a user/project `advisor.md` always wins.
2. Add `AdvisorSettings{Model string}` to `setting.Data` (`advisor.model` in
   `settings.json`) so users can point the advisor at a stronger reasoning model.
3. Add a general `modelOverrides map[string]string` to the executor — no
   agent-name special-casing inside the executor logic itself.
4. Wire settings → executor at the build site; update docs.

**Decisions locked:**

| Topic | Decision |
|---|---|
| Built-in priority | Lowest — register before `LoadAgents()`, filesystem definitions win |
| `provider` field | Dropped — `vendor/model` syntax in `model` only, matches existing agent system |
| Executor coupling | General `modelOverrides map[string]string`, not `if name == "advisor"` |
| `Source` value | Add `"builtin"` to `AgentConfig.Source` |
| System prompt structure | `Relevant Files` section is **optional** |

**Non-goals:**

- No permanent advisor process.
- No automatic advisor invocation on every task.
- No new permission mode — `explore` (read-only) is sufficient.
- No new concurrency mechanism — existing async `task.AgentTask` handles it.

---

## Architecture

```
setting.Data.Advisor (AdvisorSettings{Model string})
         │
         ▼
subagent.Executor.SetModelOverride("advisor", model)
         │  called at build-site in internal/app/agent.go
         │
         ▼
executor.prepareRunConfig()
  └─ effectiveConfigModel = modelOverrides[config.Name] OR config.Model
         │
         ▼
resolveModel(ctx, req.Model, effectiveConfigModel)

Priority:
  1. req.Model         — caller-supplied (rarely set for advisor)
  2. modelOverrides    — settings-level per-name override
  3. config.Model      — agent definition model
  4. inherit           — parent conversation model
```

---

## Sub-Tasks

---

### Sub-task 1 — Embedded advisor agent definition

**Status:** [ ] pending

**Intent:**
Register the advisor as a compiled-in default agent so it is always available
without the user creating a file. Registered programmatically at `Initialize()`
time at lowest priority — any filesystem definition wins.

**Expected Outcomes:**
- `subagent.Default().Get("advisor")` returns a valid `*AgentConfig` in any
  environment, even with no `.san/agents/` directory.
- The advisor has `mode: explore`, `allow_tools: [Read, Grep, Glob]`, a rich
  `description`, a `when-to-use` hint, and a system prompt focused on concise
  second-opinion recommendations.
- `MaxSteps` is **30** — narrow enough to prevent repo-wide wandering.
- `Source` is `"builtin"` — distinct from `"project"`, `"user"`, `"plugin"`.
- A user or project-level `advisor.md` file still wins (loader priority unchanged).

**Todo List:**
1. Add `"builtin"` as a valid `Source` constant in `internal/subagent/types.go`
   alongside `"project"`, `"user"`, `"plugin"`.
2. Create `internal/subagent/builtin_advisor.go` with `BuiltinAdvisorConfig()`
   returning `*AgentConfig`:
   - `Name: "advisor"`
   - `Description: "Read-only reasoning consultant for difficult decisions"`
   - `WhenToUse`: expanded on-demand guidance (see below)
   - `PermissionMode: PermissionExplore`
   - `AllowTools`: `ToolList` with `Read`, `Grep`, `Glob`
   - `Model: "inherit"`
   - `MaxSteps: 30`
   - `Source: "builtin"`
   - `SystemPrompt`: inline body string (see design below)
3. In `internal/subagent/service.go` `Initialize()`, call
   `defaultRegistry.Register(BuiltinAdvisorConfig())` **before** `LoadAgents()`.

**WhenToUse guidance (for system prompt section in registry):**
```
Use when: architectural trade-offs exist between multiple approaches; the
implementation is uncertain; repeated attempts have failed; concurrency,
security, or correctness implications are non-trivial; the user explicitly
requests a second opinion. Do NOT invoke for simple edits or routine tasks.
```

**System prompt body:**
```
You are a read-only reasoning advisor. Your job is to help the parent agent or
worker make difficult decisions — not to implement solutions.

When invoked, you receive a question or decision point plus relevant context
(files, findings, implementation options). Use Read, Grep, and Glob to gather
additional repository context when needed.

Respond with this structure:
1. Recommendation — one or two sentences stating the preferred approach.
2. Reasoning — concise explanation of trade-offs and why this approach wins.
3. Risks / Caveats — correctness, concurrency, security, or design risks.
4. Relevant Files — (optional) key files and a brief note for each.

Rules:
- Return a concise recommendation, not a long transcript.
- Do not write code or modify files.
- Be direct. Omit pleasantries.
```

**Relevant Context:**
- [`internal/subagent/types.go`](internal/subagent/types.go) — `AgentConfig`, `PermissionMode`, `ToolList`, `Source`
- [`internal/subagent/service.go`](internal/subagent/service.go) — `Initialize()`, `LoadAgents()`
- [`internal/subagent/registry.go`](internal/subagent/registry.go) — `Register()`
- [`internal/tool/schema.go`](internal/tool/schema.go) — `ToolRead`, `ToolGrep`, `ToolGlob` constants

---

### Sub-task 2 — AdvisorSettings in setting.Data

**Status:** [ ] pending

**Intent:**
Allow the user to pin the advisor to a stronger reasoning model via
`settings.json`, following the same pattern as `AutoPilotSettings.Model`.
No `provider` field — use `vendor/model` syntax directly, matching the existing
agent definition system.

**Expected Outcomes:**
- `settings.json` accepts:
  ```json
  { "advisor": { "model": "anthropic/claude-opus-4-7" } }
  ```
- `setting.Data.Advisor` is a plain `AdvisorSettings{Model string}` struct.
- `AdvisorSettings` is a value type (no pointer); zero value = use agent config model.
- Settings merge coalesces strings (non-empty overlay wins), same as `AutoPilotSettings.Model`.
- `Clone()` copies the struct value (it contains only strings, so assignment suffices).

**Todo List:**
1. Add `AdvisorSettings` struct to `internal/setting/settings.go`:
   ```go
   // AdvisorSettings tunes the built-in advisor subagent.
   // Model overrides the model used for advisor runs. Accepts the same
   // forms as an agent definition's model field: a bare alias, a bare
   // model id, or a "vendor/model" ref. Empty inherits the session model.
   type AdvisorSettings struct {
       Model string `json:"model,omitempty"`
   }
   ```
2. Add `Advisor AdvisorSettings \`json:"advisor"\`` field to `Data` struct.
3. Add `mergeAdvisor(base, overlay AdvisorSettings) AdvisorSettings` to
   `merger.go` — single line: `return AdvisorSettings{Model: coalesce(overlay.Model, base.Model)}`.
4. Wire `result.Advisor = mergeAdvisor(base.Advisor, overlay.Advisor)` in `mergeSettings`.
5. Add `dst.Advisor = s.Advisor` to `Clone()`.

**Relevant Context:**
- [`internal/setting/settings.go`](internal/setting/settings.go) — `Data`, `AutoPilotSettings`
- [`internal/setting/merger.go`](internal/setting/merger.go) — `mergeAutoPilot`, `coalesce`

---

### Sub-task 3 — General named model overrides in the executor

**Status:** [ ] pending

**Intent:**
Replace the would-be `if config.Name == "advisor"` special-case with a general
`modelOverrides map[string]string` on the executor. The app wires
`"advisor" → settings.Advisor.Model` at build time; the executor stays ignorant
of specific agent names. This pattern is immediately reusable for `explore`,
`worker`, `reviewer`, etc. without future special-casing.

**Priority inside `prepareRunConfig`:**
```
1. req.Model                  — caller-supplied override (highest)
2. modelOverrides[config.Name] — settings-level per-name override (new)
3. config.Model               — agent definition model
4. inherit                    — parent conversation model (lowest)
```

**Expected Outcomes:**
- `Executor` has a `modelOverrides map[string]string` field.
- `SetModelOverride(name, model string)` adds one entry; nil-safe (initializes map).
- In `prepareRunConfig`, `configModel` is resolved as:
  ```go
  configModel := config.Model
  if override, ok := e.modelOverrides[config.Name]; ok && override != "" && req.Model == "" {
      configModel = override
  }
  ```
  before the `resolveModel` call — no change to `resolveModel` itself.
- No Executor field or method references `"advisor"` as a string.
- The build site in `internal/app/agent.go` calls
  `executor.SetModelOverride("advisor", m.services.Setting.Get().Advisor.Model)`
  (only when the model string is non-empty, to keep zero-value clean).

**Todo List:**
1. Add `modelOverrides map[string]string` to the `Executor` struct.
2. Add `SetModelOverride(name, model string)` method (initialize map lazily).
3. In `prepareRunConfig`, apply the override before calling `resolveModel`.
4. In `internal/app/agent.go`, after `executor.SetDisabledTools(...)`:
   ```go
   if m := m.services.Setting.Get().Advisor.Model; m != "" {
       executor.SetModelOverride("advisor", m)
   }
   ```
5. In `internal/app/run_agent.go` (headless executor), apply the same wiring
   so CLI `san agent` runs also honour the settings override.

**Relevant Context:**
- [`internal/subagent/executor.go`](internal/subagent/executor.go) — `Executor`, `prepareRunConfig`, `resolveModel`
- [`internal/app/agent.go`](internal/app/agent.go) — TUI build site, lines 759-777
- [`internal/app/run_agent.go`](internal/app/run_agent.go) — CLI build site, lines 66-67

---

### Sub-task 4 — System prompt guidance and docs update

**Status:** [ ] pending

**Intent:**
Update the parent agent's guidance, the subagent writing guide, the package
doc, and add a new advisor guide so users and agents know the advisor exists and
when to use it.

**Expected Outcomes:**
- `AGENTS.md` documents five conceptual default agents and the advisor's role.
- `docs/guides/writing-a-subagent.md` gets a "Built-in Agents" section listing
  the advisor with its settings override.
- `docs/packages/2-feature/subagent.md` updated: `builtin_advisor.go` in
  Internals, `"builtin"` source in the loader section, built-in registration
  step in Lifecycle.
- New `docs/guides/advisor-agent.md` covering: purpose, on-demand rules, async
  launch pattern, settings model override, example prompt.

**Todo List:**
1. Add to `AGENTS.md` a "Default Agents" section with this table:
   ```
   | Agent    | Role                                      |
   |----------|-------------------------------------------|
   | explore  | Fast read-only repository exploration     |
   | planner  | Implementation planning and decomposition |
   | worker   | Coding and implementation                 |
   | reviewer | Correctness review                        |
   | advisor  | Difficult reasoning / second opinion      |
   ```
   Note that `explore` is a permission mode (built-in), while `planner`,
   `worker`, `reviewer` are user-defined conventions; `advisor` is the one
   compiled-in default.
2. Add a "Built-in Agents" section to `docs/guides/writing-a-subagent.md`
   explaining the advisor, its `model` override, and a settings example.
3. Update `docs/packages/2-feature/subagent.md`:
   - Add `builtin_advisor.go` to Internals table.
   - Add `"builtin"` source note in loader section.
   - Add built-in registration step to Lifecycle section.
4. Write `docs/guides/advisor-agent.md`:
   - Purpose and read-only constraint.
   - On-demand rules (when to invoke / when not to).
   - Async pattern (start early, block late).
   - `advisor.model` settings override with example.
   - Example invocation prompt sent by parent.

**Relevant Context:**
- [`AGENTS.md`](AGENTS.md) — project navigation map
- [`docs/guides/writing-a-subagent.md`](docs/guides/writing-a-subagent.md)
- [`docs/packages/2-feature/subagent.md`](docs/packages/2-feature/subagent.md)
