package fs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// hashlineReadOutput builds the hashline-format block for a Read result:
// a @file path#TAG header followed by LINE#hash|content lines for the
// windowed range. The file TAG covers the complete file (fullContent),
// not just the window, so Edit anchors are always valid even on partial reads.
func hashlineReadOutput(fullContent, relPath string, lines []toolresult.ContentLine) string {
	tag := ComputeFileHash(fullContent)
	var b strings.Builder
	b.WriteString(FormatFileHeader(relPath, tag))
	b.WriteByte('\n')
	for _, l := range lines {
		b.WriteString(FormatHashline(l.LineNo, l.Text))
		b.WriteByte('\n')
	}
	return b.String()
}

const (
	maxReadLines  = 2000
	maxLineLength = 2000

	// maxReadBytes caps the total content one Read emits into the context.
	// The line and line-length caps alone still admit ~4MB (2000 lines ×
	// 2000 chars); a busy minified file would blow the context in one call.
	maxReadBytes = 256 * 1024

	// lineTruncationMarker ends every line that was cut at maxLineLength. It
	// is documented in the Read schema so the model knows the marker is not
	// part of the file — a truncated line cannot be edited by copying the
	// shortened text.
	lineTruncationMarker = "… [line truncated]"

	// largeFileThreshold is the line count above which an unconstrained Read
	// (no offset/limit) returns a head/tail summary instead of streaming all
	// lines into the context. Explicit offset/limit always bypasses this.
	largeFileThreshold = 500

	// summaryHead/Tail are the number of lines shown at each end of a
	// large-file summary. 10 lines each gives enough context to orient
	// without burning tokens on content the model has not asked for yet.
	summaryHead = 10
	summaryTail = 10
)

// buildSummary returns the hashline head/tail block for a large-file Read.
// allLines contains every line of the file (0-indexed). The @file header uses
// the whole-file TAG so Edit anchors on the shown lines are always valid.
//
// Output shape:
//
//	@file rel/path#TAG
//	[large file: N lines, X KB — first 10 and last 10 lines shown; use Grep to locate a symbol, then Read with offset+limit]
//	1#abc|first line
//	...
//	10#xyz|tenth line
//	--- M lines omitted ---
//	N-9#abc|...
//	...
//	N#xyz|last line
func buildSummary(allLines []string, relPath, fullContent string) string {
	total := len(allLines)
	tag := ComputeFileHash(fullContent)

	var b strings.Builder
	b.WriteString(FormatFileHeader(relPath, tag))
	b.WriteByte('\n')

	sizeBytes := int64(len(fullContent))
	b.WriteString(fmt.Sprintf("[large file: %d lines, %s — first %d and last %d lines shown; use Grep to locate a symbol, then Read with offset+limit]\n",
		total, toolresult.FormatSize(sizeBytes), summaryHead, summaryTail))

	head := summaryHead
	if head > total {
		head = total
	}
	for i := 0; i < head; i++ {
		b.WriteString(FormatHashline(i+1, allLines[i]))
		b.WriteByte('\n')
	}

	tail := summaryTail
	tailStart := total - tail
	if tailStart < head {
		tailStart = head // no gap needed; just continue from where head left off
	}

	omitted := tailStart - head
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("--- %d lines omitted ---\n", omitted))
		for i := tailStart; i < total; i++ {
			b.WriteString(FormatHashline(i+1, allLines[i]))
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// ReadTool reads file contents
type ReadTool struct{}

func (t *ReadTool) Name() string        { return "Read" }
func (t *ReadTool) Description() string { return "Read file contents" }
func (t *ReadTool) Icon() string        { return toolresult.IconRead }

func (t *ReadTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()

	filePath, err := tool.RequireString(params, "file_path")
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	// Resolve relative path
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwd, filePath)
	}

	// summary_only forces the head/tail summary regardless of file size.
	summaryOnly := tool.GetBool(params, "summary_only")

	// Track whether offset/limit were explicitly supplied by the caller.
	// Any explicit window means the caller already knows what they want, so
	// summary mode is skipped entirely.
	_, offsetGiven := params["offset"]
	_, limitGiven := params["limit"]
	offset := tool.GetInt(params, "offset", 0)
	limit := tool.GetInt(params, "limit", maxReadLines)

	// Get file info
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return toolresult.NewErrorResult(t.Name(), "file not found: "+filePath)
		}
		return toolresult.NewErrorResult(t.Name(), "failed to stat file: "+err.Error())
	}

	if info.IsDir() {
		return toolresult.NewErrorResult(t.Name(), "path is a directory: "+filePath)
	}

	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), "failed to open file: "+err.Error())
	}
	defer file.Close()

	// Check for binary file by reading first 512 bytes
	header := make([]byte, 512)
	n, _ := file.Read(header)
	if n > 0 {
		if bytes.IndexByte(header[:n], 0) >= 0 {
			recordFileRead(filePath, info)
			binaryNote := "Binary file detected: " + filePath
			if isImagePath(filePath) {
				// Tool results are text-only for now, so be honest about the
				// gap instead of a bare "binary" that reads like failure.
				binaryNote = fmt.Sprintf("image file: %s (%s). Read cannot display images yet — ask the user to attach the image to a message instead", filePath, toolresult.FormatSize(info.Size()))
			}
			return toolresult.ToolResult{
				Success: true,
				Output:  binaryNote,
				Metadata: toolresult.ResultMetadata{
					Title:    t.Name(),
					Icon:     t.Icon(),
					Subtitle: filePath + " (binary)",
					Size:     info.Size(),
				},
			}
		}
	}
	// Reset file position to beginning
	if _, err := file.Seek(0, 0); err != nil {
		return toolresult.NewErrorResult(t.Name(), "failed to seek file: "+err.Error())
	}

	// Read all lines into memory with a large scanner buffer.
	// We need all lines to (a) check whether to trigger summary mode and
	// (b) supply the tail window if we do.
	var allLines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return toolresult.NewErrorResult(t.Name(), "error reading file: "+err.Error())
	}

	// Detect trailing newline: the scanner consumes it without recording it,
	// so a file that ends in \n and a file that does not produce identical
	// allLines. Seek to the last byte and check. The file handle is at EOF
	// after the scanner drained it, so Seek is the only way to look back.
	trailingNewline := false
	if info.Size() > 0 {
		lastByte := make([]byte, 1)
		if _, seekErr := file.Seek(info.Size()-1, 0); seekErr == nil {
			if nb, _ := file.Read(lastByte); nb == 1 {
				trailingNewline = lastByte[0] == '\n'
			}
		}
	}

	recordFileRead(filePath, info)
	duration := time.Since(start)

	// Compute relPath and normalised full content once — used by both the
	// summary path and the normal hashline path.
	// fullContent is built from allLines (already in memory) to avoid a
	// second os.ReadFile call. scanner.Text() already strips \r from \r\n
	// lines, so joining with \n gives the same normalised result. Strip BOM
	// from the first line if present (the scanner does not do this). Restore
	// the trailing newline that the scanner consumed so ComputeFileHash
	// produces the same tag that edit.go sees when it reads the file raw.
	relPath := filePath
	if cwd != "" {
		if rel, relErr := filepath.Rel(cwd, filePath); relErr == nil {
			relPath = rel
		}
	}
	if len(allLines) > 0 {
		allLines[0] = strings.TrimPrefix(allLines[0], "\ufeff")
	}
	fullContent := strings.Join(allLines, "\n")
	if trailingNewline {
		fullContent += "\n"
	}

	// --- Summary mode ---
	// Triggered when:
	//   (a) summary_only=true, OR
	//   (b) file exceeds largeFileThreshold AND no offset/limit was given.
	wantSummary := summaryOnly || (!offsetGiven && !limitGiven && len(allLines) > largeFileThreshold)
	if wantSummary {
		output := buildSummary(allLines, relPath, fullContent)
		return toolresult.ToolResult{
			Success: true,
			Output:  output,
			Metadata: toolresult.ResultMetadata{
				Title:     t.Name(),
				Icon:      t.Icon(),
				Subtitle:  filePath,
				Size:      info.Size(),
				LineCount: len(allLines),
				Duration:  duration,
				Truncated: true, // summary is always a partial view
			},
		}
	}

	// --- Normal windowed read ---
	var lines []toolresult.ContentLine
	lineNo := 0
	readCount := 0
	emittedBytes := 0
	truncated := false

	for _, rawText := range allLines {
		lineNo++

		// Skip lines before offset
		if offset > 0 && lineNo < offset {
			continue
		}

		// Check limit
		if readCount >= limit || emittedBytes >= maxReadBytes {
			truncated = true
			break
		}

		text := rawText
		// Truncate long lines (rune-aware to avoid splitting multi-byte characters)
		if utf8.RuneCountInString(text) > maxLineLength {
			runes := []rune(text)
			text = string(runes[:maxLineLength]) + lineTruncationMarker
		}

		lines = append(lines, toolresult.ContentLine{
			LineNo: lineNo,
			Text:   text,
			Type:   toolresult.LineNormal,
		})
		readCount++
		emittedBytes += len(text) + 8 // content plus the line-number prefix
	}

	// resultNote conveys states the line dump alone can't convey: an empty
	// result would render as nothing, and a truncated read must say where to
	// continue.
	resultNote := ""
	switch {
	case len(lines) == 0 && lineNo > 0:
		resultNote = fmt.Sprintf("no lines at offset %d: %s has %d lines", offset, filePath, lineNo)
	case len(lines) == 0:
		resultNote = "file exists but is empty: " + filePath
	case truncated:
		lastLine := lines[len(lines)-1].LineNo
		resultNote = fmt.Sprintf("(output truncated at line %d; continue with offset=%d)", lastLine, lastLine+1)
	}

	// Build content string for hook response
	var hookBuf strings.Builder
	for _, l := range lines {
		hookBuf.WriteString(l.Text)
		hookBuf.WriteByte('\n')
	}
	contentForHook := hookBuf.String()

	startLine := 1
	if offset > 0 {
		startLine = offset
	}

	// Always use hashline output format: @file path#TAG header + LINE#hash|content.
	var hlOutput string
	if len(lines) > 0 {
		hlOutput = hashlineReadOutput(fullContent, relPath, lines)
		if truncated {
			lastLine := lines[len(lines)-1].LineNo
			hlOutput += fmt.Sprintf("(output truncated at line %d; continue with offset=%d)\n", lastLine, lastLine+1)
		}
	}

	// resultNote remains as-is for empty/out-of-range cases.
	if hlOutput == "" {
		hlOutput = resultNote
	}

	return toolresult.ToolResult{
		Success: true,
		Output:  hlOutput,
		Lines:   lines, // kept for the TUI renderer (renderLines)
		HookResponse: map[string]any{
			"type": "text",
			"file": map[string]any{
				"filePath":   filePath,
				"content":    contentForHook,
				"numLines":   len(lines),
				"startLine":  startLine,
				"totalLines": lineNo,
			},
		},
		Metadata: toolresult.ResultMetadata{
			Title:     t.Name(),
			Icon:      t.Icon(),
			Subtitle:  filePath,
			Size:      info.Size(),
			LineCount: len(lines),
			Duration:  duration,
			Truncated: truncated,
		},
	}
}

// isImagePath reports whether the file is an image by extension, matching
// the formats the composer accepts as attachments (internal/image).
func isImagePath(filePath string) bool {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func init() {
	tool.Register(&ReadTool{})
}
