# Advisor Agent

The `advisor` is a built-in, read-only subagent that provides second opinions
on difficult coding and architectural decisions. It reads the repository but
never modifies it.

## Purpose

The advisor's role is to help the parent agent or worker think through hard
problems — not to implement solutions. It returns a concise recommendation with
reasoning, trade-offs, and risks, structured for easy consumption.

## When to Invoke

**Do invoke the advisor when:**

- Multiple implementation approaches exist and the trade-offs are non-trivial.
- The current approach is stuck after repeated failures.
- Concurrency, security, or correctness implications need careful analysis.
- An architectural decision has significant long-term consequences.
- The user explicitly requests a second opinion or deeper analysis.

**Do NOT invoke the advisor for:**

- Simple edits or routine tasks.
- Tasks where the correct path is already clear.
- Single-file changes with no design questions.

## On-Demand Only

The advisor is never called automatically. The parent agent decides when to
spawn it based on task complexity. Simple tasks should not invoke it.

## Async Launch Pattern

When the advisor's analysis is independent of other in-flight work, launch it
asynchronously so its latency does not block other useful exploration:

```
advisorTask = Agent(name="advisor", run_in_background=true, prompt=<question>)

// Continue independent exploration / other subagent work here

advisorResult = wait for advisorTask completion
```

Fan-out example — advisor runs concurrently with exploration:

```
┌─ explore backend ──────┐
parent ────┼─ explore tests ───────┼──→ fan-in → synthesise
           └─ advisor (analysis) ──┘
```

The advisor counts toward the normal read-only concurrency limit.

## Response Structure

The advisor always responds with:

1. **Recommendation** — one or two sentences stating the preferred approach.
2. **Reasoning** — concise trade-off analysis explaining why this approach wins.
3. **Risks / Caveats** — correctness, concurrency, security, or design risks.
4. **Relevant Files** — *(optional)* key files and a brief note for each.

## Tools

The advisor has access to `Read`, `Grep`, and `Glob` only. It operates in
`explore` (read-only) mode and cannot write, edit, or run commands.

## Model Override

By default the advisor inherits the session model. To use a stronger reasoning
model, set `advisor.model` in `settings.json`:

```json
{
  "advisor": {
    "model": "anthropic/claude-opus-4-7"
  }
}
```

Accepted forms — same as any agent definition's `model:` field:

| Form | Example | Behaviour |
|---|---|---|
| Alias | `opus` | Resolved on the parent provider |
| Bare model id | `claude-opus-4-7` | Served by the parent provider |
| `vendor/model` | `anthropic/claude-opus-4-7` | Routed to the named connected provider |
| Empty / `inherit` | *(unset)* | Inherits the session model |

The intended configuration is:

```
main / worker → fast or normal coding model
advisor       → stronger reasoning model
```

## Customising the Built-in

The compiled-in `advisor` definition can be overridden by placing a file at:

| Scope | Path |
|---|---|
| Project | `.san/agents/advisor.md` |
| User | `~/.san/agents/advisor.md` |

A filesystem definition always wins over the compiled-in default.

## Example Prompt

When spawning the advisor, provide the relevant question, key findings, and any
file references. Do not copy the entire parent conversation.

```json
{
  "name": "advisor",
  "description": "Architectural advice on session storage",
  "prompt": "We are deciding between two approaches for session transcript storage: (A) append-only log with periodic compaction, or (B) snapshot + delta. Key files: internal/session/store.go, internal/session/transcript/. The main concern is resumability after crash and concurrent write safety. Which approach fits better with the existing design, and what are the correctness risks?"
}
```

## See Also

- [`packages/subagent.md`](../packages/2-feature/subagent.md) — registry and executor design.
- [`guides/writing-a-subagent.md`](writing-a-subagent.md) — how to define custom agents.
- [`concepts/permission-model.md`](../concepts/permission-model.md) — explore mode details.
