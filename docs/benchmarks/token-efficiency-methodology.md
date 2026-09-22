# Token-Efficiency Benchmark Methodology

> San upgrade — OMP2 Task Batch + Tura Command Batch

These benchmarks measure whether the Phase 1–3 optimizations actually reduce
total task trajectory cost:

```
total tokens ≈ context per request × model round-trips + useful output
```

Reducing only one term is not enough. We measure **complete task trajectories**,
not individual tool outputs.

---

## Instrumentation

Every session now records live metrics via `core.SessionMetrics`.
Read them with `/debug metrics` at any point during a session, or at the end.

```
/debug metrics

Session metrics
  model calls:    4
  tool calls:    12
  command steps:  3
  subagent calls: 2
  input tokens:   18320
  cache tokens:   14200
  output tokens:  1840
  total tokens:   20160
  duration:       42.3s
```

**model calls** — the primary optimization target: how many times did the model
need to produce a new response?

**command steps** — commands executed by the Batch tool (zero for pre-Phase-2 baselines).

**subagent calls** — agent tasks launched via Agent `tasks[]` (zero for pre-Phase-1 baselines).

---

## Scenario A — Repository exploration

**Task:** `find how provider/model routing works`

**Baseline (pre-upgrade):**
- Model iterates: Glob → Read → Read → Grep → Read → summarize
- Each Read/Grep result triggers a new model turn
- Expected: 5–8 model calls, all sequential

**Target (post-upgrade):**
- Model issues one Agent `tasks[]` batch: explore routing + explore settings wiring
- Subagents work in parallel
- Model receives compact structured yield
- Expected: 2–3 model calls (one batch turn + follow-up)

**Measure:**
```
model_calls        (want: ≤3 vs baseline ~6)
input_tokens       (want: lower — no repeated context in children)
subagent_calls     (want: ≥2 — parallel exploration confirmed)
duration           (want: ≈ or lower due to parallelism)
```

---

## Scenario B — Independent investigation

**Task:** `inspect provider routing + settings + session wiring`
Three independent semantic concerns.

**Baseline:** Three sequential Agent calls, each blocking on the previous.

**Target:** One Agent `tasks[]` call with three independent items.

**Measure:**
```
model_calls        (want: 1 batch call vs 3 sequential)
subagent_calls     (want: 3)
duration           (want: ~1/3 of baseline — parallel execution)
input_tokens       (want: lower — shared context sent once, not 3×)
```

---

## Scenario C — Validation workflow

**Task:** `go test ./... && go vet ./... && go build ./...`

**Baseline:**
```
MODEL: run go test
tool result
MODEL: run go vet
tool result
MODEL: run go build
```
= 3 model calls for 3 deterministic operations

**Target:**
```
MODEL: Batch [go test, go vet, go build]
  runtime executes graph
compact result
MODEL: see results
```
= 1 model call for 3 operations

**Measure:**
```
model_calls        (want: 1 vs 3)
command_steps      (want: 3 — all three ran)
tool_calls         (want: 1 vs 3)
input_tokens       (want: lower — no intermediate "test passed, now run vet" reasoning)
```

---

## Scenario D — Full implementation cycle

**Task:** explore a package → edit a file → format + test + vet

**Baseline:**
```
Glob/Read (1–2 turns)
Edit (1 turn)
Bash gofmt (1 turn)
Bash go test (1 turn)
Bash go vet (1 turn)
```
= 5–7 model calls

**Target:**
```
Glob/Grep (1 turn)
Read targeted file (1 turn)
Edit (1 turn)
Batch [gofmt, go test, go vet] (1 turn)
```
= 4 model calls (edit + batch replaces 3 separate bash turns)

**Measure:**
```
model_calls        (want: 4 vs 6)
command_steps      (want: 3 — all post-edit steps in one batch)
input_tokens       (want: lower — no gofmt/vet output in context between steps)
success            (must remain correct — tests pass, vet clean)
```

---

## How to run

1. Start San with a connected provider.
2. Run the task prompt.
3. At completion type `/debug metrics`.
4. Record all eight fields.
5. Compare against baseline (repeat prompt on a fresh session without batch tools).

For the **batch vs no-batch** comparison, use `/tools` to toggle the Batch and
Agent tools off, then re-run.

---

## Success criteria

Per the spec (§38):

```
same or better correctness
fewer model round trips
lower total input tokens
lower total tokens
same or lower wall-clock time for parallelizable work
negligible idle overhead
```

**Reject** any "optimization" that:
- increases total tokens while only reducing one tool's output
- reduces model calls at the cost of correctness
- adds more than ~5ms startup overhead (measure with `time san --version`)
