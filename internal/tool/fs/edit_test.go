package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// editOnce is a convenience wrapper for a single-anchor hashline Edit in tests.
// It reads the file first (to satisfy the view gate) and performs one replace.
func editOnce(t *testing.T, filePath, cwd, lineContent, newContent string) bool {
	t.Helper()
	tag := readForEdit(t, filePath, cwd)
	ref := buildRef(lineIndex(t, filePath, lineContent), lineContent)
	result := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": ref, "to": ref, "content": newContent},
		},
	}, cwd)
	return result.Success
}

// lineIndex returns the 1-based line number of the first line in filePath
// whose content equals lineContent (after LF normalization).
func lineIndex(t *testing.T, filePath, lineContent string) int {
	t.Helper()
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("lineIndex: %v", err)
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for i, line := range strings.Split(content, "\n") {
		if line == lineContent {
			return i + 1
		}
	}
	t.Fatalf("lineIndex: %q not found in %s", lineContent, filePath)
	return 0
}

func TestEditRequiresReadFirst(t *testing.T) {
	ResetFileViews()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "unread.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := ComputeFileHash("hello\n")
	helloRef := buildRef(1, "hello")
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": helloRef, "to": helloRef, "content": "goodbye"},
		},
	}, tmpDir)
	if res.Success || !strings.Contains(res.Error, "has not been read in this session") {
		t.Fatalf("edit without read must be rejected, got: %+v", res)
	}
}

func TestEditStaleFileTagRejected(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "stale.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	betaRef := buildRef(2, "beta")
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  "DEAD",
		"edits": []any{
			map[string]any{"from": betaRef, "to": betaRef, "content": "BETA"},
		},
	}, tmpDir)
	if res.Success || !strings.Contains(res.Error, "stale") {
		t.Fatalf("stale file tag must be rejected, got: %+v", res)
	}
	// File must be unchanged.
	got, _ := os.ReadFile(filePath)
	if string(got) != "alpha\nbeta\n" {
		t.Fatalf("file was silently modified to %q", string(got))
	}
}

func TestEditStaleLineHashRejected(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "linestale.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := readForEdit(t, filePath, tmpDir)

	// Wrong hash for line 2.
	wrongRef := buildRef(2, "DIFFERENT_CONTENT")
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": wrongRef, "to": wrongRef, "content": "X"},
		},
	}, tmpDir)
	if res.Success || !strings.Contains(res.Error, "stale") {
		t.Fatalf("stale line hash must be rejected, got: %+v", res)
	}
}

func TestEditSingleLineReplace(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "replace.txt")
	if err := os.WriteFile(filePath, []byte("count := 1\nprint(count)\nreturn count\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := readForEdit(t, filePath, tmpDir)
	ref := buildRef(1, "count := 1")
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": ref, "to": ref, "content": "count := 42"},
		},
	}, tmpDir)
	if !res.Success {
		t.Fatalf("single-line replace failed: %s", res.Error)
	}
	got, _ := os.ReadFile(filePath)
	if !strings.Contains(string(got), "count := 42") {
		t.Fatalf("file content = %q", string(got))
	}
}

func TestEditRangeReplace(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "range.txt")
	if err := os.WriteFile(filePath, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := readForEdit(t, filePath, tmpDir)
	fromRef := buildRef(2, "b")
	toRef := buildRef(3, "c")
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": fromRef, "to": toRef, "content": "X\nY"},
		},
	}, tmpDir)
	if !res.Success {
		t.Fatalf("range replace failed: %s", res.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "a\nX\nY\nd\n" {
		t.Fatalf("file content = %q", string(got))
	}
}

func TestEditDeleteLine(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "delete.txt")
	if err := os.WriteFile(filePath, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := readForEdit(t, filePath, tmpDir)
	ref := buildRef(2, "b")
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": ref, "to": ref}, // no content = delete
		},
	}, tmpDir)
	if !res.Success {
		t.Fatalf("delete line failed: %s", res.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "a\nc\n" {
		t.Fatalf("file content = %q", string(got))
	}
}

func TestResetFileViewsForgetsObservations(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "reset.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readForEdit(t, filePath, tmpDir)

	// /clear and session switches reset the views.
	ResetFileViews()

	tag := ComputeFileHash("hello\n")
	helloRef := buildRef(1, "hello")
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": helloRef, "to": helloRef, "content": "goodbye"},
		},
	}, tmpDir)
	if res.Success || !strings.Contains(res.Error, "has not been read in this session") {
		t.Fatalf("edit after view reset must require a fresh read, got: %+v", res)
	}
}

func TestEditAfterOwnWriteNeedsNoRead(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "hello.txt")

	// Write a new file — no Read needed.
	written := (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "first\nsecond\n",
	}, tmpDir)
	if !written.Success {
		t.Fatalf("write failed: %s", written.Error)
	}

	// Edit immediately after Write — view is current because Write recorded it.
	tag := ComputeFileHash("first\nsecond\n")
	firstRef := buildRef(1, "first")
	res := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": firstRef, "to": firstRef, "content": "FIRST"},
		},
	}, tmpDir)
	if !res.Success {
		t.Fatalf("edit after own Write should need no Read, got: %s", res.Error)
	}
}

func TestEditKeepsOwnWriteFresh(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "chain.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure the second edit happens with a different mtime than the read.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filePath, old, old); err != nil {
		t.Fatal(err)
	}

	tag := readForEdit(t, filePath, tmpDir)

	// First edit: replace "one".
	oneRef := buildRef(1, "one")
	res1 := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag,
		"edits": []any{
			map[string]any{"from": oneRef, "to": oneRef, "content": "1"},
		},
	}, tmpDir)
	if !res1.Success {
		t.Fatalf("first edit failed: %s", res1.Error)
	}

	// Second edit: re-read first (snapshot invalidated after edit).
	tag2 := readForEdit(t, filePath, tmpDir)
	twoRef := buildRef(2, "two")
	res2 := (&EditTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"file_tag":  tag2,
		"edits": []any{
			map[string]any{"from": twoRef, "to": twoRef, "content": "2"},
		},
	}, tmpDir)
	if !res2.Success {
		t.Fatalf("second edit failed: %s", res2.Error)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "1\n2\n" {
		t.Fatalf("file content = %q", string(got))
	}
}

func TestWriteOverwriteRequiresCurrentViewFromEdit(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "target2.txt")
	if err := os.WriteFile(filePath, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "replaced\n",
	}, tmpDir)
	if res.Success || !strings.Contains(res.Error, "has not been read in this session") {
		t.Fatalf("overwrite without read must be rejected, got: %+v", res)
	}

	readForEdit(t, filePath, tmpDir)
	res = (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "replaced\n",
	}, tmpDir)
	if !res.Success {
		t.Fatalf("overwrite after read should succeed, got: %s", res.Error)
	}
	if !strings.Contains(res.Output, "use Edit for modifications") {
		t.Fatalf("overwrite result should nudge toward Edit, got: %s", res.Output)
	}
}

func TestWriteNewFileNeedsNoReadFromEdit(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "fresh2.txt")
	res := (&WriteTool{}).ExecuteApproved(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "hello\n",
	}, tmpDir)
	if !res.Success {
		t.Fatalf("creating a new file must not require a read, got: %s", res.Error)
	}
	if strings.Contains(res.Output, "use Edit for modifications") {
		t.Fatalf("creating a new file should not carry the overwrite note, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "no need to re-read") {
		t.Fatalf("create result should suppress the verify-read reflex, got: %s", res.Output)
	}
}
