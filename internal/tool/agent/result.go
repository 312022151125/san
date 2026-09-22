package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// maxResultActivityLines caps the tool activity echoed into the parent's
// context. The full trail stays visible in the TUI activity stream; the parent
// LLM only needs enough of the tail to sanity-check what the agent did.
const maxResultActivityLines = 30

// formatBatchResult renders a compact combined result for an Agent batch call.
// It never includes individual activity traces — those would inflate parent
// context with noise. Each agent gets a compact block:
//
//	Agent: <name>  Model: <model>  Steps: N  Tokens: in=X out=Y  Duration: T
//	<content or ResultRef>
func formatBatchResult(batch *tool.AgentBatchResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Batch completed: %d agents in %s\n\n", len(batch.Results), toolresult.FormatDuration(batch.Duration))

	for i, r := range batch.Results {
		name := r.AgentName
		if name == "" {
			name = r.AgentID
		}
		if name == "" {
			name = fmt.Sprintf("agent-%d", i+1)
		}

		status := "done"
		if !r.Success {
			status = "failed"
		}

		fmt.Fprintf(&sb, "--- %s (%s) ---\n", name, status)
		fmt.Fprintf(&sb, "Model: %s  Steps: %d  Tokens: in=%d out=%d  Duration: %s\n",
			r.Model, r.StepCount,
			r.TotalInputTokens, r.TotalOutputTokens,
			toolresult.FormatDuration(r.Duration),
		)

		if r.ResultRef != "" {
			// Large result stored to file — inline only the first snippet.
			fmt.Fprintf(&sb, "Result: %s\n", r.ResultRef)
			if r.Content != "" {
				sb.WriteString(r.Content)
				sb.WriteString("\n")
			}
		} else if r.Content != "" {
			sb.WriteString(r.Content)
			sb.WriteString("\n")
		}

		if !r.Success && r.Error != "" {
			fmt.Fprintf(&sb, "Error: %s\n", r.Error)
		}

		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatForegroundAgentResult renders a finished subagent's result for the
// parent's tool result: a short header, a capped tail of the tool trace, then
// the subagent's final message.
func formatForegroundAgentResult(agentName string, result *tool.AgentExecResult, duration time.Duration) string {
	displayName := result.AgentName
	if displayName == "" {
		displayName = agentName
	}
	agentDuration := result.Duration
	if agentDuration == 0 {
		agentDuration = duration
	}

	var outputBuilder strings.Builder
	fmt.Fprintf(&outputBuilder, "Agent: %s\nModel: %s\nSteps: %d\nToolUses: %d\nTokens: in=%d out=%d\nDuration: %s\n",
		displayName, result.Model, result.StepCount, result.ToolUses, result.TotalInputTokens, result.TotalOutputTokens, toolresult.FormatDuration(agentDuration))
	if result.AgentID != "" {
		fmt.Fprintf(&outputBuilder, "AgentID: %s\n", result.AgentID)
	}
	outputBuilder.WriteString("\n")

	activity := result.Activity
	if len(activity) > maxResultActivityLines {
		fmt.Fprintf(&outputBuilder, "(%d earlier tool calls omitted)\n", len(activity)-maxResultActivityLines)
		activity = activity[len(activity)-maxResultActivityLines:]
	}
	for _, line := range activity {
		outputBuilder.WriteString(line)
		outputBuilder.WriteString("\n")
	}
	if result.Content != "" {
		outputBuilder.WriteString(result.Content)
	}
	return outputBuilder.String()
}
