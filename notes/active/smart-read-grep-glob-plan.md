# Plan: Smart Read + Grep/Glob Tools

## Overview

Improve San's file-navigation tools in three focused areas:

1. **Smart Read** — augment the existing `ReadTool` so large files return a compact
   head/tail summary instead of silently truncating at 2000 lines, while keeping
   small-file reads identical.
2. **Grep tool** — a new `GrepTool` that prefers `rg` when available and falls back
   to a pure-Go walker, replacing ad-hoc Bash `rg` calls.
3. **Glob tool** — a new `GlobTool` for pattern-based file discovery, replacing
   Bash `find`/`ls` calls with a fast, skip-aware, pure-Go walker.

All work lives inside `internal/tool/fs/`. New tools are registered in
`internal/tool/schema.go` (`builtinToolOrder`) and `internal/tool/perm/decision.go`
(`readOnlyTools`). Tests are added/extended in the same package.

---

## Sub-task 1 — Smart Read head/tail summary for large files

**Status:** [x] done

### Intent
When a caller reads a large file without specifying `offset`/`limit`, return a
compact head/tail summary instead of dumping the first 2000 lines. The model
gets orientation (total size, total lines, and the concrete first/last lines it
needs to anchor its next move) and is explicitly told to use `Grep` to locate
a symbol then `Read(offset, limit)` for the relevant window. Small files and
explicit offset/limit reads are completely unaffected.

This is intentionally not an AST-based structural outline — no declaration
parsing, no language detection. The v1 goal is token economy and a clear
workflow hint. The natural v1 workflow once Grep and Glob exist:

  Read(big_file)           ->  head/tail summary + "use Grep to locate symbols"
  Grep("executeTool", .)   ->  foo.go:681
  Read(foo.go, offset=660, limit=80)  ->  hashlines
  Edit(...)

### Definition of "large"
A file is considered large when its line count exceeds `largeFileThreshold` (500
lines) **and** no explicit `offset` or `limit` was provided. The threshold is a
package-level constant so it can be updated in one place.

### Summary format
```
@file rel/path#TAG
[large file: 1234 lines, 45.6 KB — first 10 and last 10 lines shown; use Grep to locate a symbol, then Read with offset+limit]
1#abc|<first line>
2#def|<second line>
...
10#xyz|<tenth line>
--- 1214 lines omitted ---
1225#abc|<second-to-last line>
...
1234#xyz|<last line>
```

- The `@file` header and hashline format are **unchanged** — Edit anchors on
  the shown lines remain fully valid.
- The omission banner `--- N lines omitted ---` is machine-readable.
- Head shows first 10 lines; tail shows last 10 lines (constants:
  `summaryHead = 10`, `summaryTail = 10`).
- The hint line explicitly names `Grep` and `Read(offset, limit)` as next steps.
  Do not use the word "structural overview".

### Schema changes
- Add `summary_only` boolean parameter (optional, default false). When true,
  forces summary mode even for small files. Useful for quick orientation.
- Update description to document the automatic summary behaviour and the
  recommended Grep then Read workflow.

### Expected outcomes
- Files with <= 500 lines: behaviour identical to today.
- Files with > 500 lines, no offset/limit: returns head/tail summary.
- Explicit `offset` or `limit` always bypasses summary mode.
- `summary_only=true` forces summary on any file size.
- Existing tests pass unchanged.
- New tests verify: summary triggered at threshold, format correct,
  disabled by offset/limit.

### Todo list
- [ ] Add `largeFileThreshold`, `summaryHead`, `summaryTail` constants to `read.go`
- [ ] Implement `buildSummary(allLines []string, relPath, fullContent string) string` in `read.go`
- [ ] Update `ReadTool.Execute` to call `buildSummary` when conditions are met
- [ ] Update `ReadTool.Schema` in `schema.go` to document summary behaviour,
  add `summary_only` parameter, reference Grep workflow
- [ ] Add tests in `tools_extra_test.go`:
  - summary triggered for large file (> threshold)
  - summary not triggered when offset given
  - summary not triggered when limit given
  - `summary_only=true` forces summary on small file
  - summary format: correct @file header, omission banner, correct line counts

### Relevant context
- `internal/tool/fs/read.go` — `ReadTool.Execute`, constants
- `internal/tool/fs/schema.go` — `ReadTool.Schema`
- `internal/tool/fs/hashline.go` — `hashlineReadOutput`, `FormatHashline`
- `internal/tool/fs/tools_extra_test.go` — existing read tests

---

## Sub-task 2 — Grep tool

**Status:** [x] done

### Intent
Provide a dedicated `Grep` tool that the model can call instead of running
`rg` through Bash. Prefer `rg` for speed and `.gitignore` respect; fall back to
a pure-Go walker when `rg` is absent.

### Parameters
| Name | Type | Default | Notes |
|---|---|---|---|
| `pattern` | string (required) | — | Regex or literal pattern |
| `path` | string | `.` | Directory or file to search |
| `include` | string | `""` | Glob filter for file names (e.g. `*.go`) |
| `exclude` | string | `""` | Glob filter to skip (e.g. `*_test.go`) |
| `case_insensitive` | bool | false | |
| `fixed_strings` | bool | false | Treat pattern as literal string |
| `max_results` | int | 50 | Hard cap; returns truncation notice if exceeded |

### rg path
Launch `rg` with:
```
rg --line-number --no-heading [--case-sensitive/-i] [-F] [--glob include]
   [--glob !exclude] -- <pattern> <path>
```
**Do not pass `--max-count`** — `rg --max-count N` limits per file, not globally,
so a large repo would still produce unbounded output. Instead, read `rg`'s stdout
line-by-line in a streaming loop and cancel the process (via context cancellation
on the `exec.Cmd`) as soon as the running match count reaches `max_results + 1`.
This gives a clean global cap regardless of file count.

Parse each output line (`file:lineNo:content`) into `ContentLine`.

### Go fallback path
- `filepath.WalkDir` with skip list: `.git`, `node_modules`, `vendor`, `dist`,
  `build`, `.cache`, `__pycache__`, `.next`, `target` (Rust/Java build dir).
- Apply `include` glob filter on `filepath.Base(path)`.
- Apply `exclude` glob filter on `filepath.Base(path)`.
- Compile pattern as `regexp.MustCompile` (or `regexp.Compile`, returning error
  on bad pattern).
- Read matching files line-by-line; collect up to `max_results + 1` matches.

### Output format
```
path/to/file.go:12:  func foo() {
path/to/other.go:45: import "fmt"
[results truncated at 50 matches; narrow the search with include/path]
```

The `ToolResult.Lines` field carries `ContentLine{File, LineNo, Text, Type=LineMatch}`
for the TUI renderer. `ToolResult.Output` has the same content as plain text for
the LLM.

### Registration
- `internal/tool/fs/grep.go` — implementation + `init()`
- `internal/tool/schema.go` — add `ToolGrep = "Grep"` constant; add to `builtinToolOrder`
- `internal/tool/perm/decision.go` — add `"Grep"` to `readOnlyTools`
- `internal/tool/schema_registry_test.go` — add `tool.ToolGrep` to
  `TestBuiltinToolsAllRegistered`

### Expected outcomes
- `rg` present: uses `rg`, honours `.gitignore`.
- `rg` absent: pure-Go walker produces identical result format.
- Max results enforced; truncation note in output.
- Invalid pattern returns error result (not panic).
- Registered and visible in model tool list.

### Todo list
- [ ] Add `ToolGrep = "Grep"` constant to `internal/tool/schema.go`; add to
  `builtinToolOrder`
- [ ] Add `"Grep"` to `readOnlyTools` map in `internal/tool/perm/decision.go`
- [ ] Create `internal/tool/fs/grep.go`:
  - `GrepTool` struct with `Name/Description/Icon/Schema/Execute`
  - `execRG(ctx, args) ([]ContentLine, bool, error)` — runs `rg`, parses output
  - `execGoGrep(ctx, pattern, path, include, exclude string, caseSensitive,
    fixedString bool, max int) ([]ContentLine, bool, error)` — Go fallback
  - `init()` registers `&GrepTool{}`
- [ ] Add `internal/tool/fs/grep_test.go`:
  - `TestGrep_BasicMatch` — finds lines matching pattern in a temp dir
  - `TestGrep_CaseInsensitive` — `-i` flag works
  - `TestGrep_IncludeFilter` — only searches matching file extensions
  - `TestGrep_MaxResults` — truncation at cap
  - `TestGrep_InvalidPattern` — graceful error
  - `TestGrep_EmptyDir` — no matches, no panic
- [ ] Add `tool.ToolGrep` to `TestBuiltinToolsAllRegistered` in
  `internal/tool/schema_registry_test.go`

### Relevant context
- `internal/tool/fs/read.go` — pattern for `Execute`, parameter extraction
- `internal/tool/toolresult/content.go` — `ContentLine`, `LineMatch`
- `internal/tool/perm/decision.go` — `readOnlyTools`
- `internal/tool/schema.go` — `builtinToolOrder`

---

## Sub-task 3 — Glob tool

**Status:** [x] done

### Intent
Provide a dedicated `Glob` tool for fast file discovery by pattern. Pure Go — no
external dependency. The model calls this instead of `find` or `ls` through Bash.

### Parameters
| Name | Type | Default | Notes |
|---|---|---|---|
| `pattern` | string (required) | — | Glob pattern, e.g. `**/*.go`, `src/*.ts` |
| `path` | string | `.` | Base directory to search within |
| `exclude` | string | `""` | Glob pattern for paths to skip |
| `max_results` | int | 200 | Hard cap |

### `**` expansion
Hand-rolled: split pattern on `/`; a `**` segment matches zero or more path
components; other segments use `filepath.Match`. Simple, no external dep.

### Default skip list
`.git`, `node_modules`, `vendor`, `dist`, `build`, `.cache`, `__pycache__`,
`.next`, `target`, `.bob`, `graphify-out`.

### Output format
```
src/foo.go
src/bar.go
internal/tool/fs/read.go
[results truncated at 200 matches]
```

Sorted lexicographically. Paths relative to the base `path` (or cwd if path
is `.`).

### Registration
- `internal/tool/fs/glob.go` — implementation + `init()`
- `internal/tool/schema.go` — add `ToolGlob = "Glob"` constant; add to `builtinToolOrder`
- `internal/tool/perm/decision.go` — add `"Glob"` to `readOnlyTools`
- `internal/tool/schema_registry_test.go` — add `tool.ToolGlob`

### Expected outcomes
- `**/*.go` matches all `.go` files recursively.
- `src/*.ts` matches only direct children of `src/`.
- Skip list prevents `.git`/`node_modules` from appearing.
- `exclude` pattern removes matching files.
- Max results enforced with truncation notice.
- Results are relative paths, sorted, one per line.

### Todo list
- [ ] Add `ToolGlob = "Glob"` constant to `internal/tool/schema.go`; add to
  `builtinToolOrder`
- [ ] Add `"Glob"` to `readOnlyTools` in `internal/tool/perm/decision.go`
- [ ] Create `internal/tool/fs/glob.go`:
  - `GlobTool` struct with `Name/Description/Icon/Schema/Execute`
  - `matchGlob(pattern, relPath string) bool` — `**`-aware matcher
  - `defaultSkipDirs` set
  - `init()` registers `&GlobTool{}`
- [ ] Add `internal/tool/fs/glob_test.go`:
  - `TestGlob_RecursivePattern` — `**/*.go` finds all Go files
  - `TestGlob_SingleLevelPattern` — `*.txt` matches only root-level files
  - `TestGlob_ExcludePattern` — exclude filter removes files
  - `TestGlob_SkipDirs` — `.git`/`node_modules` are never returned
  - `TestGlob_MaxResults` — truncation at cap
  - `TestGlob_NonexistentBase` — graceful error
- [ ] Add `tool.ToolGlob` to `TestBuiltinToolsAllRegistered`

### Relevant context
- `internal/tool/fs/grep.go` (just created) — same skip list, same output pattern
- `internal/tool/perm/decision.go` — `readOnlyTools`
- `internal/tool/schema.go` — `builtinToolOrder`

---

## Sub-task 4 — Validation

**Status:** [x] done

### Intent
Ensure the whole change compiles, all existing tests pass, and the new tests pass.
Catch any `builtinToolOrder`/registry drift.

### Todo list
- [ ] `go build ./...`
- [ ] `go test ./internal/tool/...`
- [ ] `go vet ./internal/tool/...`
- [ ] Fix any failures

### Relevant context
- `docs/operations/development.md` — `make test`, `make build`
