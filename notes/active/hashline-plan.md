# Hashline Editing Plan

## Overview

Add Hashline editing to San: a token-efficient, anchor-based file editing mechanism
where `read` annotates every line with `LINE#HASH|content` and prefixes the file with
`@file path#TAG`, and `edit` uses those anchors to verify staleness before applying
changes. Implementation lives entirely in `internal/tool/fs/` — no agent-loop changes,
no new dependencies, no new packages.

**Design goals:**
- Whole-file `@file path#TAG` fingerprint (4 uppercase hex chars, FNV-64 low-16).
- Per-line `LINE#HASH` anchors (3 lowercase letters, FNV-64 mod 26³, ignoring whitespace).
- Hash stale → reject edit immediately, never silent-apply. Short error guiding model to re-read.
- After any successful edit the old snapshot is invalid; model must re-read before another edit on same file.
- Support: replace single line, replace range, insert (empty content range), delete (null content).
- Path/workspace validation identical to current Read/Edit.
- All logic in `internal/tool/fs/` layer.  No new `internal/` packages.
- Zero new runtime deps; only `hash/fnv` from stdlib.

**Non-goals:**
- Not replacing the existing `Edit` tool (keep backward compat).
- Not modifying agent loop, provider, or session layers.
- Not adding a new `Read`-variant tool with a separate name — extend the existing `Read`
  tool output format (add a `hashline` parameter) OR introduce a new `HashRead` tool.
  Decision: **Add a new `HashRead` tool** named `"Read"` → No, to avoid schema conflict.
  **Final decision: Add `hashline` boolean param to the existing `Read` tool, plus a new
  `HashEdit` tool named `"HashEdit"`. This preserves backward compat while adding Hashline.**

---

## Sub-Task 1: Hash Utilities

**Intent:** Create `internal/tool/fs/hashline.go` with pure hash functions, format helpers,
and anchor parsing. No I/O. Reusable by both Read and HashEdit.

**Expected Outcomes:**
- `ComputeLineHash(line string) string` — 3-letter hash (a-z), strips whitespace before hashing.
- `ComputeFileHash(text string) string` — 4 uppercase hex chars, FNV-64 low-16 of normalized text.
- `NormalizeFileHashText(text string) string` — strips trailing spaces/tabs/CR per line, normalizes CRLF→LF.
- `FormatFileHeader(relPath, tag string) string` — produces `@file rel/path#TAG`.
- `FormatHashline(lineNo int, text string) string` — produces `LINE#hash|text`.
- `ParseHashRef(ref string) (lineNo int, hash string, err error)` — parses `"10#abc"`.
- `NormalizeFileTag(hash string) string` — strips `@file path#` prefix, uppercases, returns empty if invalid.
- All functions are pure and deterministic.

**Todo List:**
1. Create `internal/tool/fs/hashline.go`.
2. Implement `ComputeLineHash`: FNV-64a on whitespace-stripped line, map to 3 letters `a-z` via `h % 17576` → base-26.
3. Implement `NormalizeFileHashText`: LF-normalize + strip trailing horizontal whitespace per line.
4. Implement `ComputeFileHash`: FNV-64a on normalized text, `uint16(h.Sum64() & 0xffff)`, `%04X`.
5. Implement `FormatFileHeader`: `@file path#TAG` (slash-normalize path).
6. Implement `FormatHashline`: `%d#%s|%s`.
7. Implement `ParseHashRef`: parse `"10#abc"` → lineNo=10, hash="abc"; error if malformed.
8. Implement `NormalizeFileTag`: strip `@file <path>#`, uppercase, return "" if len ≠ 4 hex chars.

**Relevant Context:**
- Phi's `internal/util/hash.go` for algorithm reference.
- `hash/fnv` is already in Go stdlib — no new imports needed in `go.mod`.
- Line hash: strip ALL whitespace (not just trim) so `a b` == `ab`. Phi strips via `removeWhitespace`.
- File hash normalizes trailing whitespace per line — so CRLF and LF produce the same hash.

**Status:** `[ ] pending`

---

## Sub-Task 2: HashEdit Apply Engine

**Intent:** Create `internal/tool/fs/hashline_apply.go` with the pure apply logic
(no file I/O, no permissions) that takes `fileContent string` + a list of flat edits +
file TAG, verifies all anchors, and returns new content.

**Expected Outcomes:**
- `type HashlineEdit struct { From, To string; Content *string }` — wire shape (JSON-compatible).
- `type HashlineMismatch struct { Line int; Expected, Actual string }`.
- `ApplyHashlineEdits(fileContent string, fileTag string, edits []HashlineEdit) (newContent string, err error)`
  - Verifies `fileTag` matches `ComputeFileHash(fileContent)` → `ErrFileTagMismatch`.
  - Parses each `From`/`To` anchor via `ParseHashRef`.
  - Validates `from.line ≤ to.line` (for ranges).
  - Validates line hash at `from` and `to` lines.
  - Sorts edits by start line ascending.
  - Checks for overlapping edit ranges → error.
  - Applies edits bottom-up (or top-down after sorting) to avoid line-index drift.
  - `Content == nil` → delete range. `Content == ""` → delete. Non-nil → replace.
  - `From == To` with content → replace single line. Insert-before: `To < From` makes no sense; use
    `From = TO` but `from` line is one past insertion point (like line N+0.5 = before line N: use `from` = target line number with same hash, content is new lines prepended).
    **Cleaner design:** `from` == target line, `to` == target line, `content` replaces that range.
    For insert-before-line-N: set `from`=N#hash, `to`=N#hash but content = "new\nexisting".
    Actually follow Phi exactly: range `[from..to]` (inclusive) is replaced by `content` lines.
    Empty content → delete. Same line for from and to → single line replace.
  - Returns `HashlineMismatch` errors structured so the error message is compact and actionable.

**Todo List:**
1. Create `internal/tool/fs/hashline_apply.go`.
2. Define `HashlineEdit` struct with `From`, `To` string fields and `Content *string`.
3. Define `HashlineMismatch` and `HashlineMismatchError` types.
4. Implement `ApplyHashlineEdits`: validate file tag, parse anchors, verify line hashes,
   sort edits, check overlap, apply bottom-up, return new content string.
5. Edge cases: single-line file, empty file, delete-all, insert at start/end.
6. CRLF files: normalize on read, restore CRLF if original had it (like existing `applyEdit`).

**Relevant Context:**
- Phi's `ApplyHashlineEdit` in `internal/tools/writetool/hashline.go` for reference.
- San's `applyEdit` in `internal/tool/fs/edit.go` for CRLF/BOM handling pattern.
- Phi sorts edits and applies bottom-up to avoid index drift.

**Status:** `[ ] pending`

---

## Sub-Task 3: Extend Read Tool — Hashline Output Format

**Intent:** Add a `hashline` boolean parameter to the existing `Read` tool. When `hashline=true`,
the output format changes: first line is `@file rel/path#TAG`, subsequent lines are `LINE#hash|content`.
Existing `FormatForLLM` must handle this new format.

**Expected Outcomes:**
- `Read` with `hashline=false` (or absent) → existing `%6d\t%s\n` format, unchanged behavior.
- `Read` with `hashline=true` → output starts with `@file rel/path#TAG\n`, then `LINE#hash|content\n`.
- The file TAG is computed on the full normalized file content regardless of offset/limit
  (partial reads still carry the whole-file TAG so the model has the correct anchor for edit).
- Lines carry per-line hash based on their actual content.
- `FormatForLLM` for `Title == "Read"` checks a new flag in metadata or uses the `Lines` field
  format (need to decide whether to use a new `Lines` variant or keep the same and just change Output).
  **Decision:** Store hashline output in `Output` field directly (as a pre-formatted string) when
  `hashline=true`, and set `Lines = nil`. This avoids touching `FormatForLLM` logic. The `Output`
  field is already forwarded when `Lines` is empty and the Title is "Read". Wait — re-check: in
  `FormatForLLM`, `Lines` non-empty always wins. So store in Lines as-is OR use a different path.
  **Final decision:** When `hashline=true`, build the full hashline text in `Output` and return
  `Lines = nil`. `FormatForLLM` already outputs `Output` when `Lines` is empty. This is the minimal
  change.
- `recordFileRead` still called exactly once (after full content read), same as today.
- The file hash covers the complete file content, not just the windowed read.

**Todo List:**
1. Add `hashline` boolean param to `Read` schema in `internal/tool/fs/schema.go`.
2. In `internal/tool/fs/read.go`, after reading lines, check `tool.GetBool(params, "hashline")`.
3. If `hashline=true`:
   a. Re-read the full file content (already have the file open / or use `os.ReadFile`).
   b. Normalize LF.
   c. Compute file TAG with `ComputeFileHash`.
   d. Build output string: first line `FormatFileHeader(relPath, tag)`, then per-line `FormatHashline`.
   e. Set `result.Output = hashlineOutput`, leave `result.Lines = nil`.
4. Update `Read` schema description to document the hashline output format and its purpose.
5. Preserve all existing limits (offset/limit still apply to lines emitted, but TAG covers whole file).

**Relevant Context:**
- `internal/tool/fs/read.go` line 41-208.
- `internal/tool/fs/schema.go` line 12-42.
- `internal/tool/toolresult/render.go` lines 55-89 — `FormatForLLM` already outputs `Output` when `Lines == nil`.
- Phi's `internal/tools/readtool/read.go` for format reference.

**Status:** `[ ] pending`

---

## Sub-Task 4: HashEdit Tool

**Intent:** Create `internal/tool/fs/hashedit.go` implementing the new `HashEdit` tool.
Accepts `file_path`, `file_tag` (string), and `edits` (array of objects). Verifies anchors,
applies changes, rejects stale, updates file view state.

**Expected Outcomes:**
- New tool `HashEdit` registered in the tool registry.
- Schema: `file_path` (string), `file_tag` (string), `edits` (array of edit objects each with `from`, `to`, `content`).
- Execution flow:
  1. Resolve and validate `file_path`.
  2. Check `requireObservedView` (model must have seen the file — prevents blind edits).
  3. Read full file content.
  4. Call `ApplyHashlineEdits(content, file_tag, edits)`.
  5. If file TAG mismatch → reject with short "stale" error + hint to re-read.
  6. If line hash mismatch → reject with compact mismatch report + hint to re-read.
  7. If success → write new content to file, call `recordFileWritten`.
  8. Return success result with diff summary.
- `HashEdit` implements `PermissionAwareTool` (same as `Edit`) — requires user approval.
- After success: explicitly invalidate file view with a custom "post-hashline" stamp so that
  any subsequent `HashEdit` call requires a fresh `Read hashline=true` first.
  Implementation: call `recordFileWritten` (which updates the stamp) then additionally store
  the new file TAG in a separate `fileHashTags` map (similar to `fileViews`). On a new `HashEdit`,
  check that the `file_tag` param matches the stored tag from the last read — if it doesn't, the
  model is using an old snapshot. **Simpler approach (follow Phi):** Just verify file TAG against
  on-disk content at execute time. The mtime-based `fileViews` check plus the content-hash TAG
  check together enforce freshness. After edit, the on-disk content changes → next `HashEdit`
  with old TAG will fail the content-hash check. This is sufficient. No extra map needed.

**Todo List:**
1. Create `internal/tool/fs/hashedit.go`.
2. Define `HashEdit` struct and register in `init()`.
3. Implement `Schema()` with tool description explaining hashline workflow.
4. Implement `RequiresPermission() bool` → `true`.
5. Implement `PreparePermission`: resolve path, read file, call `ApplyHashlineEdits` (dry-run), generate diff.
6. Implement `ExecuteApproved`: resolve path, check `requireObservedView`, read file, apply, write, record.
7. Handle CRLF/BOM same as existing `applyEdit`.
8. On stale TAG error: `"file tag stale — re-read with hashline:true before editing"`.
9. On line hash mismatch: `"line N hash mismatch (want abc got def) — re-read file"`.
10. On success result: include diff summary + `"snapshot invalidated; re-read before next HashEdit"`.

**Relevant Context:**
- `internal/tool/fs/edit.go` for `PermissionAwareTool` pattern.
- `internal/tool/fs/file_view.go` for view freshness check functions.
- `internal/tool/perm/diff.go` for `GenerateDiff` usage.
- Phi's error messages for reference (compact, actionable).

**Status:** `[ ] pending`

---

## Sub-Task 5: Registration

**Intent:** Register `HashEdit` in San's tool registry so it appears in the model's tool catalog.

**Expected Outcomes:**
- `HashEdit` tool appears in `GetToolSchemasWith()` output.
- `HashEdit` is registered at init time just like all other fs tools.
- No changes to `internal/tool/register/all.go` needed (it blank-imports `fs` which already
  triggers the `init()` in `hashedit.go`).
- Confirm the tool ordering in `internal/tool/schema.go` — decide whether `HashEdit` appears
  near `Edit` in `builtinToolOrder` or at the end.

**Todo List:**
1. Verify `internal/tool/register/all.go` already imports `fs` (it does — no change needed).
2. Add `"HashEdit"` to `builtinToolOrder` in `internal/tool/schema.go` after `"Edit"`.
3. Confirm `internal/tool/fs/hashedit.go`'s `init()` registers `&HashEditTool{}`.

**Relevant Context:**
- `internal/tool/schema.go` lines 61-140 for `builtinToolOrder`.
- `internal/tool/register/all.go`.

**Status:** `[ ] pending`

---

## Sub-Task 6: Unit Tests

**Intent:** Cover all correctness invariants with table-driven Go tests in `internal/tool/fs/`.

**Expected Outcomes:**
- `TestComputeLineHash_*`: deterministic, 3 letters a-z, whitespace-insensitive, CRLF handled.
- `TestComputeFileHash_*`: deterministic, 4 hex chars A-F/0-9, CRLF/trailing-whitespace normalized.
- `TestFormatFileHeader`: output format correctness.
- `TestParseHashRef`: valid cases, error cases.
- `TestNormalizeFileTag`: various input forms → canonical 4-hex or "".
- `TestApplyHashlineEdits_*`:
  - Replace single line.
  - Replace range of lines.
  - Insert before line (same from/to, content includes original + new).
  - Delete single line (content = empty string or nil).
  - Delete range.
  - Stale file TAG → rejected.
  - Stale line hash → rejected with mismatch report.
  - Overlapping edits → rejected.
  - CRLF input → CRLF output.
  - LF input → LF output.
  - Empty file.
  - Unicode content in lines.
  - Multiple edits applied correctly bottom-up.
- `TestHashReadFormat`: `Read` with `hashline=true` returns correct `@file` header + `LINE#hash|content` lines.
- Integration test: `Read hashline=true` → capture TAG and line hashes → `HashEdit` succeeds → file changed.
- Integration test: `HashEdit` with stale file TAG → rejected.
- Integration test: `HashEdit` with stale line hash → rejected.
- Integration test: concurrent modification (change file after read, before edit) → rejected.

**Todo List:**
1. Create `internal/tool/fs/hashline_test.go` for hash utility tests.
2. Create `internal/tool/fs/hashline_apply_test.go` for apply engine tests.
3. Add hashline-specific cases to `internal/tool/fs/tools_extra_test.go` for integration flow.
4. Run `go test ./internal/tool/fs/...` and fix any issues.

**Relevant Context:**
- Phi's `internal/tools/writetool/hashline_test.go` for test case inspiration.
- Phi's `internal/util/hash_test.go`.
- San's existing `internal/tool/fs/edit_test.go` for test file structure patterns.

**Status:** `[ ] pending`

---

## Sub-Task 7: Build Validation

**Intent:** Ensure the whole project builds cleanly and all existing tests pass.

**Expected Outcomes:**
- `go build ./...` succeeds.
- `go test ./...` passes (existing tests unbroken, new tests green).
- `go vet ./...` clean.

**Todo List:**
1. Run `go build ./...`.
2. Run `go test ./internal/tool/fs/...`.
3. Run `go test ./...` (full suite).
4. Fix any compilation errors or test failures.

**Status:** `[ ] pending`

---

## Data Flow Diagram

```
Model calls Read(file_path, hashline=true)
  └─> read.go reads file
  └─> ComputeFileHash(content) → TAG
  └─> per-line ComputeLineHash → hash
  └─> returns "@file path#TAG\n10#abc|func foo() {\n..."
  └─> recordFileRead(filePath, info)

Model calls HashEdit(file_path, file_tag="TAG", edits=[{from:"10#abc", to:"12#ghi", content:"..."}])
  └─> requireObservedView(filePath)  [must have been Read this session]
  └─> os.ReadFile(filePath)
  └─> ApplyHashlineEdits(content, file_tag, edits)
      └─> ComputeFileHash(content) == file_tag?  → reject if not
      └─> ParseHashRef(from), ParseHashRef(to)
      └─> ComputeLineHash(lines[from-1]) == from.hash?  → reject if not
      └─> ComputeLineHash(lines[to-1]) == to.hash?  → reject if not
      └─> sort edits, check overlaps
      └─> apply bottom-up → newContent
  └─> os.WriteFile(filePath, newContent)
  └─> recordFileWritten(filePath)
  └─> returns success + diff
```

---

## Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| New tool vs extend Edit | New `HashEdit` tool | No schema collision, easy model training |
| Read format change | `hashline` bool param | Backward compatible; default off |
| Hash algorithm | FNV-64a (stdlib) | No deps, deterministic, fast |
| Line hash length | 3 letters (a-z) | Same as Phi; short, unambiguous vs line numbers |
| File TAG length | 4 hex chars (0-9A-F) | Same as Phi; 65536 space, sufficient fingerprint |
| Tag stored where | Computed at read time, verified at edit time from on-disk content | No extra state map needed |
| Stale detection | Content hash (TAG) + per-line hash | TAG catches file-level changes; line hash catches partial changes |
| CRLF handling | Normalize to LF for hashing; restore CRLF on write if original had it | Consistent with existing `applyEdit` pattern |
| PermissionAwareTool | Yes (same as Edit) | HashEdit is destructive; San's gate must apply |
| Tool package location | `internal/tool/fs/` | Stays in fs layer, no new package, consistent with existing tools |
