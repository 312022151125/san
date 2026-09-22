package subagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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

	// --- Fan out concurrently ---
	start := time.Now()

	results := make([]tool.AgentExecResult, len(resolved))
	var wg sync.WaitGroup
	wg.Add(len(resolved))

	for i, ri := range resolved {
		go func(idx int, ri resolvedItem) {
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
			}

			// Acquire semaphores (respects concurrency and writer limits).
			needsWrite := isWriteCapableMode(ri.mode)
			if err := e.acquireSemaphores(ctx, needsWrite); err != nil {
				results[idx] = tool.AgentExecResult{
					AgentID:   ri.item.ID,
					AgentName: ri.item.Name,
					Success:   false,
					Error:     fmt.Sprintf("semaphore acquire failed: %v", err),
				}
				return
			}
			defer e.releaseSemaphores(needsWrite)

			result, err := e.Run(ctx, execReq)
			if err != nil {
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

			results[idx] = execResult
		}(i, ri)
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
