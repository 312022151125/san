package batch

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/tool/toolresult"
)

// formatBatchResult renders the compact combined result for the model.
//
// Success:
//
//	<id>
//	  exit: 0
//	  duration: 1.2s
//
// Failure:
//
//	<id>
//	  exit: 1
//	  duration: 3.4s
//	  errors:
//	    <relevant lines>
//
// Skipped:
//
//	<id>
//	  skipped: <reason>
//
// Large output:
//
//	<id>
//	  exit: 1
//	  duration: 3.4s
//	  log: log:/path/to/output.log
//	  errors:
//	    <relevant extract>
func formatBatchResult(r *BatchResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Batch: %d commands in %s\n\n", len(r.Commands), toolresult.FormatDuration(r.Duration))

	for _, c := range r.Commands {
		sb.WriteString(c.ID)
		sb.WriteString("\n")

		if c.Skipped {
			fmt.Fprintf(&sb, "  skipped: %s\n", c.SkipReason)
			sb.WriteString("\n")
			continue
		}

		if c.Err != "" {
			fmt.Fprintf(&sb, "  error: %s\n", c.Err)
			sb.WriteString("\n")
			continue
		}

		fmt.Fprintf(&sb, "  exit: %d\n", c.ExitCode)
		fmt.Fprintf(&sb, "  duration: %s\n", toolresult.FormatDuration(c.Duration))

		if c.LogRef != "" {
			fmt.Fprintf(&sb, "  log: %s\n", c.LogRef)
		}

		if c.ExitCode != 0 && c.Output != "" {
			sb.WriteString("  errors:\n")
			for _, line := range strings.Split(c.Output, "\n") {
				if line != "" {
					fmt.Fprintf(&sb, "    %s\n", line)
				}
			}
		}

		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}
