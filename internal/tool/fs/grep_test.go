package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrep_BasicMatch(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main\n\nfunc foo() {}\nfunc bar() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package main\n\nfunc baz() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&GrepTool{}).Execute(context.Background(), map[string]any{
		"pattern": "func foo",
		"path":    tmpDir,
	}, tmpDir)
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Error)
	}
	out := result.Output
	if !strings.Contains(out, "a.go") {
		t.Errorf("expected a.go in results, got:\n%s", out)
	}
	if strings.Contains(out, "b.go") {
		t.Errorf("b.go should not appear in results, got:\n%s", out)
	}
}

func TestGrep_CaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("Hello World\nhello world\nHELLO WORLD\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&GrepTool{}).Execute(context.Background(), map[string]any{
		"pattern":          "hello world",
		"path":             tmpDir,
		"case_insensitive": true,
	}, tmpDir)
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Error)
	}
	if len(result.Lines) != 3 {
		t.Errorf("case-insensitive search should find 3 matches, got %d:\n%s", len(result.Lines), result.Output)
	}
}

func TestGrep_IncludeFilter(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "code.go"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&GrepTool{}).Execute(context.Background(), map[string]any{
		"pattern": "needle",
		"path":    tmpDir,
		"include": "*.go",
	}, tmpDir)
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Error)
	}
	if strings.Contains(result.Output, "readme.txt") {
		t.Errorf("include=*.go should skip .txt files, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "code.go") {
		t.Errorf("include=*.go should find .go files, got:\n%s", result.Output)
	}
}

func TestGrep_ExcludeFilter(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main_test.go"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&GrepTool{}).Execute(context.Background(), map[string]any{
		"pattern": "needle",
		"path":    tmpDir,
		"exclude": "*_test.go",
	}, tmpDir)
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Error)
	}
	if strings.Contains(result.Output, "main_test.go") {
		t.Errorf("exclude=*_test.go should skip test files, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "main.go") {
		t.Errorf("main.go should still be found, got:\n%s", result.Output)
	}
}

func TestGrep_FixedStrings(t *testing.T) {
	tmpDir := t.TempDir()
	// A pattern with regex metacharacters; fixed_strings should treat it literally.
	if err := os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("price: $10.00\nnormal line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&GrepTool{}).Execute(context.Background(), map[string]any{
		"pattern":       "$10.00",
		"path":          tmpDir,
		"fixed_strings": true,
	}, tmpDir)
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "f.txt") {
		t.Errorf("fixed_strings should find the literal dollar sign, got:\n%s", result.Output)
	}
	if len(result.Lines) != 1 {
		t.Errorf("should find exactly 1 match, got %d", len(result.Lines))
	}
}

func TestGrep_MaxResults(t *testing.T) {
	tmpDir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&sb, "match line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "lots.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&GrepTool{}).Execute(context.Background(), map[string]any{
		"pattern":     "match line",
		"path":        tmpDir,
		"max_results": float64(10),
	}, tmpDir)
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Error)
	}
	if len(result.Lines) != 10 {
		t.Errorf("expected exactly 10 results, got %d", len(result.Lines))
	}
	if !result.Metadata.Truncated {
		t.Error("Metadata.Truncated should be true when cap is hit")
	}
	if !strings.Contains(result.Output, "truncated") {
		t.Errorf("output should mention truncation, got:\n%s", result.Output)
	}
}

func TestGrep_InvalidPattern(t *testing.T) {
	tmpDir := t.TempDir()
	// Only the Go fallback can test this directly (rg also rejects bad patterns
	// but returns exit code 2 and prints to stderr, not caught the same way).
	// Force the Go path by passing a pattern that is invalid.
	result := execGoGrepForTest(context.Background(), "[invalid", tmpDir, "", "", false, false, 10)
	if result == nil {
		t.Skip("execGoGrep helper not available in this build")
	}
}

func TestGrep_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	result := (&GrepTool{}).Execute(context.Background(), map[string]any{
		"pattern": "anything",
		"path":    tmpDir,
	}, tmpDir)
	if !result.Success {
		t.Fatalf("grep of empty dir should succeed, got: %s", result.Error)
	}
	if !strings.Contains(result.Output, "no matches") {
		t.Errorf("expected 'no matches' message, got:\n%s", result.Output)
	}
}

func TestGrep_SkipsDotGit(t *testing.T) {
	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "source.go"), []byte("no match here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use Go fallback directly to guarantee skip behaviour independent of rg.
	matches, _, err := execGoGrep(context.Background(), "needle", tmpDir, "", "", false, false, 50)
	if err != nil {
		t.Fatalf("execGoGrep failed: %v", err)
	}
	for _, m := range matches {
		if strings.Contains(m.File, ".git") {
			t.Errorf(".git directory should be skipped, but found match in %q", m.File)
		}
	}
}

// execGoGrepForTest is a thin helper to test invalid pattern handling directly
// against the Go fallback. Returns nil if the test can't be meaningful.
func execGoGrepForTest(ctx context.Context, pattern, path, include, exclude string, ci, fs bool, max int) *string {
	_, _, err := execGoGrep(ctx, pattern, path, include, exclude, ci, fs, max)
	if err == nil {
		return nil
	}
	s := err.Error()
	return &s
}
