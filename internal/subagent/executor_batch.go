package subagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool"
)

// batchResultThreshold is the maximum number of bytes of agent content that is
// inlined into the parent's tool result. Results larger than this threshold are
// stored to a file under outputDir and replaced with a ResultRef reference.
// The goal is to prevent a single verbose child from flooding the parent context.
const batchResultThreshold = 8 * 1024 // 8 KB

// RunBatch executes a set of independent agent tasks concurrently.
//
// The batch runs the full preflight before any goroutine starts: if any item
// fails validation (unknown agent, invalid mode, Plan Mode write-agent check)
// the entire batch is rejected atomically and no child is spawned.
//
// Each child receives:
//
//	agent system prompt + shared batch context + individual task
//
// The parent conversation history is never copied to children.
// Results are returned as compact AgentExecResult values; large results are
// stored to outputDir and replaced with a ResultRef.
func (e *Executor) RunBatch(ctx context.Context, req tool.AgentBatchRequest, outputDir string, planModeChecker tool.PlanModeChecker) (*tool.AgentBatchResult, error) {
	if len(req.Tasks) == 0 {
		return nil, fmt.Errorf("batch requires at least one task")
	}

	// --- Preflight: validate all items before starting any goroutine ---
	type resolvedItem struct {
		item   tool.AgentBatchItem
		config *AgentConfig
		mode   PermissionMode
	}

	resolved := make([]resolvedItem, 0, len(req.Tasks))
	seenIDs := make(map[string]bool, len(req.Tasks))
	seenNames := make(map[string]bool, len(req.Tasks))

	inPlanMode := planModeChecker != nil && planModeChecker.IsPlanMode()

	for i, item := range req.Tasks {
		if item.Task == "" {
			return nil, fmt.Errorf("batch item %d: task prompt cannot be empty", i)
		}

		// Enforce unique IDs within the batch.
		if item.ID != "" {
			if seenIDs[item.ID] {
				return nil, fmt.Errorf("batch item %d: duplicate id %q", i, item.ID)
			}
			seenIDs[item.ID] = true
		}
		// Enforce unique display names within the batch (non-empty only).
		if item.Name != "" {
			if seenNames[item.Name] {
				return nil, fmt.Errorf("batch item %d: duplicate name %q", i, item.Name)
			}
			seenNames[item.Name] = true
		}

		// Resolve agent config — same logic as single-agent path.
		config, ok := e.resolveAgentConfig(item.Agent)
		if !ok {
			return nil, fmt.Errorf("batch item %d (%q): unknown or disabled agent: %q", i, item.Name, item.Agent)
		}

		// Resolve effective permission mode for this item.
		execReq := tool.AgentExecRequest{
			Agent: item.Agent,
			Mode:  item.Mode,
		}
		mode := e.requestPermissionMode(config, execReq)

		// Plan Mode enforcement (D2): only explore-mode agents are allowed.
		if inPlanMode && isWriteCapableMode(mode) {
			return nil, fmt.Errorf(
				"batch contains write-capable agents; only explore mode is allowed in Plan Mode "+
					"(item %d %q has mode %q)", i, item.Name, mode,
			)
		}

		resolved = append(resolved, resolvedItem{item: item, config: config, mode: mode})
	}

	// --- Pre-allocate IDs for all items so they are deterministic ---
	for i := range resolved {
		if resolved[i].item.ID == "" {
			resolved[i].item.ID = generateShortID()
		}
	}

	// P2-A: Register all batch items in the task manager before any goroutine
	// starts, with status=queued. This makes them visible in task listings and
	// gives them deterministic IDs for log references before execution begins.
	mgr := task.Default()
	agentTasks := make([]*task.AgentTask, len(resolved))
	for i, ri := range resolved {
		desc := ri.item.Name
		if desc == "" {
			desc = ri.item.Task
		}
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		at := mgr.CreateQueuedAgentTask(ri.item.ID, ri.config.Name, desc)
		agentTasks[i] = at
	}

	// --- Fan out concurrently ---
	start := time.Now()

	results := make([]tool.AgentExecResult, len(resolved))
	var wg sync.WaitGroup
	wg.Add(len(resolved))

	for i, ri := range resolved {
		go func(idx int, ri resolvedItem, at *task.AgentTask) {
			defer wg.Done()

			// Build the child prompt: shared context first, then the task delta.
			childPrompt := buildBatchChildPrompt(req.Context, ri.item.Task)

			execReq := tool.AgentExecRequest{
				Agent:               ri.item.Agent,
				ResolvedAgentConfig: ri.config,
				Prompt:              childPrompt,
				Description:         ri.item.Name,
				Mode:                ri.item.Mode,
				TaskID:              "",  // foreground batch run
				Depth:               1,   // P2-B: batch children are always depth 1
			}

			// Acquire semaphores (respects concurrency and writer limits).
			needsWrite := isWriteCapableMode(ri.mode)
			if err := e.acquireSemaphores(ctx, needsWrite); err != nil {
				if at != nil {
					at.Complete(fmt.Errorf("semaphore acquire failed: %v", err))
				}
				results[idx] = tool.AgentExecResult{
					AgentID:   ri.item.ID,
					AgentName: ri.item.Name,
					Success:   false,
					Error:     fmt.Sprintf("semaphore acquire failed: %v", err),
				}
				return
			}
			defer e.releaseSemaphores(needsWrite)

			// P2-A: transition task to running now that a slot was acquired.
			if at != nil {
				at.MarkRunning()
			}

			// P2-E: record each batch child execution in the parent's metrics.
			if m := core.MetricsFromContext(ctx); m != nil {
				m.RecordSubagentCall()
			}

			result, err := e.Run(ctx, execReq)
			if err != nil {
				if at != nil {
					at.Complete(err)
				}
				results[idx] = tool.AgentExecResult{
					AgentID:   ri.item.ID,
					AgentName: ri.item.Name,
					Success:   false,
					Error:     err.Error(),
				}
				return
			}

			execResult := tool.AgentExecResult{
				AgentID:           result.AgentID,
				AgentName:         result.AgentName,
				OutputFile:        result.TranscriptPath,
				Model:             result.Model,
				Success:           result.Success,
				Content:           result.Content,
				StepCount:         result.StepCount,
				ToolUses:          result.ToolUses,
				TotalInputTokens:  result.TokenUsage.Input,
				TotalOutputTokens: result.TokenUsage.Output,
				Duration:          result.Duration,
				// Do NOT include Activity — the full tool trace is not useful
				// in parent context; it would inflate input tokens with noise.
				Error: result.Error,
			}
			// Override AgentID with the pre-allocated deterministic ID so the
			// parent can reference it even before the run started.
			if execResult.AgentID == "" {
				execResult.AgentID = ri.item.ID
			}

			// P1-A: Build a compact AgentYield.Summary from the result content.
			// This gives the parent a structured token-efficient view of what the
			// child found/did, without inlining the full transcript.
			execResult.Yield = extractYield(result.Content)

			// Store large results to disk (D4) to avoid flooding parent context.
			if len(result.Content) > batchResultThreshold && outputDir != "" {
				ref, err := storeBatchArtifact(outputDir, execResult.AgentID, result.Content)
				if err == nil {
					execResult.ResultRef = ref
					execResult.Content = result.Content[:256] + fmt.Sprintf(
						"\n\n(result truncated; full content at %s)", ref,
					)
				}
			}

			if at != nil {
				var completeErr error
				if !result.Success {
					completeErr = fmt.Errorf("%s", result.Error)
				}
				at.Complete(completeErr)
			}

			results[idx] = execResult
		}(i, ri, agentTasks[i])
	}

	wg.Wait()

	return &tool.AgentBatchResult{
		Results:  results,
		Duration: time.Since(start),
	}, nil
}

// isWriteCapableMode reports whether a permission mode requires the writer
// semaphore slot (i.e. the agent can modify files).
func isWriteCapableMode(mode PermissionMode) bool {
	switch NormalizePermissionMode(string(mode)) {
	case PermissionExplore:
		return false
	default:
		return true
	}
}

// buildBatchChildPrompt concatenates the shared context and the task-specific
// delta. The shared context comes first so it is in the stable prefix position
// for prompt caching.
func buildBatchChildPrompt(sharedContext, taskDelta string) string {
	if sharedContext == "" {
		return taskDelta
	}
	if taskDelta == "" {
		return sharedContext
	}
	return sharedContext + "\n\n---\n\n" + taskDelta
}

// storeBatchArtifact writes content to a file under outputDir and returns the
// reference string. The reference format is "agent://<id>" — callers must not
// parse or depend on the filesystem path.
func storeBatchArtifact(outputDir, agentID, content string) (string, error) {
	path := filepath.Join(outputDir, agentID+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return "agent://" + agentID, nil
}

// extractYield builds a compact AgentYield from agent result content.
//
// P1-A: The goal is to populate AgentYield.Summary with a concise excerpt from
// the child's final message so the parent has a structured token-efficient
// signal without inlining the full transcript.
//
// Strategy:
//   - If the content starts with a heading line (## Summary / # Summary),
//     extract the paragraph immediately following it.
//   - Otherwise, use the first non-empty paragraph (up to summaryMaxChars).
//   - If no paragraph is found, use the first summaryMaxChars characters.
//
// The full content is still available via ResultRef when it was stored to disk,
// or via Content when it was small enough to inline.
const summaryMaxChars = 500

func extractYield(content string) *tool.AgentYield {
	if content == "" {
		return &tool.AgentYield{}
	}

	// Normalise line endings.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	// Try to find a "## Summary" or "# Summary" heading and take the next
	// paragraph. This captures agents that follow the structured yield format.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(strings.TrimLeft(trimmed, "#"))
		lower = strings.TrimSpace(lower)
		if (strings.HasPrefix(trimmed, "#")) && lower == "summary" {
			// Collect non-empty lines after the heading until a blank separator.
			var para []string
			for j := i + 1; j < len(lines); j++ {
				l := strings.TrimSpace(lines[j])
				if l == "" && len(para) > 0 {
					break
				}
				if strings.HasPrefix(l, "#") && len(para) > 0 {
					break // next heading — stop
				}
				if l != "" {
					para = append(para, l)
				}
			}
			if len(para) > 0 {
				summary := strings.Join(para, " ")
				if len(summary) > summaryMaxChars {
					summary = summary[:summaryMaxChars] + "…"
				}
				return &tool.AgentYield{Summary: summary}
			}
		}
	}

	// Fall back: first non-empty paragraph.
	var para []string
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" && len(para) > 0 {
			break
		}
		if l != "" && !strings.HasPrefix(l, "#") {
			para = append(para, l)
		}
	}
	if len(para) > 0 {
		summary := strings.Join(para, " ")
		if len(summary) > summaryMaxChars {
			summary = summary[:summaryMaxChars] + "…"
		}
		return &tool.AgentYield{Summary: summary}
	}

	// Last resort: first summaryMaxChars characters of the raw content.
	summary := strings.TrimSpace(content)
	if len(summary) > summaryMaxChars {
		summary = summary[:summaryMaxChars] + "…"
	}
	return &tool.AgentYield{Summary: summary}
}
