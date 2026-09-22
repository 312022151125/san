# Hindsight Memory + SearchXNG Web Search Integration Plan

## Overview

Add two **optional** backends to San:

| Backend | Role | OMP reference |
|---|---|---|
| **Hindsight** | Long-term agent/project memory: `retain` / `recall` / `reflect` | `packages/coding-agent/src/hindsight/*`, `docs/memory.md` |
| **SearchXNG / SearXNG** | External web search behind the existing `WebSearch` tool | `packages/coding-agent/src/web/search/providers/searxng.ts` |

Both are lazy, failure-tolerant, and add zero cost when disabled. This plan
adapts OMP's *behavior* to San's existing architecture — it does not copy
OMP's TypeScript session-state machinery (retain queues, mental models,
bank management UI).

### Naming note (corrects the previous interpretation)

- **Hindsight** is correct: the remote memory server, default
  `http://localhost:8888`, API under `/v1/default/banks/{bank}/...`.
- OMP's search backend is **SearXNG** (impl file `searxng.ts`, env vars
  `SEARXNG_ENDPOINT`/`SEARXNG_TOKEN`, settings key `searxng.*`). "SearchXNG"
  refers to the same self-hosted metasearch engine. San uses the settings
  value `searxng` and the label "SearXNG" so env vars and docs
  interoperate with OMP's.

### What already exists in San (build on it, don't parallel it)

- `internal/search` — pluggable `Provider` interface with Exa (default, no
  API key), Tavily, Serper, Brave. Providers are constructed **per call**
  (`search.Preferred()` inside `WebSearchTool.Execute`) — already lazy.
- `internal/tool/web/websearch.go` — `WebSearch` tool with compact schema
  (`query`, `num_results`, `allowed_domains`, `blocked_domains`) formatting
  results as `title + url + snippet` markdown bullets.
- `searchProvider` setting + `/search` TUI selector
  (`internal/app/input/on_search.go`).
- `disabledTools` setting already removes any tool schema entirely.
- `internal/selflearn` keeps its own local file memory
  (`~/.san/projects/<cwd>/memory/`) — untouched; Hindsight is a separate
  optional backend.

### Deviations from the literal spec (adapted to San)

1. **WebSearch is not re-gated behind a `web_search.backend` switch.** San
   ships WebSearch as a core tool (Exa works zero-config); making it opt-in
   would regress existing behavior. SearXNG becomes a new *provider* in the
   existing selector. Users who want no web search already have
   `disabledTools: {"WebSearch": true}`, which drops the schema. SearXNG
   stays lazy — constructed only when `WebSearch` runs.
2. **Settings names follow San conventions** (flat camelCase keys plus
   nested sections for multi-field groups, like `selfLearn`/`subagents`):
   a nested `memory` section; search keeps flat `searchProvider` plus new
   flat `searchUrl`/`searchMaxResults`.
3. **`retain` is parent-only** — add it to `tool.parentOnlyTools`, making
   the "no writes from subagents" rule structural (blocked by the gate even
   on a hallucinated call), not just a convention.

---

## Section 1 — Hindsight memory backend

### 1.1 New package: `internal/memory` (feature layer)

Small idiomatic Go, no SDK dependency (hand-rolled HTTP — OMP itself
replaced `@vectorize-io/hindsight-client` with a fetch client for the same
reason):

```
internal/memory/
  client.go       — HTTP client: Retain, Recall, Reflect, EnsureBank
  backend.go      — Enabled() gate + lazy Backend construction from settings
  scoping.go      — global / project bank-id derivation
  client_test.go
  scoping_test.go
```

**Client protocol** (from OMP `hindsight/client.ts`):

| Op | Endpoint | Body |
|---|---|---|
| Retain | `POST /v1/default/banks/{bank}/memories` | `{"items":[{"content","context","tags"}]}` — batch in one request |
| Recall | `POST /v1/default/banks/{bank}/memories/recall` | `{"query","max_tokens","budget","tags","tags_match"}` → `{"results":[{"text","type","mentioned_at"}]}` |
| Reflect | `POST /v1/default/banks/{bank}/reflect` | `{"query","context","budget"}` → `{"text"}` |
| EnsureBank | `PUT /v1/default/banks/{bank}` | best-effort, once per process per bank, before first write; failures swallowed |

- Headers: `Content-Type: application/json`, optional
  `Authorization: Bearer <token>` (from `HINDSIGHT_API_TOKEN` via
  `secret.Resolve`).
- Timeouts via `context.WithTimeout` on a shared `http.Client`:
  recall 30s, retain 60s, reflect 120s (OMP defaults). The caller's `ctx`
  flows into `http.NewRequestWithContext`, so cancellation propagates.
- No shared mutable state: a `Backend` value is immutable after
  construction; concurrent `Recall`/`Reflect` are safe read-only HTTP.
- Every failure returns an error to the caller; nothing panics, nothing
  retries in the background.

### 1.2 Settings

New nested section in `setting.Data`, mirroring the `SubagentSettings`
pattern (struct + resolved accessors + `Validate` + field merge in
`merger.go` + clone coverage in `clone_test.go`):

```json
{
  "memory": {
    "backend": "off",
    "url": "http://localhost:8888",
    "scope": "project",
    "autoRecall": true,
    "autoRetain": false,
    "maxResults": 5
  }
}
```

- `backend`: `"off"` (default) | `"hindsight"`.
- `url`: default `http://localhost:8888` — Hindsight/OMP standard, not a
  San invention. Override via `HINDSIGHT_API_URL`; token via
  `HINDSIGHT_API_TOKEN` (resolved through `secret.Resolve`, consistent
  with search provider keys). Only these two env vars in v1; OMP's other
  16 `HINDSIGHT_*` knobs stay out unless proven necessary.
- `scope`: `"project"` (default) | `"global"` — the two modes required; no
  OMP `per-project-tagged` hybrid.
- `autoRecall`: default `true` (only active when backend ≠ off).
- `autoRetain`: default `false` — retention stays a deliberate act by
  default.
- `maxResults`: recall cap, default 5.

### 1.3 Project scoping (`scoping.go`)

Follow OMP's `per-project` derivation, kept simple:

1. Find the git primary checkout root from cwd (so linked worktrees of one
   repo resolve to the same bank); fall back to cwd outside a repo.
2. Label = lowercased basename of that root (`~/code/San` → `san`).
3. `scope=project` → bank id `san-{label}` (OMP appends `-{label}` to its
   base bank; San prefixes with `san-` to avoid colliding with other
   Hindsight clients sharing the server).
4. `scope=global` → bank id `san`.

No tag filters, no shared-bank-plus-tagging — a project bank is simply a
different bank id, which is what OMP's `per-project` mode does and is the
simplest correct isolation.

### 1.4 Memory tools: `retain`, `recall`, `reflect`

New package `internal/tool/memory` following the Evolve conditional-tool
pattern (`internal/tool/evolve`):

- Register the three tools in `init()` so dispatch works
  (`tool.Register`).
- Add their names to `manageableExtraToolOrder` (visible in `/tools`, not
  in the default schema) — **not** `builtinToolOrder`.
- Schemas are injected per-turn via `BuildParams.ExtraTools`, exactly like
  `selfLearnExtraTools()`: `m.memoryExtraTools()` returns
  `[retain, recall, reflect]` when `memory.backend == "hindsight"`, nil
  otherwise. Zero schema bytes when off.
- Each tool's `Execute` re-checks the backend gate and returns an error
  result if memory was disabled mid-session — no cached client, no stale
  state. The HTTP call is made on demand (lazy per call, like
  `search.Preferred()` today).

**Schemas (compact):**

```text
recall   { query: string, limit?: int }
retain   { items: [{ content: string, context?: string }] }
reflect  { query: string, context?: string }
```

Result formatting:

- `recall` → `- <text> [<type>] (<YYYY-MM-DD>)` bullets (OMP's
  `formatMemories`), capped at `memory.maxResults`; `No relevant
  memories found.` when empty. No raw JSON back to the model.
- `retain` → `N memories stored.`; errors surfaced as tool errors (San,
  unlike OMP, stores synchronously — no background queue to lose items).
- `reflect` → the server's synthesized `text`, or `No relevant
  information found to reflect on.`

**`retain` is parent-only**: add `ToolRetain` to `parentOnlyTools` in
`internal/tool/set.go`. Subagent allow-lists never contain it, and the
gate rejects hallucinated calls (same protection as `Agent`/`Cron`).

### 1.5 Auto-recall (once per user task)

Hook: `dispatchSubmission` path in `internal/app/update_submit.go`, before
the first turn of a **new user task**, guarded by a session-level
`sync.Once`-style flag reset per user submission:

- Only when `memory.backend == "hindsight" && autoRecall` and the flag is
  clear for this task.
- Query = current user message truncated to ~800 chars (OMP's
  `recallMaxQueryChars = 800`).
- Run as a `tea.Cmd` goroutine with `context.WithTimeout(ctx, 30s)`;
  result arrives as a message and is injected as a **background context
  block**, not instructions — reuse `reminder.EnqueueOnce` /
  `reminder.WrapMemory(scope, body)` so it renders like other
  session-level context and never re-fires for the same content.
- Failure or timeout → log via `internal/log`, skip injection, session
  proceeds normally. Never blocks or errors the turn.
- The flag prevents any second auto-recall inside the same agent loop;
  explicit `recall` tool calls are unaffected.

Subagents **never** auto-recall (matches OMP: subagents alias the parent
client for explicit calls only). The parent's optional auto-recall is the
only automatic injection point.

### 1.6 Auto-retain (default off)

When `autoRetain` is on (opt-in), retain at **turn boundaries only** —
after a user turn completes, on the orchestration (parent) side:

- Content = a **consolidated digest**, not the transcript: the user's
  request + a trimmed final assistant answer. Never tool output, never
  Read/Grep results, never per-tool-call logs. If nothing durable was
  produced (short ack, error turn), skip the retain entirely.
- Single-flight: an in-flight retain flag prevents duplicate concurrent
  retains when parallel subagents finished the same task.
- Default off ⇒ zero background HTTP in a fresh install.

### 1.7 Reflect

Exposed as a tool only; never auto-invoked (spec: reflection must not run
for routine coding tasks). The advisor agent gets it (see Section 3) so
synthesis happens when a decision genuinely needs accumulated knowledge.

### 1.8 Docs & package map

- New feature doc: `docs/packages/2-feature/memory.md` (purpose,
  entrypoints, flow, config, tests, pitfalls — per AGENTS.md doc rules).
- Add `internal/memory` to `docs/reference/package-map.md` (feature layer)
  and the feature list in `docs/reference/dependency-rules.md`; layer
  check must pass (`memory` → `core`+`setting`+`secret` only; tools under
  `internal/tool/*` stay thin adapters per rule 6).
- Update `docs/packages/2-feature/setting.md` with the new `memory`
- Update `docs/packages/2-feature/setting.md` with the new `memory`
  section.

---

## Section 2 — SearXNG (SearchXNG) web search

### 2.1 New provider: `internal/search/searxng.go`

Implements the existing `Provider` interface — nothing else changes:

```go
const ProviderSearXNG ProviderName = "searxng"
```

- `RequiresAPIKey() false`; `EnvVars() ["SEARXNG_ENDPOINT" "SEARXNG_TOKEN"]`
  (OMP-compatible names).
- Endpoint resolution order (OMP's): settings `searchUrl` →
  `SEARXNG_ENDPOINT` env → unavailable. No hardcoded default URL — a
  self-hosted instance has no standard port (OMP leaves `searxng.endpoint`
  unset by default).
- Request (OMP `searxng.ts`):
  `GET {endpoint}/search?q=...&format=json` with `Accept: application/json`,
  optional `Authorization: Bearer {token}`. Supported query params kept to
  what San's schema already exposes — `q`, `format`, `language`/`engines`
  only if a clear need appears later; `allowed_domains`/`blocked_domains`
  are filtered client-side by the existing `matchesDomainFilter`.
- Response mapping → `SearchResult{Title, URL, Snippet}` from
  `results[].title/url/content` (fall back to `snippet`), snippets
  truncated with the existing `truncateSnippet`. Cap at
  `SearchOptions.NumResults`. Raw JSON never leaves the provider.
- Per-call `http.Client{Timeout: getTimeout(opts)}` +
  `http.NewRequestWithContext` — identical to `tavily.go`/`brave.go`
  (cancellation + timeout already handled by the shared helpers).
- Register in `factory.go` `CreateProvider`, `AllProviders()` metadata
  (display name "SearXNG (self-hosted)", no API key), and
  `GetDefaultProvider` priority stays Exa-first (SearXNG only used when
  explicitly selected — it needs a reachable instance).

### 2.2 Settings + selector

- `searchProvider: "searxng"` flows through the existing
  `SetSearchProvider`/`Preferred()` path unchanged.
- New flat keys on `setting.Data`:
  - `searchUrl` (`string`) — instance endpoint; also serves future
    URL-based providers.
  - `searchMaxResults` (`int`, 0 = current default 10) — global result
    cap passed as `SearchOptions.NumResults`; the tool's `num_results`
    param can still lower it per call.
- `/search` TUI selector (`on_search.go`) picks up SearXNG automatically
  from `AllProviders()`; its availability check becomes "`searchUrl` or
  `SEARXNG_ENDPOINT` set" instead of an API-key check (add an `EnvVars`
  alternative — small `availableViaURL` flag on the item — and let the
  panel show the endpoint as the "credential" line, reusing the existing
  row layout; entering the endpoint can write `searchUrl` via the same
  text-input sub-view used for API keys).

### 2.3 Tool schema stays as-is

The existing `WebSearch` schema is already the compact `query` /
`num_results` (+ domain filters with clear practical value). No new
parameters, no SearchXNG API surface exposed. Result formatting (title,
url, snippet bullets) already matches the spec.

Failure: provider error → existing `search failed: ...` tool error result;
San continues normally.

---

## Section 3 — Agent integration

Tool lists per agent (`internal/subagent/builtin_*.go`):

| Agent | Tools | Change |
|---|---|---|
| explore | Read, Grep, Glob | none |
| worker | Read, Grep, Glob, Edit, Write, Bash | none |
| reviewer | Read, Grep, Glob, Bash | none |
| advisor | Read, Grep, Glob, **WebSearch**, **recall**, **reflect** | extend `AllowTools` |
| parent/main | existing set + **retain/recall/reflect** via `ExtraTools` | conditional |

Mechanics:

- Advisor: append `tool.ToolWebSearch`, `ToolRecall`, `ToolReflect` to its
  compiled-in `AllowTools`. Allow-lists filter against *registered*
  schemas, so:
  - WebSearch resolves always (it's in `builtinToolOrder`).
  - recall/reflect resolve **only** when memory tools are injected into
    the subagent's tool set. Subagent sets get `ExtraTools`-style
    injection the same way the main agent does: extend
    `subagent.newAgentToolSet` / executor schema build to append
    `memory.Schemas()` when the backend is enabled (same gate, one
    helper). When memory is off the names sit in the allow-list but match
    no schema — harmless, zero cost.
- `retain` never appears in any subagent allow-list and is additionally
  blocked by `parentOnlyTools` (belt and braces).
- WebSearch is deliberately *not* added to explore/worker/reviewer —
  external search stays an advisor/parent capability per the spec's
  defaults.

---

## Section 4 — Parallel safety

- **Read concurrency**: `recall` and `WebSearch` are stateless HTTP;
  multiple explore/advisor agents may call them concurrently. The
  `Backend` and provider values are immutable; `http.Client` is
  concurrency-safe. No shared mutable model/provider state is introduced.
- **Write safety**: `retain` is parent-only → no concurrent subagent
  writes, no duplicate auto-retention from parallel workers. Auto-retain
  additionally runs single-flight on the parent at turn boundaries.
- **Recall dedup**: session-level flag (atomic bool) ensures one
  auto-recall per user task even if submission paths race; explicit
  `recall` calls are unaffected.
- **Cancellation**: every request uses `http.NewRequestWithContext` with
  the caller ctx + operation timeout — Bash/agent cancellation propagates
  to in-flight Hindsight/SearXNG calls.
- **Failure isolation**: any backend error is caught at the tool/auto-hook
  boundary, logged, and turned into a soft skip — San never fails a turn
  because a memory or search server is down.

---

## Section 5 — Settings (final shape)

```json
{
  "memory": {
    "backend": "off",
    "url": "http://localhost:8888",
    "scope": "project",
    "autoRecall": true,
    "autoRetain": false,
    "maxResults": 5
  },
  "searchProvider": "searxng",
  "searchUrl": "http://localhost:8080",
  "searchMaxResults": 5
}
```

- Follows San naming: nested multi-field group (`memory`) + flat
  single-value keys (`searchProvider` already exists). JSON keys are
  camelCase like every other San setting.
- Env overrides: `HINDSIGHT_API_URL`, `HINDSIGHT_API_TOKEN`,
  `SEARXNG_ENDPOINT`, `SEARXNG_TOKEN` — matching OMP/Hindsight standards
  instead of inventing `SAN_*` names, resolved through `secret.Resolve`
  like the existing search API keys. (`SAN_*` child-process export is a
  separate mechanism and stays untouched.)
- Defaults: memory off, Exa search unchanged — a fresh install behaves
  exactly as today.
- Merge/validate/clone hooks added where `SubagentSettings` has them
  (`merger.go`, `Validate`, `clone_test.go`, `malformed_settings_test.go`).

---

## Section 6 — TUI settings

Add two panels to the existing `/config` `PanelPopup`
(`NewConfigSelector` in `internal/app/input/panel_popup.go`), reusing
`panel_form.go` rows (`rowSubHeader`, `rowBool`, `rowInt`, `rowText`) and
the appearance-panel persistence pattern:

```
Memory
  Backend             Off / Hindsight      (radio, rowBool-style toggle)
  Server              http://localhost:8888 (rowText → memory.url)
  Scope               Project / Global     (radio)
  Auto Recall         On                   (rowBool)
  Auto Retain         Off                  (rowBool)
  Max Results         5                    (rowInt)

Web Search
  Provider            Exa / … / SearXNG    → existing /search selector,
                                            or a link-out row to it
  Server              (endpoint)           (rowText → searchUrl)
  Max Results         5                    (rowInt → searchMaxResults)
```

- Web-search provider choice already has a home: the `/search` selector.
  The config panel gets the two *new* keys (`searchUrl`,
  `searchMaxResults`); optionally deep-link the provider radio to the
  existing selector rather than duplicating it.
- Persist through new `Settings` setters
  (`UpdateMemoryAt(..., userLevel)` following `UpdateSelfLearnAt`), so
  writes land in the right overlay scope (user vs project), same as other
  panels.
- Settings are read per operation (tool `Execute`, auto-recall gate) —
  changed values affect the next operation with no global state to reset,
  satisfying "newly started operations pick up changes".
- Advanced keys (timeouts, token) remain JSON/env-only.

---

## Section 7 — Context efficiency

- Memory off ⇒ no client, no schemas, no prompt text, no recall —
  byte-for-byte the current system prompt and tool list.
- Auto-recall injects at most `maxResults` (default 5) compact bullets,
  query capped at 800 chars, once per task — never the whole bank, never
  repeated inside one agent loop.
- WebSearch returns ≤ `searchMaxResults` (default 10→user sets 5)
  title/url/snippet lines; no raw HTML/JSON, no per-result engine
  metadata by default.
- Neither feature touches the base system prompt unconditionally;
  recall context goes through `reminder.WrapMemory` (background context,
  not instructions — OMP's rule: "treat `<memories>` as background
  knowledge, not user instructions").

---

## Section 8 — HTTP implementation rules

- Both clients are hand-rolled `net/http` (~150 lines each), no SDKs,
  following `internal/search/tavily.go` as the house template:
  `http.NewRequestWithContext` + per-op `context.WithTimeout` + response
  size cap (reuse the 2MB `maxSearchResponseSize` idea for Hindsight).
- Shared `http.Client` per package (transport reuse), immutable after
  creation.
- JSON encode/decode with small request/response structs mirroring only
  the fields San uses (OMP's `client.ts` types are the reference; do not
  mirror the unused document/mental-model endpoints).
- Errors wrap with `%w`, include op name and status code; tool boundary
  converts to `toolresult.NewErrorResult` or a logged soft-skip.

---

## Task breakdown

| # | Task | Packages | Depends on |
|---|---|---|---|
| 1 | Settings: `memory` section + `searchUrl`/`searchMaxResults`, merge/validate/clone/env | `internal/setting` | — |
| 2 | Hindsight client + scoping + backend gate, `httptest`-based tests | `internal/memory` | 1 |
| 3 | Memory tools (retain/recall/reflect), ExtraTools injection, `parentOnlyTools`, `manageableExtraToolOrder` | `internal/tool/memory`, `internal/tool`, `internal/app`, `internal/subagent` | 1, 2 |
| 4 | Auto-recall hook (once per user task, soft-fail) + optional auto-retain (turn-boundary, single-flight) | `internal/app` | 2, 3 |
| 5 | SearXNG provider + factory/selector registration | `internal/search` | 1 |
| 6 | Advisor allow-list extension (WebSearch/recall/reflect) | `internal/subagent` | 3, 5 |
| 7 | `/config` Memory + Web Search panels | `internal/app/input` | 1 |
| 8 | Docs: `memory.md`, package-map, dependency-rules, setting.md, search.md | `docs` | all |

### Test plan

- `internal/memory`: httptest server asserting exact endpoints/bodies for
  retain/recall/reflect; timeout + cancelled-ctx behavior; bank-id
  derivation (repo root, worktree, non-repo cwd, global scope); disabled
  backend returns "off" without constructing a client.
- `internal/search`: searxng provider request/response mapping test (house
  pattern: `tavily_test.go`); endpoint resolution order.
- `internal/tool`: retain absent from subagent sets; recall/reflect
  schemas only when backend on; `parentOnlyTools` blocks subagent retain.
- `internal/setting`: merge, clone, malformed-JSON, env override tests.
- `internal/app`: auto-recall fires exactly once per task; failure path
  leaves the turn untouched.
- Layer check: `tools/layercheck` passes with `internal/memory` registered
  in package-map.

### Explicitly out of scope (keep San light)

- OMP's retain queue/debounce (batch 16, 5s) — San retains synchronously
  or not at all.
- Mental models, `memory_edit`, documents API, `/memory` slash-command
  suite, bank mission setup UI.
- Mnemopi/local backends, `per-project-tagged` scoping, recall budgets
  (`low/mid/high`) — use `maxResults` + `max_tokens` only.
- Raw transcript retention in any form.

