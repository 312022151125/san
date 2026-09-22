---
package: github.com/genai-io/san/internal/memory
layer: feature
---

# memory

Optional long-term memory backend (Hindsight): a lazy HTTP client for the
`retain` / `recall` / `reflect` operations plus project-scoped bank
derivation. Entirely off unless `settings.memory.backend = "hindsight"`.

## Purpose

`memory` gives San durable cross-session memory without making San depend
on a memory server. When the backend is off there is no client, no
connection, no tool schema, and no prompt text — zero runtime overhead
(one settings read at each gate). When on, the app (auto-recall /
auto-retain) and the `internal/tool/memory` adapters call a small
hand-rolled HTTP client over the slice of the Hindsight API San uses.

This package is distinct from `internal/selflearn`'s local file memory
(`MEMORY.md` under `~/.san/projects/<cwd>/memory/`): selflearn is
agent-written local files; Hindsight is a remote, bank-scoped server. They
can both be on.

## Contract

```go
// Gate: does a backend exist at all?
func Enabled() bool                        // settings.Memory().Enabled()
func Settings() setting.MemorySettings

// Lazy construction (nil when disabled). Keyed by config; Reset drops it.
func Get(cwd string) *Backend
func Reset()

type Backend struct { Client *Client; BankID string }

// Client operations — all take the caller's ctx (cancellation propagates)
// and apply an op timeout internally (recall 30s, retain 60s, reflect 120s).
func (c *Client) Retain(ctx, bankID string, items []RetainItem, tags []string) error
func (c *Client) Recall(ctx, bankID, query string, limit int) ([]Memory, error)
func (c *Client) Reflect(ctx, bankID, query, extra string) (string, error)
func (c *Client) EnsureBank(ctx, bankID string)   // best-effort, once per bank

// Scoping: "project" (default) → "san-{label}-{hash}", "global" → "san".
func BankID(cwd, scope string) string

// Shared rendering used by the recall tool and auto-recall.
func FormatBullets(results []Memory, limit int) string
func TruncateRunes(s string, n int) string
```

### Protocol

| Op | Endpoint | Notes |
| --- | --- | --- |
| Retain | `POST /v1/default/banks/{bank}/memories` | one batched `{items:[{content,context}]}` request |
| Recall | `POST /v1/default/banks/{bank}/memories/recall` | `{query,max_tokens}` → `{results:[{text,type,mentioned_at}]}` |
| Reflect | `POST /v1/default/banks/{bank}/reflect` | `{query,context}` → `{text}` |
| EnsureBank | `PUT /v1/default/banks/{bank}` | before first write; failure swallowed |

URL precedence: `HINDSIGHT_API_URL` env > `memory.url` setting > the
Hindsight standard default `http://localhost:8888`. The token comes only
from `HINDSIGHT_API_TOKEN` (env/secret store via `secret.Resolve`) — it is
a credential and never lives in settings.json.

## Scoping

Two modes only (`setting.MemoryScopeProject` / `MemoryScopeGlobal`):

- **project** (default): bank id `san-{label}-{8 hex}` where `label` is the
  lowercased basename of the git primary checkout root (worktrees resolve
  to the main checkout through the `.git` file's `gitdir:` pointer;
  submodules keep their own root) and the hash is FNV-1a of the absolute
  root — same-named repositories stay isolated. Outside a repo, the cwd.
- **global**: one shared `san` bank.

## Lifecycle

`Get` is the only stateful entry: it caches one `Backend` per
(settings, env) key under a mutex and returns nil while disabled. Tool
`Execute` calls, the auto-recall gate, and auto-retain all go through it,
so a settings change (the `/config` Memory panel's save calls `Reset`)
takes effect on the next operation with no agent restart. Failures never
break the session: tools return error results, the auto hooks log and skip.

## Consumers

- `internal/tool/memory` — the `retain` / `recall` / `reflect` tools,
  registered always but schema-gated via `ExtraTools` (nil when off).
- `internal/app/memory_hindsight.go` — the once-per-task auto-recall in
  the submission pipeline and the opt-in auto-retain at `OnTurnEnd`.
  `retain` is parent-only (`tool.parentOnlyTools`): subagents read memory,
  only the orchestration layer writes it.

## Tests

```
internal/memory/client_test.go     — httptest protocol, auth, caps, cancellation, down-server
internal/memory/scoping_test.go    — repo root, worktree, submodule, same-basename isolation, non-repo
internal/tool/memory/memory_test.go — schema gating (off ⇒ no schemas), execute-refusal, parent-only
```

## See Also

- Tools: [`packages/tool.md`](tool.md)
- Settings: [`packages/setting.md`](setting.md)
- App wiring: `internal/app/memory_hindsight.go`
- Plan: `notes/active/hindsight-searchxng-plan.md`
- Layer: `feature`
