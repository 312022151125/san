package finalize

import (
	"path/filepath"
	"strings"
)

// Tier is the verification depth selected for a given change set.
type Tier int

const (
	// TierFast: formatting check + compile-check on directly changed packages.
	// Used for documentation/comment-only changes.
	TierFast Tier = iota

	// TierStandard: affected-scope test + vet + build. Used for localized
	// implementation or test changes that do not touch shared infrastructure.
	TierStandard

	// TierFull: repository-wide test + vet + build. Used for risky changes:
	// shared interfaces, core runtime, session infrastructure, go.mod, etc.
	TierFull
)

func (t Tier) String() string {
	switch t {
	case TierFast:
		return "fast"
	case TierStandard:
		return "standard"
	case TierFull:
		return "full"
	default:
		return "unknown"
	}
}

// riskyPrefixes are file path prefixes (relative to repo root) that force
// FULL verification. These are the paths whose changes can silently break
// consumers in other packages that are not obviously related.
//
// This table is the canonical auditable list — keep it simple and correct.
// No model call is made to evaluate these rules.
var riskyPrefixes = []string{
	"internal/core/",
	"internal/session/",
	"internal/subagent/",
	"internal/llm/",
	"internal/tool/batch/",
	"internal/agent/",
	"internal/broker/",
	"internal/task/",
}

// riskyFiles are specific filenames (basename only) that force FULL regardless
// of their package location.
var riskyFiles = []string{
	"go.mod",
	"go.sum",
	"Makefile",
	"Dockerfile",
	".goreleaser.yml",
	".goreleaser.yaml",
}

// riskyFileSuffixes are filename suffixes that force FULL.
var riskyFileSuffixes = []string{
	"_interface.go",
}

// ClassifyTier returns the cheapest verification tier that gives reasonable
// confidence for the given changed file set. The classification is
// deterministic and requires no model call.
//
//   - Empty file list → TierFull (no diff info → safe default)
//   - Any risky path → TierFull
//   - Files in > 2 packages → TierFull
//   - Comments/docs only → TierFast
//   - Single-package or test-only → TierStandard
func ClassifyTier(files []string) Tier {
	if len(files) == 0 {
		// No diff information available; use full to be safe.
		return TierFull
	}

	// Check risky files first — any match forces FULL immediately.
	for _, f := range files {
		if isRiskyFile(f) {
			return TierFull
		}
	}

	// Count distinct packages containing changed .go files.
	pkgCount := len(ExtractGoPackages("", files))

	// More than 2 packages changed → FULL.
	if pkgCount > 2 {
		return TierFull
	}

	// If no .go files changed at all (only docs, configs, etc.) → FAST.
	if pkgCount == 0 {
		return TierFast
	}

	// If every changed .go file is comment/docs only → FAST.
	// We approximate this by checking if all changed files are either
	// non-Go or have names suggesting documentation.
	if areDocsOnly(files) {
		return TierFast
	}

	// 1–2 packages, implementation or test files → STANDARD.
	return TierStandard
}

// IsReviewerNeeded reports whether the reviewer agent should be launched for
// the given files in the given mode ("smart", "always", "off").
//
// In "smart" mode the reviewer is skipped for:
//   - FAST-tier changes (docs/comments only)
//   - Test-only changes in a single package
//
// In "always" mode the reviewer always runs.
// In "off" mode the reviewer never runs.
func IsReviewerNeeded(files []string, mode string) bool {
	switch mode {
	case "off":
		return false
	case "always":
		return true
	default: // "smart" or empty
		tier := ClassifyTier(files)
		if tier == TierFast {
			return false
		}
		// Test-only single-package change → skip reviewer.
		if isTestOnlySinglePackage(files) {
			return false
		}
		// Risky files always get reviewer.
		return true
	}
}

// isRiskyFile returns true when the file should force FULL verification.
func isRiskyFile(f string) bool {
	// Normalise to forward slashes for consistent prefix matching.
	f = filepath.ToSlash(f)
	base := filepath.Base(f)

	for _, rf := range riskyFiles {
		if base == rf {
			return true
		}
	}
	for _, suffix := range riskyFileSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	// Check path has "types.go" in a shared package (heuristic: not _test.go).
	if base == "types.go" && !strings.Contains(f, "_test") {
		return true
	}
	for _, prefix := range riskyPrefixes {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

// areDocsOnly returns true when all changed files are non-Go or are Go files
// that appear to contain only documentation changes (e.g. doc.go, README).
// This is a heuristic: we check file names, not file content.
func areDocsOnly(files []string) bool {
	docExtensions := map[string]bool{
		".md": true, ".txt": true, ".rst": true,
		".adoc": true, ".asciidoc": true,
	}
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		base := filepath.Base(f)
		if docExtensions[ext] {
			continue
		}
		if ext == ".go" {
			// doc.go files are documentation by convention.
			if base == "doc.go" {
				continue
			}
			// Any other .go file is not docs-only.
			return false
		}
		// Non-.go, non-doc extension: treat as non-docs (e.g. .yaml, .sh)
		// so we don't accidentally skip verification for config changes.
		return false
	}
	return true
}

// isTestOnlySinglePackage returns true when all changed .go files are test
// files in a single package.
func isTestOnlySinglePackage(files []string) bool {
	var pkg string
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		if !strings.HasSuffix(f, "_test.go") {
			return false
		}
		dir := filepath.ToSlash(filepath.Dir(f))
		if pkg == "" {
			pkg = dir
		} else if pkg != dir {
			return false
		}
	}
	return pkg != ""
}
