package memory

import (
	"fmt"
	"strings"
)

// FormatBullets renders recalled memories as the compact bullet list both
// auto-recall (app layer) and the recall tool put in front of the model:
//
//   - <text> [<type>] (YYYY-MM-DD)
//
// type/date suffixes appear only when present. limit > 0 caps the bullets.
// An empty result set renders as the canonical no-hits line, so callers can
// inject the output unconditionally.
func FormatBullets(results []Memory, limit int) string {
	if len(results) == 0 {
		return "No relevant memories found."
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	var sb strings.Builder
	for _, m := range results {
		sb.WriteString("- ")
		sb.WriteString(m.Text)
		if m.Type != "" {
			fmt.Fprintf(&sb, " [%s]", m.Type)
		}
		if date := formatDate(m.MentionedAt); date != "" {
			fmt.Fprintf(&sb, " (%s)", date)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatDate turns a Hindsight mentioned_at timestamp into YYYY-MM-DD;
// empty when absent or too short to parse.
func formatDate(mentionedAt string) string {
	if len(mentionedAt) >= 10 {
		return mentionedAt[:10]
	}
	return ""
}

// TruncateRunes caps s to n runes with an ellipsis (no cap when n <= 0 or
// the text already fits). Used to bound recall queries and retain digests.
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
