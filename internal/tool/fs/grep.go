package fs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// grepSkipDirs is the set of directory names that the Go fallback walker
// always skips. rg handles these itself via .gitignore and its own defaults.
var grepSkipDirs = map[string]bool{
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
	grepDefaultMax = 50
	grepMaxResults = 200 // hard ceiling regardless of param
)

// GrepTool searches file contents for a pattern.
type GrepTool struct{}

func (t *GrepTool) Name() string        { return "Grep" }
func (t *GrepTool) Description() string { return "Search file contents for a pattern" }
func (t *GrepTool) Icon() string        { return toolresult.IconGrep }

func (t *GrepTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Grep",
		Description: fmt.Sprintf(`Search file contents for a pattern.

- Uses ripgrep (rg) when available for speed and .gitignore support; falls back to a pure-Go walker.
- Returns at most max_results matches (default %d, hard ceiling %d).
- Results format: path:line_number:content, one per line.
- Use include/exclude to narrow the search by filename glob (e.g. include="*.go").
- Combine with Read(offset, limit) to read a specific window after locating a symbol.`, grepDefaultMax, grepMaxResults),
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regex pattern to search for. Use fixed_strings=true for a literal string.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "File or directory to search. Defaults to the session working directory.",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "Glob pattern for filenames to include (e.g. \"*.go\", \"*.{ts,tsx}\").",
				},
				"exclude": map[string]any{
					"type":        "string",
					"description": "Glob pattern for filenames to skip (e.g. \"*_test.go\").",
				},
				"case_insensitive": map[string]any{
					"type":        "boolean",
					"description": "Case-insensitive match. Default false.",
				},
				"fixed_strings": map[string]any{
					"type":        "boolean",
					"description": "Treat pattern as a literal string, not a regex. Default false.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum number of matches to return (default %d, max %d).", grepDefaultMax, grepMaxResults),
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *GrepTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()

	pattern, err := tool.RequireString(params, "pattern")
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	searchPath := tool.GetString(params, "path")
	if searchPath == "" {
		searchPath = "."
	}
	if !filepath.IsAbs(searchPath) {
		searchPath = filepath.Join(cwd, searchPath)
	}

	include := tool.GetString(params, "include")
	exclude := tool.GetString(params, "exclude")
	caseInsensitive := tool.GetBool(params, "case_insensitive")
	fixedStrings := tool.GetBool(params, "fixed_strings")
	maxResults := tool.GetInt(params, "max_results", grepDefaultMax)
	if maxResults <= 0 || maxResults > grepMaxResults {
		maxResults = grepDefaultMax
	}

	var matches []toolresult.ContentLine
	var truncated bool

	if rgPath, rgErr := exec.LookPath("rg"); rgErr == nil {
		matches, truncated, err = execRG(ctx, rgPath, pattern, searchPath, include, exclude, caseInsensitive, fixedStrings, maxResults)
	} else {
		matches, truncated, err = execGoGrep(ctx, pattern, searchPath, include, exclude, caseInsensitive, fixedStrings, maxResults)
	}
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	// Build relative paths and plain-text output.
	var sb strings.Builder
	for i := range matches {
		rel, relErr := filepath.Rel(cwd, matches[i].File)
		if relErr == nil {
			matches[i].File = rel
		}
		sb.WriteString(fmt.Sprintf("%s:%d:%s\n", matches[i].File, matches[i].LineNo, matches[i].Text))
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("[results truncated at %d matches; narrow the search with include= or a more specific path]\n", maxResults))
	}
	if len(matches) == 0 {
		sb.WriteString("(no matches found)\n")
	}

	return toolresult.ToolResult{
		Success: true,
		Output:  sb.String(),
		Lines:   matches,
		Metadata: toolresult.ResultMetadata{
			Title:     t.Name(),
			Icon:      t.Icon(),
			Subtitle:  searchPath,
			ItemCount: len(matches),
			Duration:  time.Since(start),
			Truncated: truncated,
		},
	}
}

// execRG runs ripgrep and streams its output, stopping once maxResults+1 lines
// have been collected. The process is cancelled via context when the cap is hit.
func execRG(ctx context.Context, rgPath, pattern, searchPath, include, exclude string, caseInsensitive, fixedStrings bool, maxResults int) ([]toolresult.ContentLine, bool, error) {
	args := []string{"--line-number", "--no-heading", "--color=never"}
	if caseInsensitive {
		args = append(args, "--ignore-case")
	} else {
		args = append(args, "--case-sensitive")
	}
	if fixedStrings {
		args = append(args, "--fixed-strings")
	}
	if include != "" {
		args = append(args, "--glob", include)
	}
	if exclude != "" {
		args = append(args, "--glob", "!"+exclude)
	}
	args = append(args, "--", pattern, searchPath)

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(cctx, rgPath, args...) //nolint:gosec // rgPath from LookPath, args caller-controlled
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, fmt.Errorf("rg stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("rg start: %w", err)
	}

	var matches []toolresult.ContentLine
	truncated := false
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		m := parseRGLine(line, searchPath)
		if m == nil {
			continue
		}
		matches = append(matches, *m)
		if len(matches) > maxResults {
			truncated = true
			cancel() // stop rg
			break
		}
	}
	// Drain and wait — ignore exit errors because context cancellation and
	// rg returning non-zero when no matches are found are both expected.
	_, _ = stdout.Read(make([]byte, 1)) // ensure pipe is drained enough for Wait
	_ = cmd.Wait()

	if truncated {
		matches = matches[:maxResults]
	}
	return matches, truncated, nil
}

// parseRGLine parses a single rg --no-heading --line-number output line of the
// form "path:lineNo:content" into a ContentLine.
func parseRGLine(line, basePath string) *toolresult.ContentLine {
	// rg may emit binary-match notices or other non-match lines; skip them.
	// A valid match line has at least two ':' separators.
	first := strings.IndexByte(line, ':')
	if first < 0 {
		return nil
	}
	rest := line[first+1:]
	second := strings.IndexByte(rest, ':')
	if second < 0 {
		return nil
	}
	filePart := line[:first]
	lineNoPart := rest[:second]
	content := rest[second+1:]

	lineNo := 0
	for _, c := range lineNoPart {
		if c < '0' || c > '9' {
			return nil
		}
		lineNo = lineNo*10 + int(c-'0')
	}
	if lineNo == 0 {
		return nil
	}

	// Resolve to absolute so callers can make it relative to cwd later.
	if !filepath.IsAbs(filePart) {
		filePart = filepath.Join(basePath, filePart)
	}

	return &toolresult.ContentLine{
		File:   filePart,
		LineNo: lineNo,
		Text:   content,
		Type:   toolresult.LineMatch,
	}
}

// execGoGrep is the pure-Go fallback used when rg is not available.
func execGoGrep(ctx context.Context, pattern, searchPath, include, exclude string, caseInsensitive, fixedStrings bool, maxResults int) ([]toolresult.ContentLine, bool, error) {
	// Build regexp from pattern.
	reStr := pattern
	if fixedStrings {
		reStr = regexp.QuoteMeta(pattern)
	}
	if caseInsensitive {
		reStr = "(?i)" + reStr
	}
	re, err := regexp.Compile(reStr)
	if err != nil {
		return nil, false, fmt.Errorf("invalid pattern: %w", err)
	}

	var matches []toolresult.ContentLine
	truncated := false

	walkErr := filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries silently
		}
		if d.IsDir() {
			if grepSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		name := d.Name()
		// Apply include/exclude filters on the filename.
		if include != "" {
			ok, _ := filepath.Match(include, name)
			if !ok {
				return nil
			}
		}
		if exclude != "" {
			skip, _ := filepath.Match(exclude, name)
			if skip {
				return nil
			}
		}

		// Read and scan the file line by line.
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer f.Close()

		// Skip binary files.
		hdr := make([]byte, 512)
		n, _ := f.Read(hdr)
		if n > 0 && bytes.IndexByte(hdr[:n], 0) >= 0 {
			return nil
		}
		if _, seekErr := f.Seek(0, 0); seekErr != nil {
			return nil
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			lineNo++
			text := scanner.Text()
			if re.MatchString(text) {
				matches = append(matches, toolresult.ContentLine{
					File:   path,
					LineNo: lineNo,
					Text:   text,
					Type:   toolresult.LineMatch,
				})
				if len(matches) > maxResults {
					truncated = true
					return errGrepCap
				}
			}
		}
		return nil
	})

	if walkErr != nil && walkErr != errGrepCap {
		return nil, false, walkErr
	}

	if truncated {
		matches = matches[:maxResults]
	}
	return matches, truncated, nil
}

// errGrepCap is a sentinel error used to abort the WalkDir early when the
// match cap is reached. It is never returned to callers.
var errGrepCap = fmt.Errorf("grep cap reached")

func init() {
	tool.Register(&GrepTool{})
}
