# San Agent Guide

This file is the short navigation map for agents and contributors. Keep durable
knowledge in `docs/`; keep this file focused on where to look and what rules to
follow before changing code.

San follows the [AGENTS.md](https://agents.md) standard, so this file is both
the navigation aid for whoever opens the repository and the project
instructions loaded into the running agent's context at session start. Keep it
short: pointers and rules only. Uncommitted personal notes belong in
`AGENTS.local.md` (git-ignored); a subdirectory with its own conventions can
carry its own `AGENTS.md`, and the file nearest the working directory wins.

## Start Here

- Product overview: `README.md`
- Documentation index: `docs/index.md`
- Detailed architecture: `docs/concepts/architecture.md`
- Package map and ownership: `docs/reference/package-map.md`
- Dependency rules: `docs/reference/dependency-rules.md`
- Feature notes: `docs/packages/index.md`
- Development workflow: `docs/operations/development.md`

## Repository Shape

- `cmd/san`: CLI entrypoint and command wiring.
- `internal/app`: Bubble Tea TUI shell, model composition, event routing.
- `internal/core`: stable agent, message, tool, and system-prompt contracts.
- `internal/agent`: agent construction and session-facing runtime setup.
- `internal/llm`: model provider registry, clients, cost and logging helpers.
- `internal/tool`: built-in tool registry, schemas, adapters, and executors.
- `internal/session`: transcript persistence, projection, metadata, resume.
- `internal/task`, `internal/subagent`, `internal/cron`: background work and orchestration.
- `internal/command`, `internal/skill`, `internal/plugin`, `internal/mcp`, `internal/hook`: extension surfaces.
- `internal/setting`, `internal/persona`: configuration and persona overlays.
- `internal/log`, `internal/secret`, `internal/filecache`, `internal/markdown`, `internal/confdir`: infrastructure helpers.
- `internal/selflearn`: background memory and skill review loop.
- `docs`: durable explanations, design decisions, operations, and references.

## Rules

Before editing internal packages, read:

- `docs/reference/dependency-rules.md` — allowed import directions and the
  rule for each layer.
- `docs/design/principles.md` — coding principles for package structure,
  interfaces, tests, and context handling.

Update those files when the rules change. Do not duplicate them here.

## Common Commands

See `docs/operations/development.md` for build / test / lint / format
and the sandbox-friendly `GOCACHE` workaround. Update that file when
commands change. Do not duplicate them here.

## Default Agents

The four built-in lightweight-coding agents:

| Agent    | Role                                      | Notes |
|----------|-------------------------------------------|-------|
| explore  | Fast read-only repository exploration     | Built-in compiled-in default |
| worker   | Coding and implementation                 | Built-in compiled-in default |
| reviewer | Correctness review                        | Built-in compiled-in default |
| advisor  | Difficult reasoning / second opinion      | Built-in compiled-in default |

All four are compiled-in defaults. Any user- or project-level
`.san/agents/<name>.md` overrides the built-in with the same name.
Use `advisor` on-demand for architectural trade-offs, debugging dead ends,
and non-trivial correctness or concurrency questions. See
[`docs/guides/advisor-agent.md`](docs/guides/advisor-agent.md).

## Token-Efficient Orchestration

San has two batching layers for reducing model round trips.

**Agent batch** — use `Agent` with `tasks[]` for parallel independent semantic work:
```json
{ "context": "Shared goal and constraints...",
  "tasks": [
    { "agent": "explore", "name": "A", "task": "..." },
    { "agent": "explore", "name": "B", "task": "..." }
  ] }
```
One model turn fans out N agents concurrently. Shared context is sent once.
Only `explore`-mode agents are allowed during Plan Mode.

**Batch tool** — use `Batch` for deterministic command graphs:
```json
{ "commands": [
    { "id": "build", "command": "go build ./..." },
    { "id": "test",  "command": "go test ./...", "depends_on": ["build"] }
  ] }
```
One tool call runs the full DAG; failed deps skip their dependents automatically.
Available to write-capable agents (`worker`). Rejected entirely in Plan Mode.

See [`docs/concepts/token-efficiency.md`](docs/concepts/token-efficiency.md)
for the full design, including output compaction, session metrics, and permission rules.

## Documentation Rules

- Add or update docs in the same change as architecture or workflow changes.
- Each feature document should list purpose, entrypoints, core packages, flow,
  configuration, tests, and common pitfalls.
- Architecture decision records live in `docs/design/decisions/`.
- File naming rules live in `docs/reference/file-naming.md`.
- Active plans live in `notes/active/`; completed plans move to
  `notes/completed/`.
