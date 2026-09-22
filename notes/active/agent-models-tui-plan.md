# Agent Models TUI Plan

## Overview

Add an **Agent Models** panel to the existing `/config` settings popup so users
can configure each registered agent's model override without editing JSON.

**Scope:**

1. Replace the per-agent `AdvisorSettings` struct in `setting.Data` with a
   generic `AgentModelSettings` map (`settings.json` key `"agents"`).
2. Add `UpdateAgentModelAt(name, model string, userLevel bool)` and `GetAgentModel(name) string` to `internal/setting/loader.go`.
3. Add a `config_agents.go` panel that implements `Panel` and plugs into the
   existing `/config` `PanelPopup`.
4. Wire the panel into `NewConfigSelector`, feed it the agent registry and
   current model, and propagate saves to both `settings.json` and the running
   executor's `SetModelOverride` map.
5. Remove the now-redundant `AdvisorSettings` type and its wiring.

**Non-goals:**
- No new popup command or slash command — this lives in `/config`.
- No second model list — `ProviderSelector` is reused.
- No per-agent system prompt or permission overrides — model only.
- No mutation of already-running subagents.

---

## Design

### JSON schema (new)

```json
{
  "agents": {
    "advisor": { "model": "anthropic/claude-opus-4-7" },
    "worker":  { "model": "deepseek/deepseek-v4" }
  }
}
```

An entry with empty `model` (or no entry at all) means "inherit parent model".

### settings.Data change

`AdvisorSettings` → `AgentModelSettings` map:

```go
// AgentModelSettings stores per-agent model overrides.
// key = agent name (lower-case), value = model ref or "".
type AgentModelSettings map[string]AgentModelEntry

type AgentModelEntry struct {
    Model string `json:"model,omitempty"`
}
```

`Data.Advisor AdvisorSettings` → `Data.Agents AgentModelSettings \`json:"agents"\``

`setting.Settings.Snapshot().Agents["advisor"].Model` replaces
`setting.Settings.Snapshot().Advisor.Model`.

### Executor wiring change

`internal/app/agent.go` build site currently has:
```go
executor.SetModelOverride("advisor", m.services.Setting.Snapshot().Advisor.Model)
```

Replaced with a range over all `Agents` entries:
```go
for name, entry := range m.services.Setting.Snapshot().Agents {
    executor.SetModelOverride(name, entry.Model)
}
```

### Panel UX

```
⚙ Config   appearance & settings       [tab] Agents

AGENT MODELS ─────────────────────────────────────

  advisor    anthropic/claude-opus-4-7          ●
  explore    Inherit → claude-sonnet-4-6
  worker     Inherit → claude-sonnet-4-6
  planner    Inherit → claude-sonnet-4-6
  reviewer   Inherit → claude-sonnet-4-6

────────────────────────────────────────────────
↑↓ navigate  enter pick model  del clear  esc close
```

- Rows are built from `subagent.Default().ListConfigs()` — all registered agents.
- Selecting a row opens the existing `ProviderSelector`.
- A special top row "Inherit parent model" in the picker clears the override.
- After selection, the panel saves immediately to `settings.json` and emits
  `AgentModelSavedMsg{Name, Model}` so the app can call `SetModelOverride`.

---

## Sub-Tasks

---

### Sub-task 1 — Generic `AgentModelSettings` in `setting.Data`

**Status:** [ ] pending

**Intent:**
Replace the ad-hoc `AdvisorSettings` with a generic per-agent map that holds
a model ref string for each named agent. Keeps the JSON schema extensible —
new agents appear automatically without adding more fields.

**Expected Outcomes:**
- `Data.Agents AgentModelSettings \`json:"agents"\`` replaces `Data.Advisor`.
- `AgentModelSettings` is `map[string]AgentModelEntry`; zero value is an empty map.
- `AgentModelEntry` is `struct { Model string \`json:"model,omitempty"\` }`.
- Merge: `mergeAgents` does a key-union with non-empty overlay value winning per key.
- `Clone()` deep-copies the map.
- `GetAgentModel(name string) string` helper on `Data` returns the model string
  for a named agent, or `""` for inherit.
- Backward compatibility: the old `"advisor": {...}` top-level JSON key is
  ignored on load (it was never widely used). Add a migration shim in `loader.go`
  that moves a non-empty `Data.Advisor.Model` into `Data.Agents["advisor"].Model`
  on first load, then clears `Advisor`.
- `UpdateAgentModelAt(name, model string, userLevel bool) error` in `loader.go`
  — mutates only the `agents[name].model` key via `updateSettingsFile`.

**Todo List:**
1. Add `AgentModelEntry` and `AgentModelSettings` types to `settings.go`.
2. Add `Agents AgentModelSettings` field to `Data`; add `GetAgentModel(name) string` method.
3. Add `mergeAgents` to `merger.go` and wire in `mergeSettings`.
4. Update `Clone()` to deep-copy the map.
5. Add `UpdateAgentModelAt` to `loader.go`.
6. Add a `migrateAdvisorField` helper in `loader.go` called at the end of `Load()`.
7. Remove `Data.Advisor AdvisorSettings` field and `mergeAdvisor` once the migration shim is in place.

**Relevant Context:**
- [`internal/setting/settings.go`](internal/setting/settings.go) — `AdvisorSettings`, `Data`, `Clone()`
- [`internal/setting/merger.go`](internal/setting/merger.go) — `mergeAdvisor`, pattern
- [`internal/setting/loader.go`](internal/setting/loader.go) — `updateSettingsFile`, `UpdateAutoPilotAt`

---

### Sub-task 2 — Update executor wiring to use `Agents` map

**Status:** [ ] pending

**Intent:**
Replace the single `executor.SetModelOverride("advisor", ...)` call at both
build sites with a loop over all `settings.Agents` entries.

**Expected Outcomes:**
- `internal/app/agent.go` and `internal/app/run_agent.go` loop over `Snapshot().Agents`.
- `SetModelOverride` is unchanged — it's already generic.
- The `advisorWhenToUse` constant in `builtin_advisor.go` can reference
  `settings.json` `"agents"` key instead of the old `"advisor"` key.
- All tests that reference `Advisor.Model` are updated to use `Agents["advisor"].Model`.

**Todo List:**
1. In `internal/app/agent.go`, replace single `SetModelOverride` call with loop.
2. In `internal/app/run_agent.go`, same change.
3. Update `notes/active/advisor-agent-plan.md` comment (settings key changed).
4. Update `docs/guides/writing-a-subagent.md` "Built-in Agents" settings example.
5. Update `docs/guides/advisor-agent.md` settings example.

**Relevant Context:**
- [`internal/app/agent.go`](internal/app/agent.go) — lines ~767-769
- [`internal/app/run_agent.go`](internal/app/run_agent.go) — lines ~67-69
- [`internal/subagent/builtin_advisor.go`](internal/subagent/builtin_advisor.go)

---

### Sub-task 3 — Agent Models panel (`config_agents.go`)

**Status:** [ ] pending

**Intent:**
Implement the `Panel` interface as a list of registered agents. Each row shows
the agent name and either its configured model or "Inherit → <parent>" label.
Pressing Enter on a row opens the existing `ProviderSelector` to pick a model.

**Panel data flow:**

```
Enter()
  └─ subagent.Default().ListConfigs() → sorted rows
  └─ for each agent: setting.Snapshot().GetAgentModel(name) → stored model

HandleKey("enter")
  └─ open ProviderSelector overlay (pass agent name as context)
  └─ return AgentModelPickingMsg{AgentName} to open overlay in parent

On ProviderSelector selection → providerModelSelectedMsg
  └─ panel intercepts → format "vendor/model"
  └─ UpdateAgentModelAt(name, model, userLevel=true) → disk
  └─ emit AgentModelSavedMsg{Name, Model}

HandleKey("delete" / "d")
  └─ clear this agent's override
  └─ UpdateAgentModelAt(name, "", userLevel=true)
  └─ emit AgentModelSavedMsg{Name, ""}
```

**Provider picker integration:**

The `ProviderSelector` is already on `input.Model.Provider`. The panel cannot
open it directly (it's inside a `PanelPopup` which doesn't hold the full
`Model`). Two clean options:

A. Emit a `tea.Cmd` message (`AgentModelPickingMsg{AgentName}`) that the app
   intercepts, sets a "pending agent name" state var, and opens the provider
   selector. When `providerModelSelectedMsg` arrives and the pending name is
   set, the app routes it to the agents panel's save path instead of the normal
   session model path.

B. Give the panel a callback `func(agentName string) tea.Cmd` that the app
   wires at `NewConfigSelector` time.

**Choose option A** — it follows the existing pattern where
`providerModelSelectedMsg` is handled in `UpdateProvider` in `on_provider.go`.
The app model grows one small `pendingAgentModelName string` field.

**"Inherit parent model" row in picker:**

Add a `providerInheritSelectedMsg` message type emitted when the user
selects the special "Inherit" entry at the top of the model list. This clears
the agent's model override.

**Expected Outcomes:**
- `internal/app/input/config_agents.go` implements `Panel`.
- `Title() string` returns `"agents"`.
- `Enter()` reads `subagent.Default().ListConfigs()` and the current settings.
- Rows are sorted alphabetically by agent name.
- `Render(w, h)` shows a list with the agent name, a padded column, then the
  effective model (configured value, or dim "Inherit → <parentModel>").
- `Dirty()` returns true while the picker is open (before save).
- `HintLine()` returns key hints.
- `HandleKey` handles up/down navigation, enter (emits `AgentModelPickingMsg`),
  and delete/d (clear override, save immediately).
- All saves use `setting.UpdateAgentModelAt`.

**Relevant Context:**
- [`internal/app/input/config_appearance.go`](internal/app/input/config_appearance.go) — `Panel` pattern
- [`internal/app/input/panel_popup.go`](internal/app/input/panel_popup.go) — `Panel` interface
- [`internal/app/input/on_provider.go`](internal/app/input/on_provider.go) — `providerModelSelectedMsg`
- [`internal/subagent/registry.go`](internal/subagent/registry.go) — `ListConfigs()`
- [`internal/setting/loader.go`](internal/setting/loader.go) — `UpdateAgentModelAt` (new)

---

### Sub-task 4 — Wire panel into `/config` and app model

**Status:** [ ] pending

**Intent:**
Plug the new panel into `NewConfigSelector`, add the `AgentModelPickingMsg`
message type, extend `OverlayDeps` or `SelectorDeps` with what the panel needs
(parent model ID, agent registry), and handle the save/clear messages in the
app to call `executor.SetModelOverride`.

**Expected Outcomes:**

1. `NewConfigSelector` grows a third panel:
   ```go
   NewConfigSelector(settings *setting.Settings, agentRegistry AgentRegistry,
                     parentModelID func() string) PanelPopup
   ```
   The agents panel is appended after the permissions panel.

2. `input.AgentModelPickingMsg{AgentName string}` is a new message type in
   `config_agents.go`.

3. `input.AgentModelSavedMsg{Name, Model string}` is emitted after each save
   so the app calls `executor.SetModelOverride(name, model)` live.

4. In `internal/app/update.go` (or the message handler that owns the config
   popup), handle:
   - `AgentModelPickingMsg` → store `pendingAgentModelName` on the app model,
     open `ProviderSelector` with a mode that shows all connected providers.
   - `AgentModelSavedMsg` → call `executor.SetModelOverride(name, model)` on
     the live executor so newly spawned agents use the new model immediately.

5. When `providerModelSelectedMsg` arrives AND `pendingAgentModelName != ""`:
   - Format as `"vendor/model"` (or bare model id if same provider as parent).
   - Route to `AgentModelSavedMsg` instead of `handleProviderModelSelected`.
   - Clear `pendingAgentModelName`.

6. A special "Inherit" sentinel in the picker: add an `InheritProviderModel`
   row at the very top of the Models tab when opened in agent-model mode.
   Selecting it emits `AgentModelSavedMsg{Name, ""}`.

7. `SelectorDeps` (model.go) gains what the agents panel needs:
   - `AgentRegistry AgentRegistry` (already present)
   - A `parentModelID func() string` for the "Inherit → X" label.

**Relevant Context:**
- [`internal/app/input/model.go`](internal/app/input/model.go) — `SelectorDeps`, `New()`
- [`internal/app/input/panel_popup.go`](internal/app/input/panel_popup.go) — `NewConfigSelector`
- [`internal/app/input/runtime.go`](internal/app/input/runtime.go) — `OverlayDeps`
- [`internal/app/update.go`](internal/app/update.go) — message dispatch
- [`internal/app/agent.go`](internal/app/agent.go) — executor build site

---

### Sub-task 5 — Tests

**Status:** [ ] pending

**Intent:**
Unit-test the settings layer and the panel logic. No TUI rendering tests needed
for the initial ship.

**Required test coverage:**

| Test | Location | What it covers |
|---|---|---|
| `TestAgentModelSettingsMerge` | `internal/setting/merger_test.go` | overlay wins, empty base, empty overlay |
| `TestAgentModelSettingsClone` | `internal/setting/settings_test.go` | deep copy; mutating clone doesn't affect original |
| `TestUpdateAgentModelAt` | `internal/setting/loader_test.go` | round-trip through `updateSettingsFile` |
| `TestMigrateAdvisorField` | `internal/setting/loader_test.go` | old `"advisor"."model"` JSON migrates to `"agents"."advisor"."model"` |
| `TestGetAgentModel` | `internal/setting/settings_test.go` | missing key returns "", set key returns value |
| `TestExecutorReceivesAgentOverrides` | `internal/subagent/executor_test.go` | `SetModelOverride` range populates `modelOverrides` correctly |

**Relevant Context:**
- [`internal/setting/autopilot_test.go`](internal/setting/autopilot_test.go) — existing pattern
- [`internal/subagent/executor_test.go`](internal/subagent/executor_test.go) — existing patterns

---

## Dependency Order

```
Sub-task 1 (settings schema) ──► Sub-task 2 (executor wiring)
                              ──► Sub-task 3 (panel impl)
                                       ──► Sub-task 4 (app wiring)
                                                ──► Sub-task 5 (tests, all deps)
```

Sub-tasks 3 and 2 can proceed in parallel after Sub-task 1.

---

## What Does NOT Change

- `Executor.modelOverrides` and `SetModelOverride` — already generic, untouched.
- The `ProviderSelector` — reused as-is; a small mode bit routes its selection.
- The `/config` popup shell — `Panel` interface and `PanelPopup.Render()` are untouched.
- `RunBackground`, `Run`, and all subagent execution paths — no change.
- Already-running subagent tasks — model overrides only apply at spawn time.
