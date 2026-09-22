package fs

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// globSkipDirs is the set of directory names the Glob walker always skips.
// It mirrors grepSkipDirs from grep.go.
var globSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".cache":       true,
	"__pycache__":  true,
	".next":        true,
	"target":       true,
	".bob":         true,
	"graphify-out": true,
}

const (
	globDefaultMax = 200
	globMaxResults = 500 // hard ceiling
)

// GlobTool finds files matching a glob pattern.
type GlobTool struct{}

func (t *GlobTool) Name() string        { return "Glob" }
func (t *GlobTool) Description() string { return "Find files matching a glob pattern" }
func (t *GlobTool) Icon() string        { return toolresult.IconGlob }

func (t *GlobTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Glob",
		Description: fmt.Sprintf(`Find files matching a glob pattern.

- Supports ** for recursive matching (e.g. "**/*.go" matches all Go files anywhere).
- Results are relative paths sorted lexicographically, capped at max_results (default %d).
- Common heavy directories (.git, node_modules, vendor, build, dist, .cache, etc.) are always skipped.
- Use exclude to filter out additional paths by filename glob.`, globDefaultMax),
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern to match files, e.g. \"**/*.go\", \"src/*.ts\", \"*.md\".",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Base directory to search within. Defaults to the session working directory.",
				},
				"exclude": map[string]any{
					"type":        "string",
					"description": "Glob pattern for filenames to exclude (e.g. \"*_test.go\").",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum number of files to return (default %d, max %d).", globDefaultMax, globMaxResults),
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *GlobTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()

	pattern, err := tool.RequireString(params, "pattern")
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	basePath := tool.GetString(params, "path")
	if basePath == "" {
		basePath = "."
	}
	if !filepath.IsAbs(basePath) {
		basePath = filepath.Join(cwd, basePath)
	}

	exclude := tool.GetString(params, "exclude")
	maxResults := tool.GetInt(params, "max_results", globDefaultMax)
	if maxResults <= 0 || maxResults > globMaxResults {
		maxResults = globDefaultMax
	}

	// Normalise the pattern to forward slashes so segment splitting works on
	// all platforms.
	pattern = filepath.ToSlash(pattern)

	var matches []string
	truncated := false

	walkErr := filepath.WalkDir(basePath, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // skip unreadable entries
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if d.IsDir() {
			if globSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		// Compute the path relative to basePath using forward slashes.
		rel, relErr := filepath.Rel(basePath, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		// Apply exclude filter on the filename part only.
		if exclude != "" {
			skip, _ := filepath.Match(exclude, d.Name())
			if skip {
				return nil
			}
		}

		if matchGlobPattern(pattern, rel) {
			matches = append(matches, rel)
			if len(matches) > maxResults {
				truncated = true
				return errGlobCap
			}
		}
		return nil
	})

	if walkErr != nil && walkErr != errGlobCap {
		return toolresult.NewErrorResult(t.Name(), "glob walk error: "+walkErr.Error())
	}

	if truncated {
		matches = matches[:maxResults]
	}
	sort.Strings(matches)

	var sb strings.Builder
	for _, m := range matches {
		sb.WriteString(m)
		sb.WriteByte('\n')
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("[results truncated at %d files; use a more specific pattern or path]\n", maxResults))
	}
	if len(matches) == 0 {
		sb.WriteString("(no files matched)\n")
	}

	return toolresult.ToolResult{
		Success: true,
		Output:  sb.String(),
		Metadata: toolresult.ResultMetadata{
			Title:     t.Name(),
			Icon:      t.Icon(),
			Subtitle:  basePath,
			ItemCount: len(matches),
			Duration:  time.Since(start),
			Truncated: truncated,
		},
	}
}

// matchGlobPattern reports whether relPath (forward-slash separated, e.g.
// "internal/tool/fs/read.go") matches pattern (e.g. "**/*.go", "src/*.ts").
//
// Pattern semantics:
//   - A "**" segment matches zero or more path components.
//   - All other segments are matched with filepath.Match (standard glob).
//   - A leading "**/" is treated as "anywhere in the tree".
func matchGlobPattern(pattern, relPath string) bool {
	patSegs := strings.Split(pattern, "/")
	pathSegs := strings.Split(relPath, "/")
	return matchSegs(patSegs, pathSegs)
}

// matchSegs recursively matches pattern segments against path segments.
func matchSegs(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		// ** matches zero or more components: try consuming 0..len(path) segments.
		for i := 0; i <= len(path); i++ {
			if matchSegs(pat[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	matched, err := filepath.Match(pat[0], path[0])
	if err != nil || !matched {
		return false
	}
	return matchSegs(pat[1:], path[1:])
}

// errGlobCap is a sentinel used to abort WalkDir early when the cap is hit.
var errGlobCap = fmt.Errorf("glob cap reached")

func init() {
	tool.Register(&GlobTool{})
}
