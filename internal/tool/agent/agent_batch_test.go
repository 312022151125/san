package agent

import (
	"context"
	"testing"
	"time"

	"github.com/genai-io/san/internal/tool"
)

// TestAgentBatchBasicRouting verifies that tasks[] triggers the batch path and
// each task is forwarded to RunBatch with the shared context.
func TestAgentBatchBasicRouting(t *testing.T) {
	executor := &recordingExecutor{configOK: true}
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	params := map[string]any{
		"context": "Shared background context for all tasks.",
		"tasks": []any{
			map[string]any{
				"agent": "explore",
				"name":  "ExploreRouting",
				"task":  "Map provider resolution.",
				"mode":  "explore",
			},
			map[string]any{
				"agent": "explore",
				"name":  "ExploreSettings",
				"task":  "Map model settings flow.",
				"mode":  "explore",
			},
		},
	}

	result := agentTool.Execute(context.Background(), params, ".")
	if !result.Success {
		t.Fatalf("batch Execute() failed: %s", result.Error)
	}

	// Verify the batch request was forwarded correctly.
	if executor.batchReq.Context != "Shared background context for all tasks." {
		t.Errorf("shared context = %q, want expected value", executor.batchReq.Context)
	}
	if len(executor.batchReq.Tasks) != 2 {
		t.Fatalf("batch tasks = %d, want 2", len(executor.batchReq.Tasks))
	}
	if executor.batchReq.Tasks[0].Name != "ExploreRouting" {
		t.Errorf("task[0].Name = %q, want ExploreRouting", executor.batchReq.Tasks[0].Name)
	}
	if executor.batchReq.Tasks[1].Task != "Map model settings flow." {
		t.Errorf("task[1].Task = %q, want map model settings flow", executor.batchReq.Tasks[1].Task)
	}
}

// TestAgentBatchOutputContainsAllAgents verifies the combined result output
// mentions each agent.
func TestAgentBatchOutputContainsAllAgents(t *testing.T) {
	executor := &recordingExecutor{configOK: true}
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	params := map[string]any{
		"context": "ctx",
		"tasks": []any{
			map[string]any{"name": "Alpha", "task": "do alpha"},
			map[string]any{"name": "Beta", "task": "do beta"},
		},
	}

	result := agentTool.Execute(context.Background(), params, ".")
	if !result.Success {
		t.Fatalf("Execute() failed: %s", result.Error)
	}

	for _, name := range []string{"Alpha", "Beta"} {
		if len(result.Output) == 0 {
			t.Fatal("output is empty")
		}
		found := false
		for i := 0; i+len(name) <= len(result.Output); i++ {
			if result.Output[i:i+len(name)] == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("output should contain agent name %q", name)
		}
	}
}

// TestAgentBatchEmptyTasksReturnsError verifies that an empty tasks[] is rejected.
func TestAgentBatchEmptyTasksReturnsError(t *testing.T) {
	executor := &recordingExecutor{configOK: true}
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	params := map[string]any{
		"context": "ctx",
		"tasks":   []any{},
	}

	result := agentTool.Execute(context.Background(), params, ".")
	if result.Success {
		t.Fatal("empty tasks[] should return an error result")
	}
}

// TestAgentBatchMissingTaskFieldReturnsError verifies that a task item without
// a "task" field is rejected.
func TestAgentBatchMissingTaskFieldReturnsError(t *testing.T) {
	executor := &recordingExecutor{configOK: true}
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	params := map[string]any{
		"context": "ctx",
		"tasks": []any{
			map[string]any{"name": "NoTask"},
		},
	}

	result := agentTool.Execute(context.Background(), params, ".")
	if result.Success {
		t.Fatal("missing task field should return an error result")
	}
}

// TestAgentBatchSingleAgentRoutesSingleRun verifies that a single task in
// tasks[] goes through RunBatch (not single-agent Run).
func TestAgentBatchSingleAgentRoutesSingleRun(t *testing.T) {
	executor := &recordingExecutor{configOK: true}
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	params := map[string]any{
		"context": "ctx",
		"tasks": []any{
			map[string]any{"task": "do something"},
		},
	}

	result := agentTool.Execute(context.Background(), params, ".")
	if !result.Success {
		t.Fatalf("single batch task failed: %s", result.Error)
	}
	if len(executor.batchReq.Tasks) != 1 {
		t.Fatalf("batchReq.Tasks = %d, want 1", len(executor.batchReq.Tasks))
	}
	// Single-agent run path should not have been used.
	if executor.runReq.Prompt != "" {
		t.Error("single-agent Run should not have been called for batch invocation")
	}
}

// TestAgentBatchFormatResultIncludesDuration verifies that the result subtitle
// mentions agent count and duration.
func TestAgentBatchFormatResultIncludesDuration(t *testing.T) {
	batch := &tool.AgentBatchResult{
		Results: []tool.AgentExecResult{
			{AgentName: "Worker1", Success: true, Content: "done", Duration: time.Second},
			{AgentName: "Worker2", Success: true, Content: "done", Duration: 2 * time.Second},
		},
		Duration: 3 * time.Second,
	}

	output := formatBatchResult(batch)
	for _, want := range []string{"Worker1", "Worker2", "Batch completed: 2"} {
		found := false
		for i := 0; i+len(want) <= len(output); i++ {
			if output[i:i+len(want)] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("formatBatchResult output should contain %q", want)
		}
	}
}

// TestAgentBatchFormatResultHidesActivityTrace verifies that raw activity
// lines from individual agents do not appear in the batch result output.
func TestAgentBatchFormatResultHidesActivityTrace(t *testing.T) {
	batch := &tool.AgentBatchResult{
		Results: []tool.AgentExecResult{
			{
				AgentName: "Worker1",
				Success:   true,
				Content:   "summary of findings",
				// Activity would be tool-call noise — must NOT appear in output.
				Activity: []string{
					"Read(internal/foo/foo.go)",
					"Grep(pattern)",
					"Edit(internal/foo/foo.go)",
				},
				Duration: time.Second,
			},
		},
		Duration: time.Second,
	}

	output := formatBatchResult(batch)
	for _, trace := range batch.Results[0].Activity {
		found := false
		for i := 0; i+len(trace) <= len(output); i++ {
			if output[i:i+len(trace)] == trace {
				found = true
				break
			}
		}
		if found {
			t.Errorf("activity trace line %q must not appear in batch output", trace)
		}
	}
}

// TestAgentBatchResultRefIncludedWhenSet verifies that a ResultRef is included
// in the output so the model knows large results were stored to disk.
func TestAgentBatchResultRefIncludedWhenSet(t *testing.T) {
	batch := &tool.AgentBatchResult{
		Results: []tool.AgentExecResult{
			{
				AgentName: "BigWorker",
				Success:   true,
				Content:   "(result truncated)",
				ResultRef: "agent://bigworker-abc123",
				Duration:  time.Second,
			},
		},
		Duration: time.Second,
	}

	output := formatBatchResult(batch)
	wantRef := "agent://bigworker-abc123"
	found := false
	for i := 0; i+len(wantRef) <= len(output); i++ {
		if output[i:i+len(wantRef)] == wantRef {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ResultRef %q must appear in batch output", wantRef)
	}
}
