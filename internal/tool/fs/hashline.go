package fs

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strconv"
	"strings"
)

// Hashline constants. Lengths are chosen to be short (low token cost) while
// giving enough entropy to detect accidental reuse across edits.
const (
	// lineHashLen is the number of lowercase letters in a per-line hash.
	// 3 letters → 17 576 buckets (26³); digits excluded so hashes never look
	// like line numbers.
	lineHashLen = 3

	// fileHashLen is the number of uppercase hex digits in a whole-file tag.
	// 4 hex chars → 65 536 buckets; sufficient fingerprint at low token cost.
	fileHashLen = 4
)

// ComputeLineHash returns a 3-letter (a-z) hash for a single line.
// All whitespace is stripped before hashing so that indentation changes do
// not produce false mismatches on surrounding context lines.
// A trailing CR (from CRLF) is also removed before stripping.
func ComputeLineHash(line string) string {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	// Strip all whitespace characters.
	var b strings.Builder
	b.Grow(len(line))
	for _, r := range line {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			// skip
		default:
			b.WriteRune(r)
		}
	}
	stripped := b.String()

	h := fnv.New64a()
	_, _ = h.Write([]byte(stripped))
	n := h.Sum64() % uint64(26*26*26) // 17576

	// Convert to 3 letters (a-z), most-significant first.
	out := [lineHashLen]byte{}
	for i := lineHashLen - 1; i >= 0; i-- {
		out[i] = byte('a' + n%26)
		n /= 26
	}
	return string(out[:])
}

// NormalizeFileHashText trims trailing spaces and tabs on every line and
// normalises CRLF → LF, so that files that differ only in trailing whitespace
// or line-ending style produce the same file tag.
func NormalizeFileHashText(text string) string {
	if text == "" {
		return ""
	}
	// Normalise CRLF first so split on \n gives clean lines.
	text = strings.ReplaceAll(text, "\r\n", "\n")

	var b strings.Builder
	b.Grow(len(text))
	start := 0
	for i := 0; i <= len(text); i++ {
		if i == len(text) || text[i] == '\n' {
			line := text[start:i]
			// Strip trailing spaces and tabs.
			line = strings.TrimRight(line, " \t")
			b.WriteString(line)
			if i < len(text) {
				b.WriteByte('\n')
			}
			start = i + 1
		}
	}
	return b.String()
}

// ComputeFileHash returns a 4-digit uppercase hex fingerprint of the whole
// file text. The text is LF-normalised and trailing horizontal whitespace is
// stripped per line before hashing so that CRLF files and files with
// trailing-space differences do not produce different tags.
func ComputeFileHash(text string) string {
	normalized := NormalizeFileHashText(text)
	h := fnv.New64a()
	_, _ = h.Write([]byte(normalized))
	low16 := uint16(h.Sum64() & 0xffff) //nolint:gosec // intentional low-16 fingerprint
	return fmt.Sprintf("%04X", low16)
}

// FormatFileHeader formats the @file path#TAG header line shown at the top
// of hashline Read output and expected by HashEdit's file_tag field.
//
//	@file internal/foo.go#A1B2
func FormatFileHeader(relPath, tag string) string {
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	if relPath == "" {
		relPath = "."
	}
	return fmt.Sprintf("@file %s#%s", relPath, strings.ToUpper(strings.TrimSpace(tag)))
}

// FormatHashline formats a single line in hashline output.
//
//	10#abc|func foo() {
func FormatHashline(lineNo int, text string) string {
	return fmt.Sprintf("%d#%s|%s", lineNo, ComputeLineHash(text), text)
}

// ParseHashRef parses a hashline anchor of the form "LINE#hash", e.g. "10#abc".
// It returns the 1-based line number and the hash string.
func ParseHashRef(ref string) (lineNo int, hash string, err error) {
	ref = strings.TrimSpace(ref)
	idx := strings.IndexByte(ref, '#')
	if idx < 0 {
		return 0, "", fmt.Errorf("invalid hashline ref %q: missing '#'", ref)
	}
	lineStr := ref[:idx]
	hash = strings.ToLower(ref[idx+1:])
	if lineStr == "" {
		return 0, "", fmt.Errorf("invalid hashline ref %q: empty line number", ref)
	}
	if hash == "" {
		return 0, "", fmt.Errorf("invalid hashline ref %q: empty hash", ref)
	}
	n, convErr := strconv.Atoi(lineStr)
	if convErr != nil {
		return 0, "", fmt.Errorf("invalid hashline ref %q: line number is not an integer", ref)
	}
	if n < 1 {
		return 0, "", fmt.Errorf("invalid hashline ref %q: line number must be ≥ 1", ref)
	}
	return n, hash, nil
}

// NormalizeFileTag extracts the canonical 4-hex-char file tag from any of the
// forms the model may copy-paste:
//
//	"A1B2"                     → "A1B2"
//	"#A1B2"                    → "A1B2"
//	"@file src/app.go#A1B2"    → "A1B2"
//	"a1b2"                     → "A1B2"  (uppercased)
//
// Returns "" when the input is empty or does not contain a valid 4-hex tag.
func NormalizeFileTag(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Strip "@file path#" or just "#" prefix to get the tag portion.
	if idx := strings.LastIndexByte(s, '#'); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSpace(s)
	s = strings.ToUpper(s)
	if len(s) != fileHashLen {
		return ""
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			return ""
		}
	}
	return s
}
