package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkTree creates a small directory tree for glob tests.
//
//	root/
//	  a.go
//	  b.go
//	  sub/
//	    c.go
//	    d.ts
//	  sub2/
//	    e.go
func mkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dirs := []string{
		filepath.Join(root, "sub"),
		filepath.Join(root, "sub2"),
	}
	for _, d := range dirs {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(root, "a.go"):        "package main\n",
		filepath.Join(root, "b.go"):        "package main\n",
		filepath.Join(root, "sub", "c.go"): "package sub\n",
		filepath.Join(root, "sub", "d.ts"): "export {}\n",
		filepath.Join(root, "sub2", "e.go"): "package sub2\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGlob_RecursivePattern(t *testing.T) {
	root := mkTree(t)
	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern": "**/*.go",
		"path":    root,
	}, root)
	if !result.Success {
		t.Fatalf("glob failed: %s", result.Error)
	}
	// Should find a.go, b.go, sub/c.go, sub2/e.go (4 files).
	got := parseGlobLines(result.Output)
	if len(got) != 4 {
		t.Errorf("expected 4 .go files, got %d: %v", len(got), got)
	}
	for _, f := range got {
		if !strings.HasSuffix(f, ".go") {
			t.Errorf("unexpected non-.go file %q", f)
		}
	}
}

func TestGlob_SingleLevelPattern(t *testing.T) {
	root := mkTree(t)
	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern": "*.go",
		"path":    root,
	}, root)
	if !result.Success {
		t.Fatalf("glob failed: %s", result.Error)
	}
	got := parseGlobLines(result.Output)
	// Only a.go and b.go are at root level.
	if len(got) != 2 {
		t.Errorf("single-level *.go should find 2 files, got %d: %v", len(got), got)
	}
	for _, f := range got {
		if strings.Contains(f, "/") {
			t.Errorf("single-level pattern should not match subdirectory files, got %q", f)
		}
	}
}

func TestGlob_WildcardInSubdir(t *testing.T) {
	root := mkTree(t)
	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern": "sub/*.go",
		"path":    root,
	}, root)
	if !result.Success {
		t.Fatalf("glob failed: %s", result.Error)
	}
	got := parseGlobLines(result.Output)
	if len(got) != 1 {
		t.Errorf("sub/*.go should find 1 file, got %d: %v", len(got), got)
	}
	if len(got) > 0 && !strings.Contains(got[0], "c.go") {
		t.Errorf("expected c.go, got %q", got[0])
	}
}

func TestGlob_ExcludeFilter(t *testing.T) {
	root := mkTree(t)
	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern": "**/*.go",
		"path":    root,
		"exclude": "b.go",
	}, root)
	if !result.Success {
		t.Fatalf("glob failed: %s", result.Error)
	}
	out := result.Output
	if strings.Contains(out, "b.go") {
		t.Errorf("exclude=b.go should remove it from results, got:\n%s", out)
	}
}

func TestGlob_SkipDirs(t *testing.T) {
	root := t.TempDir()
	// Place a .go file inside a skip dir and one outside.
	gitDir := filepath.Join(root, ".git")
	nodeDir := filepath.Join(root, "node_modules")
	for _, d := range []string{gitDir, nodeDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "secret.go"), []byte("should not match\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "real.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern": "**/*.go",
		"path":    root,
	}, root)
	if !result.Success {
		t.Fatalf("glob failed: %s", result.Error)
	}
	out := result.Output
	for _, skipName := range []string{".git", "node_modules"} {
		if strings.Contains(out, skipName) {
			t.Errorf("skip dir %q should be excluded, got:\n%s", skipName, out)
		}
	}
	if !strings.Contains(out, "real.go") {
		t.Errorf("real.go should appear in results, got:\n%s", out)
	}
}

func TestGlob_MaxResults(t *testing.T) {
	root := t.TempDir()
	// Create 20 files.
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern":     "*.txt",
		"path":        root,
		"max_results": float64(5),
	}, root)
	if !result.Success {
		t.Fatalf("glob failed: %s", result.Error)
	}
	got := parseGlobLines(result.Output)
	if len(got) != 5 {
		t.Errorf("expected 5 results due to cap, got %d", len(got))
	}
	if !result.Metadata.Truncated {
		t.Error("Metadata.Truncated should be true when cap is hit")
	}
	if !strings.Contains(result.Output, "truncated") {
		t.Errorf("output should mention truncation, got:\n%s", result.Output)
	}
}

func TestGlob_NonexistentBase(t *testing.T) {
	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern": "**/*.go",
		"path":    "/nonexistent/path/that/does/not/exist",
	}, "/tmp")
	// Should return an error result gracefully, not panic.
	if result.Success {
		t.Logf("output: %s", result.Output)
	}
	// Either an error or empty results is acceptable — the key is no panic.
}

func TestGlob_EmptyDir(t *testing.T) {
	root := t.TempDir()
	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern": "**/*.go",
		"path":    root,
	}, root)
	if !result.Success {
		t.Fatalf("glob of empty dir should succeed, got: %s", result.Error)
	}
	if !strings.Contains(result.Output, "no files matched") {
		t.Errorf("empty glob should say 'no files matched', got:\n%s", result.Output)
	}
}

func TestGlob_StarStarMatchesZeroComponents(t *testing.T) {
	// "**/*.go" should also match root-level .go files (zero path components consumed by **).
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern": "**/*.go",
		"path":    root,
	}, root)
	if !result.Success {
		t.Fatalf("glob failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "main.go") {
		t.Errorf("**/*.go should match root-level main.go, got:\n%s", result.Output)
	}
}

// parseGlobLines splits the output into non-empty, non-notice lines.
func parseGlobLines(output string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "(") {
			continue
		}
		out = append(out, line)
	}
	return out
}
