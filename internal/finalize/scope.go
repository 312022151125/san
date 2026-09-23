package finalize

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// DetectChangedFiles returns the list of files changed relative to HEAD in the
// given working directory. It runs "git diff --name-only HEAD" and returns the
// paths as reported by git (relative to the repository root).
//
// Returns an empty slice (not an error) when cwd is not a git repository or
// when git is not installed — in that case the caller should fall back to FULL
// tier rather than failing.
func DetectChangedFiles(ctx context.Context, cwd string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		// Not a git repo or git unavailable → treat as no changed-file info.
		return nil, nil //nolint:nilerr
	}
	return parseLines(string(out)), nil
}

// DetectChangedFilesUnstaged returns files with uncommitted changes (both
// staged and unstaged), using "git status --porcelain" to catch new files not
// yet committed. Used as a fallback when HEAD diff returns nothing but the
// working tree has modifications.
func DetectChangedFilesUnstaged(ctx context.Context, cwd string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	var files []string
	for _, line := range parseLines(string(out)) {
		if len(line) < 4 {
			continue
		}
		// Porcelain format: "XY path" or "XY oldpath -> newpath"
		path := strings.TrimSpace(line[3:])
		if idx := strings.LastIndex(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// ExtractGoPackages maps a list of file paths (relative to cwd) to the
// minimal set of Go package patterns suitable for "go test" and "go vet".
//
// Rules:
//   - Only .go files are considered.
//   - Each unique directory containing a .go file becomes a pattern.
//   - The pattern is "./dir" (relative, for use in go test ./dir/...).
//   - Patterns are deduplicated and sorted for determinism.
func ExtractGoPackages(cwd string, files []string) []string {
	seen := make(map[string]bool)
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		dir := filepath.Dir(f)
		if dir == "." || dir == "" {
			dir = "."
		}
		// Normalise to forward-slash relative pattern for go tooling.
		pkg := "./" + filepath.ToSlash(dir)
		seen[pkg] = true
	}
	if len(seen) == 0 {
		return nil
	}
	result := make([]string, 0, len(seen))
	for pkg := range seen {
		result = append(result, pkg)
	}
	sortStrings(result)
	return result
}

// parseLines splits output by newlines, trims whitespace, and drops blank
// lines.
func parseLines(s string) []string {
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// sortStrings sorts a string slice in place (stdlib sort avoidance: use
// simple insertion sort for small slices; real usage is always small).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
