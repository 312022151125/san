// Package finalizetool provides the Finalize tool — a model-callable surface
// over the Smart Parallel Finalization pipeline.
//
// The tool accepts a task description and optional acceptance criteria, then
// delegates to finalize.Pipeline.Run which:
//
//   - detects changed files (git diff)
//   - classifies the verification tier (FAST / STANDARD / FULL)
//   - builds and executes a parallel verification DAG via the Batch engine
//   - runs the reviewer concurrently
//   - aggregates into a compact result
//
// Trust boundary: Finalize constructs all verification commands itself from
// known strategies. Model-supplied strings are limited to task description
// and acceptance criteria (injected into the reviewer prompt only).
// No model-supplied shell strings execute through this tool.
package finalizetool

import (
	"context"
	"fmt"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/finalize"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/batch"
	"github.com/genai-io/san/internal/tool/toolresult"
)

const toolName = "Finalize"

// FinalizeTool is the model-facing tool surface over finalize.Pipeline.
// It is registered via init() and wired to a real pipeline by the composition
// layer (cmd/san) via SetPipeline / SetDependencies.
type FinalizeTool struct {
	pipeline        *finalize.Pipeline
	planModeChecker tool.PlanModeChecker
}

// NewFinalizeTool creates a FinalizeTool with no pipeline wired yet.
// Call SetDependencies before the tool is used.
func NewFinalizeTool() *FinalizeTool {
	return &FinalizeTool{}
}

// SetPlanModeChecker wires the Plan Mode gate. Finalize is rejected entirely
// in Plan Mode (same policy as BatchTool — it runs real commands).
func (t *FinalizeTool) SetPlanModeChecker(c tool.PlanModeChecker) {
	t.planModeChecker = c
}

// SetDependencies wires the execution engines at startup.
// batchEngine must not be nil. subagentExec may be nil.
func (t *FinalizeTool) SetDependencies(
	batchEngine batch.Engine,
	subagentExec finalize.SubagentExecutor,
	outputDir string,
) {
	t.pipeline = finalize.NewPipeline(batchEngine, subagentExec, outputDir)
}

// SetPipeline directly sets a pre-built pipeline (useful for tests).
func (t *FinalizeTool) SetPipeline(p *finalize.Pipeline) {
	t.pipeline = p
}

// Name implements tool.Tool.
func (t *FinalizeTool) Name() string { return toolName }

// Description implements tool.Tool.
func (t *FinalizeTool) Description() string {
	return "Run parallel verification (test, vet, build, review) after implementation is complete"
}

// Icon implements tool.Tool.
func (t *FinalizeTool) Icon() string { return "✓" }

// Schema implements tool.Tool.
func (t *FinalizeTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: toolName,
		Description: `Finalize implementation: run parallel affected-scope verification in one call.

Use Finalize when implementation is complete instead of running go test, go vet, go build, and reviewer sequentially. Finalize:
- Detects changed files and selects the cheapest verification tier
- Runs test/vet/build concurrently via the Batch engine
- Runs the reviewer concurrently (when needed)
- Returns one aggregated PASS or FAIL result

On PASS: implementation is verified — finish the task.
On FAIL: treat the output as actionable implementation work.

Do not run go test / go vet / go build individually before calling Finalize.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "Brief description of the implementation task (injected into reviewer context). Optional.",
				},
				"acceptance_criteria": map[string]any{
					"type":        "string",
					"description": "Acceptance criteria or requirements to verify (injected into reviewer context). Optional.",
				},
				"tier": map[string]any{
					"type":        "string",
					"description": "Override verification tier: 'fast', 'standard', or 'full'. Default: smart (auto-detected from changed files).",
					"enum":        []string{"fast", "standard", "full"},
				},
			},
		},
	}
}

// Execute implements tool.Tool. It rejects in Plan Mode and delegates to the
// pipeline.
func (t *FinalizeTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	// Plan Mode rejection — same policy as BatchTool.
	if t.planModeChecker != nil && t.planModeChecker.IsPlanMode() {
		return toolresult.NewErrorResult(toolName, "Finalize is not available in Plan Mode")
	}

	if t.pipeline == nil {
		return toolresult.NewErrorResult(toolName, "Finalize pipeline not configured")
	}

	taskDesc, _ := params["task"].(string)
	criteria, _ := params["acceptance_criteria"].(string)
	tierStr, _ := params["tier"].(string)

	req := finalize.Request{
		CWD:                cwd,
		Task:               taskDesc,
		AcceptanceCriteria: criteria,
		ReviewerMode:       "smart",
	}

	// Apply explicit tier override.
	if tierStr != "" {
		switch tierStr {
		case "fast":
			req.Tier = finalize.TierFast
			req.TierOverride = true
		case "standard":
			req.Tier = finalize.TierStandard
			req.TierOverride = true
		case "full":
			req.Tier = finalize.TierFull
			req.TierOverride = true
		}
	}

	result, err := t.pipeline.Run(ctx, req)
	if err != nil {
		return toolresult.NewErrorResult(toolName, fmt.Sprintf("finalization error: %v", err))
	}

	output := result.String()
	subtitle := buildSubtitle(result)

	return toolresult.ToolResult{
		Success: true,
		Output:  output,
		Metadata: toolresult.ResultMetadata{
			Title:    toolName,
			Icon:     t.Icon(),
			Subtitle: subtitle,
			Duration: result.Duration,
		},
	}
}

// buildSubtitle returns a compact status line for the TUI tool block.
func buildSubtitle(r finalize.Result) string {
	if r.Pass {
		return fmt.Sprintf("PASS · %s · tier=%s", toolresult.FormatDuration(r.Duration), r.Tier.String())
	}
	return fmt.Sprintf("FAILED (%d) · %s · tier=%s", r.FailureCount, toolresult.FormatDuration(r.Duration), r.Tier.String())
}

func init() {
	tool.Register(NewFinalizeTool())
}
