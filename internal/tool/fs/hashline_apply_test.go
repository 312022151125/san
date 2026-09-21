package fs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// buildRef creates a "LINE#hash" anchor string for the given line content.
// e.g. buildRef(2, "beta") → "2#xyz" where xyz = ComputeLineHash("beta").
func buildRef(lineNo int, content string) string {
	return fmt.Sprintf("%d#%s", lineNo, ComputeLineHash(content))
}

// strPtr returns a pointer to a string literal.
func strPtr(s string) *string { return &s }

// --- ApplyHashlineEdits tests -----------------------------------------------

func TestApplyHashlineEdits_ReplaceSingleLine(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(2, "beta"),
		To:      buildRef(2, "beta"),
		Content: strPtr("BETA"),
	}}
	got, err := ApplyHashlineEdits(content, tag, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "alpha\nBETA\ngamma\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplyHashlineEdits_ReplaceRange(t *testing.T) {
	content := "a\nb\nc\nd\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(2, "b"),
		To:      buildRef(3, "c"),
		Content: strPtr("X\nY"),
	}}
	got, err := ApplyHashlineEdits(content, tag, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "a\nX\nY\nd\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplyHashlineEdits_DeleteSingleLine(t *testing.T) {
	content := "a\nb\nc\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(2, "b"),
		To:      buildRef(2, "b"),
		Content: nil, // delete
	}}
	got, err := ApplyHashlineEdits(content, tag, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "a\nc\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplyHashlineEdits_DeleteRange(t *testing.T) {
	content := "a\nb\nc\nd\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(2, "b"),
		To:      buildRef(3, "c"),
		Content: nil,
	}}
	got, err := ApplyHashlineEdits(content, tag, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "a\nd\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplyHashlineEdits_MultipleEdits(t *testing.T) {
	content := "line1\nline2\nline3\nline4\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{
		{From: buildRef(1, "line1"), To: buildRef(1, "line1"), Content: strPtr("LINE_ONE")},
		{From: buildRef(3, "line3"), To: buildRef(3, "line3"), Content: strPtr("LINE_THREE")},
	}
	got, err := ApplyHashlineEdits(content, tag, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "LINE_ONE\nline2\nLINE_THREE\nline4\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplyHashlineEdits_StaleFileTag(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	edits := []HashlineEdit{{
		From:    buildRef(2, "beta"),
		To:      buildRef(2, "beta"),
		Content: strPtr("BETA"),
	}}
	_, err := ApplyHashlineEdits(content, "DEAD", edits)
	if err == nil {
		t.Fatal("expected error for stale file tag, got nil")
	}
	var tagErr *ErrFileTagMismatch
	if !errors.As(err, &tagErr) {
		t.Errorf("expected *ErrFileTagMismatch, got %T: %v", err, err)
	}
}

func TestApplyHashlineEdits_StaleLineHash(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(2, "STALE_CONTENT"),
		To:      buildRef(2, "STALE_CONTENT"),
		Content: strPtr("new"),
	}}
	_, err := ApplyHashlineEdits(content, tag, edits)
	if err == nil {
		t.Fatal("expected error for stale line hash, got nil")
	}
	var mmErr *HashlineMismatchError
	if !errors.As(err, &mmErr) {
		t.Errorf("expected *HashlineMismatchError, got %T: %v", err, err)
	}
	if len(mmErr.Mismatches) == 0 {
		t.Error("expected at least one mismatch")
	}
	if mmErr.Mismatches[0].Line != 2 {
		t.Errorf("mismatch line = %d want 2", mmErr.Mismatches[0].Line)
	}
}

func TestApplyHashlineEdits_StaleToHash(t *testing.T) {
	content := "a\nb\nc\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(2, "b"),
		To:      buildRef(3, "WRONG"),
		Content: strPtr("replacement"),
	}}
	_, err := ApplyHashlineEdits(content, tag, edits)
	if err == nil {
		t.Fatal("expected error for stale to-hash")
	}
	var mmErr *HashlineMismatchError
	if !errors.As(err, &mmErr) {
		t.Errorf("expected *HashlineMismatchError, got %T: %v", err, err)
	}
}

func TestApplyHashlineEdits_OverlappingEdits(t *testing.T) {
	content := "a\nb\nc\nd\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{
		{From: buildRef(2, "b"), To: buildRef(3, "c"), Content: strPtr("X")},
		{From: buildRef(3, "c"), To: buildRef(4, "d"), Content: strPtr("Y")},
	}
	_, err := ApplyHashlineEdits(content, tag, edits)
	if err == nil {
		t.Fatal("expected error for overlapping edits")
	}
	if !strings.Contains(err.Error(), "overlap") {
		t.Errorf("expected 'overlap' in error, got: %v", err)
	}
}

func TestApplyHashlineEdits_EmptyFile(t *testing.T) {
	content := ""
	tag := ComputeFileHash(content)
	// No edits on empty file should be a no-op.
	got, err := ApplyHashlineEdits(content, tag, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != content {
		t.Errorf("got %q want %q", got, content)
	}
}

func TestApplyHashlineEdits_CRLF(t *testing.T) {
	// The apply engine receives LF-normalized content; CRLF restoration is
	// handled above (in ExecuteApproved). Verify that CRLF input in content
	// field of edits is handled.
	content := "alpha\nbeta\ngamma\n"
	tag := ComputeFileHash(content)
	replacement := "X\r\nY" // CRLF in replacement — gets normalised to LF
	edits := []HashlineEdit{{
		From:    buildRef(2, "beta"),
		To:      buildRef(2, "beta"),
		Content: strPtr(replacement),
	}}
	got, err := ApplyHashlineEdits(content, tag, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// CRLF in content is normalised during splitContentLines.
	want := "alpha\nX\nY\ngamma\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplyHashlineEdits_Unicode(t *testing.T) {
	content := "日本語\nこんにちは\n世界\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(2, "こんにちは"),
		To:      buildRef(2, "こんにちは"),
		Content: strPtr("Hello"),
	}}
	got, err := ApplyHashlineEdits(content, tag, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "日本語\nHello\n世界\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestApplyHashlineEdits_MissingFileTag(t *testing.T) {
	content := "a\n"
	edits := []HashlineEdit{{From: buildRef(1, "a"), To: buildRef(1, "a"), Content: strPtr("b")}}
	_, err := ApplyHashlineEdits(content, "", edits)
	if err == nil {
		t.Fatal("expected error for empty file_tag")
	}
}

func TestApplyHashlineEdits_NormalizeFileTagForms(t *testing.T) {
	content := "alpha\nbeta\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(1, "alpha"),
		To:      buildRef(1, "alpha"),
		Content: strPtr("ALPHA"),
	}}
	// All these tag forms should be accepted.
	forms := []string{
		tag,
		"#" + tag,
		"@file file.txt#" + tag,
		strings.ToLower(tag),
	}
	for _, form := range forms {
		got, err := ApplyHashlineEdits(content, form, edits)
		if err != nil {
			t.Errorf("form %q: unexpected error: %v", form, err)
			continue
		}
		want := "ALPHA\nbeta\n"
		if got != want {
			t.Errorf("form %q: got %q want %q", form, got, want)
		}
	}
}

func TestApplyHashlineEdits_FromGtToError(t *testing.T) {
	content := "a\nb\nc\n"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(3, "c"), // from > to
		To:      buildRef(1, "a"),
		Content: strPtr("x"),
	}}
	_, err := ApplyHashlineEdits(content, tag, edits)
	if err == nil {
		t.Fatal("expected error for from > to")
	}
}

func TestApplyHashlineEdits_ReplaceLastLine(t *testing.T) {
	content := "first\nlast"
	tag := ComputeFileHash(content)
	edits := []HashlineEdit{{
		From:    buildRef(2, "last"),
		To:      buildRef(2, "last"),
		Content: strPtr("LAST"),
	}}
	got, err := ApplyHashlineEdits(content, tag, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "first\nLAST"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
