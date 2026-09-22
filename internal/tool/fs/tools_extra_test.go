package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRead_LineLimit_LargeFile verifies that Read respects the limit parameter
// and returns at most limit lines even when the file has many more.
func TestRead_LineLimit_LargeFile(t *testing.T) {
	// Create a temp file with 200 lines
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "large.txt")

	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		sb.WriteString(fmt.Sprintf("line %d\n", i))
	}
	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	rt := &ReadTool{}
	ctx := context.Background()

	t.Run("reads all lines by default (up to maxReadLines)", func(t *testing.T) {
		result := rt.Execute(ctx, map[string]any{
			"file_path": filePath,
		}, tmpDir)

		if !result.Success {
			t.Fatalf("Expected success, got error: %s", result.Output)
		}
		// 200 is below maxReadLines (2000), so all 200 lines should be returned
		if len(result.Lines) != 200 {
			t.Errorf("Expected 200 lines, got %d", len(result.Lines))
		}
		if result.Metadata.Truncated {
			t.Error("Expected Truncated=false for 200-line file with default limit")
		}
	})

	t.Run("limit parameter restricts number of lines returned", func(t *testing.T) {
		limit := 50
		result := rt.Execute(ctx, map[string]any{
			"file_path": filePath,
			"limit":     float64(limit), // JSON numbers come as float64
		}, tmpDir)

		if !result.Success {
			t.Fatalf("Expected success, got error: %s", result.Output)
		}
		if len(result.Lines) != limit {
			t.Errorf("Expected %d lines, got %d", limit, len(result.Lines))
		}
		if !result.Metadata.Truncated {
			t.Error("Expected Truncated=true when limit < total lines")
		}
	})

	t.Run("limit=1 returns exactly one line", func(t *testing.T) {
		result := rt.Execute(ctx, map[string]any{
			"file_path": filePath,
			"limit":     float64(1),
		}, tmpDir)

		if !result.Success {
			t.Fatalf("Expected success, got error: %s", result.Output)
		}
		if len(result.Lines) != 1 {
			t.Errorf("Expected 1 line, got %d", len(result.Lines))
		}
		if result.Lines[0].Text != "line 1" {
			t.Errorf("Expected first line text 'line 1', got %q", result.Lines[0].Text)
		}
	})

	t.Run("offset skips lines before reading", func(t *testing.T) {
		result := rt.Execute(ctx, map[string]any{
			"file_path": filePath,
			"offset":    float64(100),
			"limit":     float64(10),
		}, tmpDir)

		if !result.Success {
			t.Fatalf("Expected success, got error: %s", result.Output)
		}
		if len(result.Lines) != 10 {
			t.Errorf("Expected 10 lines, got %d", len(result.Lines))
		}
		// First returned line should be line 100 (offset is 1-based but lines before offset are skipped)
		if !strings.HasPrefix(result.Lines[0].Text, "line ") {
			t.Errorf("Unexpected first line text: %q", result.Lines[0].Text)
		}
	})
}

func TestReadCapsTotalOutput(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "huge.txt")
	// 400 lines x ~1KB each = ~400KB, above the 256KB output cap but far
	// below the 2000-line cap.
	line := strings.Repeat("x", 1024)
	var sb strings.Builder
	for range 400 {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, tmpDir)
	if !result.Success {
		t.Fatalf("read failed: %s", result.Error)
	}
	if !result.Metadata.Truncated || len(result.Lines) >= 400 {
		t.Fatalf("read should stop at the byte cap, got %d lines, truncated=%v", len(result.Lines), result.Metadata.Truncated)
	}
	out := result.FormatForLLM()
	if !strings.Contains(out, "output truncated at line") || !strings.Contains(out, "continue with offset=") {
		t.Fatalf("truncated read must tell the model where to continue, got tail %q", out[len(out)-120:])
	}
}

func TestReadImageFileSaysSo(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "shot.png")
	// A null byte marks it binary; the extension marks it as an image.
	if err := os.WriteFile(filePath, []byte{0x89, 'P', 'N', 'G', 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, tmpDir)
	if !result.Success {
		t.Fatalf("read failed: %s", result.Error)
	}
	out := result.FormatForLLM()
	if !strings.Contains(out, "image file") || !strings.Contains(out, "attach the image") {
		t.Fatalf("image read should explain the gap and the alternative, got %q", out)
	}
}

func TestReadEmptyFileSaysSo(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(filePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, tmpDir)
	if !result.Success {
		t.Fatalf("reading an empty file should succeed, got: %s", result.Error)
	}
	if !strings.Contains(result.FormatForLLM(), "file exists but is empty") {
		t.Fatalf("empty read should say so instead of returning nothing, got %q", result.FormatForLLM())
	}
}

// readForEdit satisfies Edit/Write's read-before-modify gate in tests.
// Returns the file TAG extracted from the hashline Read output.
func readForEdit(t *testing.T, filePath, cwd string) string {
	t.Helper()
	result := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, cwd)
	if !result.Success {
		t.Fatalf("Read before edit failed: %s", result.FormatForLLM())
	}
	// Extract TAG from first line "@file path#TAG".
	out := result.FormatForLLM()
	firstLine := strings.SplitN(out, "\n", 2)[0]
	idx := strings.LastIndexByte(firstLine, '#')
	if idx < 0 {
		t.Fatalf("Read output missing @file header: %q", firstLine)
	}
	return firstLine[idx+1:]
}

// editFile is a test helper for a single-edit hashline Edit call.
func editFile(t *testing.T, filePath, cwd, tag string, edits []any) bool {
	t.Helper()
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits":     edits,
	}, cwd)
	return result.Success
}

// TestEditPreservesBOM verifies that BOM is stripped for hashing and that
// the edit result does not corrupt a BOM-prefixed file.
func TestEditPreservesBOM(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "bom.txt")
	original := "\ufefftitle: old\nfirst: keep\n"
	if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := readForEdit(t, filePath, tmpDir)

	titleRef := buildRef(1, "title: old")
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": titleRef, "to": titleRef, "content": "title: new"},
		},
	}, tmpDir)
	if !result.Success {
		t.Fatalf("edit failed: %s", result.Error)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	want := "\ufefftitle: new\nfirst: keep\n"
	if string(got) != want {
		t.Errorf("file content = %q, want %q", string(got), want)
	}
}

// TestEditPreservesCRLF verifies that CRLF line endings survive a hashline edit.
func TestEditPreservesCRLF(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "crlf.txt")
	original := "first: old\r\nsecond: keep\r\n"
	if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := readForEdit(t, filePath, tmpDir)

	firstRef := buildRef(1, "first: old")
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": firstRef, "to": firstRef, "content": "first: new"},
		},
	}, tmpDir)
	if !result.Success {
		t.Fatalf("CRLF edit failed: %s", result.Error)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	want := "first: new\r\nsecond: keep\r\n"
	if string(got) != want {
		t.Errorf("CRLF file = %q, want %q", string(got), want)
	}
}

// TestWriteOverwriteRequiresCurrentView verifies Write still enforces read-before-overwrite.
func TestWriteOverwriteRequiresCurrentView(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "target.txt")
	if err := os.WriteFile(filePath, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	write := func() bool {
		res := (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
			"file_path": filePath,
			"content":   "replaced\n",
		}, tmpDir)
		return res.Success
	}

	if write() {
		t.Fatal("overwrite without read must be rejected")
	}

	readForEdit(t, filePath, tmpDir)
	if !write() {
		t.Fatal("overwrite after read should succeed")
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "replaced\n" {
		t.Fatalf("file content = %q", got)
	}
}

// TestWriteNewFileNeedsNoRead verifies Write can create a new file without a prior Read.
func TestWriteNewFileNeedsNoRead(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "fresh.txt")
	res := (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "hello\n",
	}, tmpDir)
	if !res.Success {
		t.Fatalf("creating a new file must not require a read, got: %s", res.Error)
	}
}

// --- Hashline Read output format tests ---

// readFileTag reads a file and returns the file TAG from the @file header.
func readFileTag(t *testing.T, filePath, cwd string) string {
	t.Helper()
	return readForEdit(t, filePath, cwd)
}

// TestHashlineRead_OutputFormat verifies the @file header and LINE#hash|content format.
func TestHashlineRead_OutputFormat(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "sample.txt")
	content := "first line\nsecond line\nthird line\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{"file_path": filePath}, tmpDir)
	if !result.Success {
		t.Fatal(result.Error)
	}
	output := result.FormatForLLM()
	tag := readFileTag(t, filePath, tmpDir)

	// File tag must be 4 hex chars.
	if len(tag) != 4 {
		t.Errorf("file tag %q has length %d, want 4", tag, len(tag))
	}
	for _, c := range tag {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			t.Errorf("file tag %q has non-hex char %q", tag, c)
		}
	}

	// Lines must be LINE#hash|content format.
	lines := strings.Split(strings.TrimSpace(output), "\n")
	// lines[0] is the @file header; rest are LINE#hash|content
	for i, l := range lines[1:] {
		lineNo := i + 1
		prefix := fmt.Sprintf("%d#", lineNo)
		if !strings.HasPrefix(l, prefix) {
			t.Errorf("line %d: missing prefix %q in %q", lineNo, prefix, l)
			continue
		}
		rest := l[len(prefix):]
		pipeIdx := strings.IndexByte(rest, '|')
		if pipeIdx != 3 {
			t.Errorf("line %d: hash should be 3 chars before |, got pos %d in %q", lineNo, pipeIdx, rest)
		}
	}
}

// TestHashlineRead_FileTagCoversFullFile verifies that a windowed read still
// returns the whole-file TAG, not a partial one.
func TestHashlineRead_FileTagCoversFullFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "big.txt")
	var sb strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	content := sb.String()
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Full read tag.
	fullTag := readForEdit(t, filePath, tmpDir)

	// Windowed read (lines 5–10) should carry the same full-file tag.
	result := (&ReadTool{}).Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"offset":    float64(5),
		"limit":     float64(6),
	}, tmpDir)
	if !result.Success {
		t.Fatalf("windowed Read failed: %s", result.Error)
	}
	firstLine := strings.SplitN(result.FormatForLLM(), "\n", 2)[0]
	idx := strings.LastIndexByte(firstLine, '#')
	windowTag := firstLine[idx+1:]

	if fullTag != windowTag {
		t.Errorf("window tag %q != full tag %q", windowTag, fullTag)
	}
}

// --- Edit integration tests ---

// TestEdit_FullFlow exercises the Read → Edit → verify cycle.
func TestEdit_FullFlow(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "edit_me.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	tag := readForEdit(t, filePath, tmpDir)

	betaRef := buildRef(2, "beta")
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": betaRef, "to": betaRef, "content": "BETA"},
		},
	}, tmpDir)
	if !result.Success {
		t.Fatalf("Edit failed: %s / %s", result.Output, result.Error)
	}
	if !strings.Contains(result.Output, "snapshot invalidated") {
		t.Errorf("success result should mention snapshot invalidation, got: %q", result.Output)
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\nBETA\ngamma\n" {
		t.Errorf("file content = %q want %q", string(got), "alpha\nBETA\ngamma\n")
	}
}

// TestEdit_StaleFileTag verifies that an Edit with stale TAG is rejected.
func TestEdit_StaleFileTag(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "stale.txt")
	original := "alpha\nbeta\n"
	if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	readForEdit(t, filePath, tmpDir)

	betaRef := buildRef(2, "beta")
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  "DEAD",
		"edits": []any{
			map[string]any{"from": betaRef, "to": betaRef, "content": "BETA"},
		},
	}, tmpDir)
	if result.Success {
		t.Fatal("expected Edit to fail with stale file tag")
	}
	if !strings.Contains(result.Error, "stale") && !strings.Contains(result.Error, "tag") {
		t.Errorf("error should mention stale/tag, got: %q", result.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != original {
		t.Errorf("stale-tag edit changed file to %q", string(got))
	}
}

// TestEdit_StaleLineHash verifies that an Edit with stale line hash is rejected.
func TestEdit_StaleLineHash(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "linemismatch.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	tag := readForEdit(t, filePath, tmpDir)

	wrongRef := fmt.Sprintf("2#%s", ComputeLineHash("WRONG_CONTENT"))
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": wrongRef, "to": wrongRef, "content": "X"},
		},
	}, tmpDir)
	if result.Success {
		t.Fatal("expected Edit to fail with stale line hash")
	}
	if !strings.Contains(result.Error, "stale") {
		t.Errorf("error should mention stale, got: %q", result.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != original {
		t.Errorf("stale-line-hash edit changed file to %q", string(got))
	}
}

// TestEdit_ConcurrentModification verifies that if the file changes on disk
// after read but before edit, the TAG mismatch catches it.
func TestEdit_ConcurrentModification(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "concurrent.txt")
	original := "line1\nline2\n"
	if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	tag := readForEdit(t, filePath, tmpDir)

	// Simulate concurrent modification.
	modified := "line1\nMODIFIED\n"
	if err := os.WriteFile(filePath, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}
	// Refresh the mtime stamp so the mtime check doesn't fire first.
	recordFileWritten(filePath)

	line1Ref := buildRef(1, "line1")
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": line1Ref, "to": line1Ref, "content": "NEW_LINE1"},
		},
	}, tmpDir)
	if result.Success {
		t.Fatal("expected Edit to fail when file was concurrently modified")
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != modified {
		t.Errorf("concurrent edit changed file to %q (should stay as %q)", string(got), modified)
	}
}

// TestEdit_RequiresObservedView verifies that Edit fails if the file
// has never been Read this session.
func TestEdit_RequiresObservedView(t *testing.T) {
	ResetFileViews()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "unread.txt")
	content := "a\nb\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := ComputeFileHash(content)

	aRef := buildRef(1, "a")
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": aRef, "to": aRef, "content": "A"},
		},
	}, tmpDir)
	if result.Success {
		t.Fatal("expected Edit to fail when file was never read this session")
	}
	if !strings.Contains(result.Error, "not been read") && !strings.Contains(result.Error, "Read it") {
		t.Errorf("error should instruct to read first, got: %q", result.Error)
	}
}

// --- Smart Read (large-file head/tail summary) tests ---

// TestSmartRead_SummaryTriggeredForLargeFile verifies that a file above
// largeFileThreshold lines returns a summary when no offset/limit is given.
func TestSmartRead_SummaryTriggeredForLargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "big.txt")

	var sb strings.Builder
	total := largeFileThreshold + 50 // clearly over threshold
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{
		"file_path": filePath,
	}, tmpDir)
	if !result.Success {
		t.Fatalf("read failed: %s", result.Error)
	}

	out := result.FormatForLLM()

	// Must contain the @file header.
	if !strings.HasPrefix(out, "@file ") {
		t.Errorf("summary missing @file header: %q", out[:min(60, len(out))])
	}
	// Must contain the large-file annotation.
	if !strings.Contains(out, "large file:") {
		t.Errorf("summary missing 'large file:' annotation, got:\n%s", out[:min(200, len(out))])
	}
	// Must contain the omission banner.
	if !strings.Contains(out, "lines omitted") {
		t.Errorf("summary missing 'lines omitted' banner, got:\n%s", out[:min(200, len(out))])
	}
	// Metadata must be marked truncated.
	if !result.Metadata.Truncated {
		t.Error("summary result should set Metadata.Truncated=true")
	}
	// LineCount should reflect total file lines, not just the window.
	if result.Metadata.LineCount != total {
		t.Errorf("LineCount = %d, want %d", result.Metadata.LineCount, total)
	}
}

// TestSmartRead_SummaryNotTriggeredWhenOffsetGiven verifies that an explicit
// offset bypasses summary mode and returns the normal windowed output.
func TestSmartRead_SummaryNotTriggeredWhenOffsetGiven(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "big.txt")

	var sb strings.Builder
	for i := 1; i <= largeFileThreshold+100; i++ {
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"offset":    float64(10),
		"limit":     float64(5),
	}, tmpDir)
	if !result.Success {
		t.Fatalf("read failed: %s", result.Error)
	}
	out := result.FormatForLLM()
	if strings.Contains(out, "large file:") {
		t.Error("summary mode must not trigger when offset is given")
	}
	if len(result.Lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(result.Lines))
	}
}

// TestSmartRead_SummaryNotTriggeredWhenLimitGiven verifies that an explicit
// limit bypasses summary mode.
func TestSmartRead_SummaryNotTriggeredWhenLimitGiven(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "big.txt")

	var sb strings.Builder
	for i := 1; i <= largeFileThreshold+100; i++ {
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"limit":     float64(20),
	}, tmpDir)
	if !result.Success {
		t.Fatalf("read failed: %s", result.Error)
	}
	out := result.FormatForLLM()
	if strings.Contains(out, "large file:") {
		t.Error("summary mode must not trigger when limit is given")
	}
	if len(result.Lines) != 20 {
		t.Errorf("expected 20 lines, got %d", len(result.Lines))
	}
}

// TestSmartRead_SummaryOnlyForcesSmallFile verifies that summary_only=true
// forces the head/tail format even for a small file (below threshold).
func TestSmartRead_SummaryOnlyForcesSmallFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "small.txt")

	var sb strings.Builder
	for i := 1; i <= 30; i++ { // well below largeFileThreshold
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{
		"file_path":    filePath,
		"summary_only": true,
	}, tmpDir)
	if !result.Success {
		t.Fatalf("read failed: %s", result.Error)
	}
	out := result.FormatForLLM()
	if !strings.Contains(out, "large file:") {
		t.Errorf("summary_only=true must produce summary even for small file; got:\n%s", out[:min(300, len(out))])
	}
}

// TestSmartRead_SummaryFormat checks the structural correctness of the summary
// output: @file header with TAG, annotation line, head hashlines, omission
// banner, tail hashlines.
func TestSmartRead_SummaryFormat(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "fmt.txt")

	total := largeFileThreshold + 200
	var sb strings.Builder
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&sb, "line%d content\n", i)
	}
	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&ReadTool{}).Execute(context.Background(), map[string]any{
		"file_path": filePath,
	}, tmpDir)
	if !result.Success {
		t.Fatalf("read failed: %s", result.Error)
	}
	out := result.FormatForLLM()
	outLines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	// Line 0: @file header with 4-hex TAG.
	if !strings.HasPrefix(outLines[0], "@file ") {
		t.Fatalf("line 0 should be @file header, got %q", outLines[0])
	}
	tagIdx := strings.LastIndexByte(outLines[0], '#')
	if tagIdx < 0 {
		t.Fatalf("@file header missing '#TAG': %q", outLines[0])
	}
	tag := outLines[0][tagIdx+1:]
	if len(tag) != 4 {
		t.Errorf("TAG should be 4 chars, got %q", tag)
	}

	// Line 1: annotation.
	if !strings.Contains(outLines[1], "large file:") {
		t.Errorf("line 1 should be the annotation, got %q", outLines[1])
	}

	// Lines 2..11: head hashlines (1#hash|content through 10#hash|content).
	for i := 0; i < summaryHead; i++ {
		lineIdx := 2 + i
		expected := fmt.Sprintf("%d#", i+1)
		if !strings.HasPrefix(outLines[lineIdx], expected) {
			t.Errorf("head line %d: want prefix %q, got %q", lineIdx, expected, outLines[lineIdx])
		}
	}

	// After the head there must be exactly one "--- N lines omitted ---" banner.
	bannerIdx := -1
	for i, l := range outLines {
		if strings.Contains(l, "lines omitted") {
			bannerIdx = i
			break
		}
	}
	if bannerIdx < 0 {
		t.Fatal("omission banner not found in summary output")
	}

	// After the banner there must be exactly summaryTail hashlines.
	tail := outLines[bannerIdx+1:]
	if len(tail) != summaryTail {
		t.Errorf("tail should have %d lines, got %d", summaryTail, len(tail))
	}
	// Last tail line should be the last line of the file.
	lastExpected := fmt.Sprintf("%d#", total)
	if !strings.HasPrefix(tail[len(tail)-1], lastExpected) {
		t.Errorf("last tail line should start with %q, got %q", lastExpected, tail[len(tail)-1])
	}
}

