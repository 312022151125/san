package fs

import (
	"fmt"
	"sort"
	"strings"
)

// HashlineEdit is the wire shape for one element in the "edits" array of a
// HashEdit call. From and To are "LINE#hash" anchor strings; Content replaces
// the lines from From to To (inclusive). A nil Content means delete the range.
//
// Single-line replace: From == To (same line).
// Range replace:       From < To.
// Insert before line N: set From=To="N#hash" and Content = newLine + "\n" + existing line N.
// Delete:              set Content to nil (omit the field in JSON).
type HashlineEdit struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Content *string `json:"content,omitempty"`
}

// HashlineMismatch records a single line whose hash did not match.
type HashlineMismatch struct {
	Line     int
	Expected string
	Actual   string
}

// HashlineMismatchError is returned when one or more line hashes don't match.
type HashlineMismatchError struct {
	Mismatches []HashlineMismatch
}

func (e *HashlineMismatchError) Error() string {
	var b strings.Builder
	b.WriteString("line hash mismatch (file changed) — re-read with hashline:true before editing:")
	for _, m := range e.Mismatches {
		fmt.Fprintf(&b, "\n  line %d: want %s got %s", m.Line, m.Expected, m.Actual)
	}
	return b.String()
}

// ErrFileTagMismatch is the sentinel value embedded in file-tag stale errors.
// Callers can use errors.As to distinguish it from line-hash errors.
type ErrFileTagMismatch struct {
	Expected string
	Actual   string
	Path     string
}

func (e *ErrFileTagMismatch) Error() string {
	return fmt.Sprintf(
		"file tag stale: want %s got %s — re-read %s with hashline:true before editing",
		e.Expected, e.Actual, e.Path,
	)
}

// parsedEdit is one validated range-replace ready for application.
type parsedEdit struct {
	start, end         int    // 1-based line numbers, inclusive
	startHash, endHash string // expected hashes
	lines              []string
	deleteRange        bool // true when Content is nil (delete)
}

// ApplyHashlineEdits verifies fileTag against fileContent, validates every
// anchor, and applies edits returning the new content string. fileContent must
// already be LF-normalised by the caller; CRLF restoration is handled above
// this layer.
//
// Errors:
//   - *ErrFileTagMismatch     – whole-file tag does not match
//   - *HashlineMismatchError  – one or more line anchors are stale
//   - plain error             – overlapping edits, parse failures, etc.
func ApplyHashlineEdits(fileContent, fileTag string, edits []HashlineEdit) (string, error) {
	// 1. Verify whole-file tag.
	actualTag := ComputeFileHash(fileContent)
	expectedTag := NormalizeFileTag(fileTag)
	if expectedTag == "" {
		return "", fmt.Errorf(
			"file_tag is required: supply the 4 hex chars from the @file path#TAG header (e.g. %s)",
			actualTag,
		)
	}
	if expectedTag != actualTag {
		return "", &ErrFileTagMismatch{Expected: expectedTag, Actual: actualTag}
	}

	if len(edits) == 0 {
		return fileContent, nil
	}

	// 2. Split into lines (keep trailing empty element for files ending in \n).
	lines := strings.Split(fileContent, "\n")
	// strings.Split("a\nb\n", "\n") → ["a","b",""] — the trailing "" represents
	// the newline terminator and must be preserved to avoid adding a spurious
	// newline or losing one.

	// 3. Parse and validate each edit.
	parsed := make([]parsedEdit, 0, len(edits))
	var mismatches []HashlineMismatch

	for i, e := range edits {
		fromLine, fromHash, err := ParseHashRef(e.From)
		if err != nil {
			return "", fmt.Errorf("edit[%d].from: %w", i, err)
		}
		toLine, toHash, err := ParseHashRef(e.To)
		if err != nil {
			return "", fmt.Errorf("edit[%d].to: %w", i, err)
		}
		if fromLine > toLine {
			return "", fmt.Errorf("edit[%d]: from line %d > to line %d", i, fromLine, toLine)
		}

		// Validate from hash.
		if fromLine < 1 || fromLine > len(lines) {
			mismatches = append(mismatches, HashlineMismatch{Line: fromLine, Expected: fromHash, Actual: ""})
		} else {
			actual := ComputeLineHash(lines[fromLine-1])
			if actual != fromHash {
				mismatches = append(mismatches, HashlineMismatch{Line: fromLine, Expected: fromHash, Actual: actual})
			}
		}

		// Validate to hash (only if different from from).
		if toLine != fromLine {
			if toLine < 1 || toLine > len(lines) {
				mismatches = append(mismatches, HashlineMismatch{Line: toLine, Expected: toHash, Actual: ""})
			} else {
				actual := ComputeLineHash(lines[toLine-1])
				if actual != toHash {
					mismatches = append(mismatches, HashlineMismatch{Line: toLine, Expected: toHash, Actual: actual})
				}
			}
		}

		// Determine replacement lines.
		var dst []string
		deleteRange := e.Content == nil
		if !deleteRange {
			dst = splitContentLines(*e.Content)
		}

		parsed = append(parsed, parsedEdit{
			start:       fromLine,
			end:         toLine,
			startHash:   fromHash,
			endHash:     toHash,
			lines:       dst,
			deleteRange: deleteRange,
		})
	}

	if len(mismatches) > 0 {
		return "", &HashlineMismatchError{Mismatches: mismatches}
	}

	// 4. Sort edits by start line ascending, then check for overlaps.
	sort.Slice(parsed, func(i, j int) bool {
		return parsed[i].start < parsed[j].start
	})
	for i := 1; i < len(parsed); i++ {
		if parsed[i].start <= parsed[i-1].end {
			return "", fmt.Errorf(
				"overlapping edits: edit ending at line %d overlaps edit starting at line %d",
				parsed[i-1].end, parsed[i].start,
			)
		}
	}

	// 5. Apply bottom-up (reverse order) to avoid line-index drift.
	result := make([]string, len(lines))
	copy(result, lines)

	for i := len(parsed) - 1; i >= 0; i-- {
		p := parsed[i]
		// Convert 1-based inclusive range to 0-based slice indices.
		lo := p.start - 1 // inclusive
		hi := p.end       // exclusive (p.end is the last line included, 1-based)

		var replacement []string
		if !p.deleteRange {
			replacement = p.lines
		}
		// Splice: result = result[:lo] + replacement + result[hi:]
		result = append(result[:lo], append(replacement, result[hi:]...)...)
	}

	return strings.Join(result, "\n"), nil
}

// splitContentLines splits content on \n (after CRLF normalisation).
// A trailing newline produces a trailing "" entry which is intentional —
// it represents the newline at end of the last replacement line.
func splitContentLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	return strings.Split(content, "\n")
}
