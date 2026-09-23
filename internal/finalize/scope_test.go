package finalize

import (
	"testing"
)

// --- ExtractGoPackages ---

func TestExtractGoPackages_BasicMapping(t *testing.T) {
	files := []string{
		"internal/finalize/scope.go",
		"internal/finalize/tier.go",
		"internal/tool/batch/batch.go",
	}
	pkgs := ExtractGoPackages("", files)
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d: %v", len(pkgs), pkgs)
	}
	wantPkgs := map[string]bool{
		"./internal/finalize":    true,
		"./internal/tool/batch": true,
	}
	for _, p := range pkgs {
		if !wantPkgs[p] {
			t.Errorf("unexpected package %q", p)
		}
	}
}

func TestExtractGoPackages_IgnoresNonGo(t *testing.T) {
	files := []string{
		"README.md",
		"go.mod",
		"docs/index.md",
	}
	pkgs := ExtractGoPackages("", files)
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages for non-Go files, got %v", pkgs)
	}
}

func TestExtractGoPackages_Deduplicates(t *testing.T) {
	files := []string{
		"internal/finalize/scope.go",
		"internal/finalize/tier.go",
		"internal/finalize/dag.go",
	}
	pkgs := ExtractGoPackages("", files)
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package (deduplicated), got %d: %v", len(pkgs), pkgs)
	}
	if pkgs[0] != "./internal/finalize" {
		t.Errorf("expected ./internal/finalize, got %q", pkgs[0])
	}
}

func TestExtractGoPackages_Sorted(t *testing.T) {
	files := []string{
		"z/z.go",
		"a/a.go",
		"m/m.go",
	}
	pkgs := ExtractGoPackages("", files)
	for i := 1; i < len(pkgs); i++ {
		if pkgs[i] < pkgs[i-1] {
			t.Errorf("packages not sorted: %v", pkgs)
		}
	}
}

func TestExtractGoPackages_TestFiles(t *testing.T) {
	files := []string{
		"internal/finalize/scope_test.go",
		"internal/finalize/tier_test.go",
	}
	pkgs := ExtractGoPackages("", files)
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %v", pkgs)
	}
}

func TestExtractGoPackages_EmptyInput(t *testing.T) {
	pkgs := ExtractGoPackages("", nil)
	if pkgs != nil {
		t.Errorf("expected nil for empty input, got %v", pkgs)
	}
}

// --- parseLines ---

func TestParseLines_Basic(t *testing.T) {
	lines := parseLines("a\nb\n\nc\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
}

func TestParseLines_EmptyInput(t *testing.T) {
	lines := parseLines("")
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for empty input, got %v", lines)
	}
}
