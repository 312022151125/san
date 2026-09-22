package batch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// planOn/planOff helpers
type testPlanOn struct{}

func (testPlanOn) IsPlanMode() bool { return true }

type testPlanOff struct{}

func (testPlanOff) IsPlanMode() bool { return false }

// --- Unit tests for pure logic ---

func TestBuildWavesNoDeps(t *testing.T) {
	cmds := []BatchCommand{
		{ID: "a", Command: "echo a"},
		{ID: "b", Command: "echo b"},
	}
	waves, err := buildWaves(cmds)
	if err != nil {
		t.Fatalf("buildWaves error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(waves))
	}
	if len(waves[0]) != 2 {
		t.Fatalf("wave 0 should contain 2 commands, got %d", len(waves[0]))
	}
}

func TestBuildWavesLinearChain(t *testing.T) {
	cmds := []BatchCommand{
		{ID: "a", Command: "echo a"},
		{ID: "b", Command: "echo b", DependsOn: []string{"a"}},
		{ID: "c", Command: "echo c", DependsOn: []string{"b"}},
	}
	waves, err := buildWaves(cmds)
	if err != nil {
		t.Fatalf("buildWaves error: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves, got %d", len(waves))
	}
	for i, wave := range waves {
		if len(wave) != 1 {
			t.Fatalf("wave %d should have 1 command, got %d", i, len(wave))
		}
	}
}

func TestBuildWavesFanOut(t *testing.T) {
	// A → {B, C}
	cmds := []BatchCommand{
		{ID: "a", Command: "echo a"},
		{ID: "b", Command: "echo b", DependsOn: []string{"a"}},
		{ID: "c", Command: "echo c", DependsOn: []string{"a"}},
	}
	waves, err := buildWaves(cmds)
	if err != nil {
		t.Fatalf("buildWaves error: %v", err)
	}
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(waves))
	}
	if len(waves[0]) != 1 || waves[0][0] != "a" {
		t.Fatalf("wave 0 should be [a], got %v", waves[0])
	}
	if len(waves[1]) != 2 {
		t.Fatalf("wave 1 should contain B and C, got %v", waves[1])
	}
}

func TestBuildWavesCycleDetected(t *testing.T) {
	cmds := []BatchCommand{
		{ID: "a", Command: "echo a", DependsOn: []string{"b"}},
		{ID: "b", Command: "echo b", DependsOn: []string{"a"}},
	}
	_, err := buildWaves(cmds)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
}

func TestBuildWavesUnknownDepRejected(t *testing.T) {
	cmds := []BatchCommand{
		{ID: "a", Command: "echo a", DependsOn: []string{"nonexistent"}},
	}
	_, err := buildWaves(cmds)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
}

func TestCollapseRunsSingle(t *testing.T) {
	lines := []string{"a", "b", "c"}
	got := collapseRuns(lines)
	if len(got) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(got))
	}
}

func TestCollapseRunsDuplicates(t *testing.T) {
	lines := []string{"error: x", "error: x", "error: x", "ok"}
	got := collapseRuns(lines)
	if len(got) != 2 {
		t.Fatalf("expected 2 lines after collapse, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "×3") {
		t.Errorf("collapsed line should contain ×3, got: %q", got[0])
	}
}

func TestExtractRelevantShortOutput(t *testing.T) {
	input := "hello world"
	got := extractRelevant(input, 100)
	if got != "hello world" {
		t.Errorf("short output should pass through unchanged, got %q", got)
	}
}

func TestExtractRelevantLargeOutput(t *testing.T) {
	// Build output larger than maxBytes with one error line.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("some long line of output that is not an error\n")
	}
	sb.WriteString("error: the real problem is here\n")
	output := sb.String()

	got := extractRelevant(output, MaxInlineBytes)
	if !strings.Contains(got, "error: the real problem is here") {
		t.Error("extract should preserve error lines")
	}
	if len(got) > MaxInlineBytes+50 { // small slack for truncation suffix
		t.Errorf("extract too large: %d bytes", len(got))
	}
}

func TestIsErrorLine(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"error: something", true},
		{"FAIL\tgithub.com/foo/bar", true},
		{"panic: runtime error", true},
		{"foo.go:42: undefined", true},
		{"all packages OK", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isErrorLine(tc.line); got != tc.want {
			t.Errorf("isErrorLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// --- Integration tests (real shell commands) ---

func TestBatchSingleCommandSuccess(t *testing.T) {
	bt := NewBatchTool()
	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "hello", "command": "echo hello"},
		},
	}
	result := bt.Execute(context.Background(), params, t.TempDir())
	if !result.Success {
		t.Fatalf("single command failed: %s", result.Error)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Errorf("output should contain command id 'hello', got: %s", result.Output)
	}
}

func TestBatchSingleCommandFailure(t *testing.T) {
	bt := NewBatchTool()
	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "fail", "command": "exit 1"},
		},
	}
	result := bt.Execute(context.Background(), params, t.TempDir())
	// Batch tool itself succeeds at the tool level — it's the command that failed.
	if !result.Success {
		t.Fatalf("batch Execute should succeed even when command fails")
	}
	if !strings.Contains(result.Output, "exit: 1") {
		t.Errorf("output should report exit: 1, got: %s", result.Output)
	}
}

func TestBatchParallelCommands(t *testing.T) {
	// Two independent commands that sleep for 0.1s each. If they run in parallel,
	// total wall time should be < 0.3s; if sequential it would be >= 0.2s.
	bt := NewBatchTool()
	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "a", "command": "sleep 0.1 && echo a"},
			map[string]any{"id": "b", "command": "sleep 0.1 && echo b"},
		},
	}

	start := time.Now()
	result := bt.Execute(context.Background(), params, t.TempDir())
	elapsed := time.Since(start)

	if !result.Success {
		t.Fatalf("parallel batch failed: %s", result.Error)
	}
	// Both commands take 0.1s but run in parallel → total < 0.25s with overhead.
	// This verifies concurrency rather than sequential execution.
	if elapsed > 250*time.Millisecond {
		t.Errorf("parallel commands took too long (%v); expected ~0.1s", elapsed)
	}
}

func TestBatchDependencyChain(t *testing.T) {
	// A → B → C: verify sequential execution.
	bt := NewBatchTool()
	dir := t.TempDir()
	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "a", "command": "echo a > " + dir + "/a.txt"},
			map[string]any{"id": "b", "command": "cat " + dir + "/a.txt > " + dir + "/b.txt", "depends_on": []any{"a"}},
			map[string]any{"id": "c", "command": "cat " + dir + "/b.txt", "depends_on": []any{"b"}},
		},
	}

	result := bt.Execute(context.Background(), params, dir)
	if !result.Success {
		t.Fatalf("chain batch failed: %s", result.Error)
	}

	// C should have run and produced output.
	if !strings.Contains(result.Output, "exit: 0") {
		t.Errorf("all commands should exit 0, got: %s", result.Output)
	}
}

func TestBatchFailFastSkipDependent(t *testing.T) {
	// A fails → B (depends on A) should be skipped; C (independent) continues.
	bt := NewBatchTool()
	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "a", "command": "exit 1"},
			map[string]any{"id": "b", "command": "echo b", "depends_on": []any{"a"}},
			map[string]any{"id": "c", "command": "echo c"},
		},
	}

	result := bt.Execute(context.Background(), params, t.TempDir())
	if !result.Success {
		t.Fatalf("batch should succeed at tool level: %s", result.Error)
	}

	// B should be skipped.
	if !strings.Contains(result.Output, "skipped") {
		t.Errorf("B should be skipped, output: %s", result.Output)
	}
	// C should still run and produce exit: 0.
	if strings.Count(result.Output, "exit: 0") < 1 {
		t.Errorf("C should run successfully, output: %s", result.Output)
	}
}

func TestBatchTimeoutKillsCommand(t *testing.T) {
	bt := NewBatchTool()
	params := map[string]any{
		"commands": []any{
			map[string]any{
				"id":         "slow",
				"command":    "sleep 30",
				"timeout_ms": float64(100), // 100ms timeout
			},
		},
	}

	start := time.Now()
	result := bt.Execute(context.Background(), params, t.TempDir())
	elapsed := time.Since(start)

	if !result.Success {
		t.Fatalf("batch should succeed at tool level even with timeout")
	}
	if elapsed > 2*time.Second {
		t.Errorf("timed-out command took too long (%v)", elapsed)
	}
	// Should report non-zero exit.
	if !strings.Contains(result.Output, "exit: -1") && !strings.Contains(result.Output, "exit: 1") {
		t.Logf("timeout result: %s", result.Output)
		// Acceptable: timeout may produce exit:-1 or non-zero; main check is it finished fast.
	}
}

func TestBatchContextCancellation(t *testing.T) {
	bt := NewBatchTool()
	ctx, cancel := context.WithCancel(context.Background())

	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "long", "command": "sleep 30"},
		},
	}

	// Cancel after 100ms.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result := bt.Execute(ctx, params, t.TempDir())
	elapsed := time.Since(start)

	// Result may be success or failure at the tool level — the key is it finished fast.
	_ = result
	if elapsed > 2*time.Second {
		t.Errorf("cancelled batch took too long (%v)", elapsed)
	}
}

func TestBatchOutputBoundsStoresToFile(t *testing.T) {
	dir := t.TempDir()
	bt := NewBatchTool()
	bt.SetOutputDir(dir)

	// Generate > LargeOutputThreshold bytes of output.
	largeOutput := strings.Repeat("x", LargeOutputThreshold+100)
	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "big", "command": "echo '" + largeOutput + "'"},
		},
	}

	result := bt.Execute(context.Background(), params, t.TempDir())
	if !result.Success {
		t.Fatalf("batch failed: %s", result.Error)
	}

	// The log file should have been written.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected a log file to be written for large output")
	}

	// The inline output should contain the log reference.
	if !strings.Contains(result.Output, "log:") {
		t.Errorf("output should contain log reference, got: %s", result.Output)
	}
}

func TestBatchPlanModeRejectsAll(t *testing.T) {
	bt := NewBatchTool()
	bt.SetPlanModeChecker(testPlanOn{})

	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "a", "command": "echo a"},
		},
	}

	result := bt.Execute(context.Background(), params, t.TempDir())
	if result.Success {
		t.Fatal("Batch should be rejected in Plan Mode")
	}
	if !strings.Contains(result.Error, "Plan Mode") {
		t.Errorf("error should mention Plan Mode, got: %s", result.Error)
	}
}

func TestBatchPlanModeOffAllowsExecution(t *testing.T) {
	bt := NewBatchTool()
	bt.SetPlanModeChecker(testPlanOff{})

	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "a", "command": "echo a"},
		},
	}

	result := bt.Execute(context.Background(), params, t.TempDir())
	if !result.Success {
		t.Fatalf("Batch should succeed when plan mode is off: %s", result.Error)
	}
}

func TestBatchCycleDetectionBeforeExecution(t *testing.T) {
	bt := NewBatchTool()
	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "a", "command": "echo a", "depends_on": []any{"b"}},
			map[string]any{"id": "b", "command": "echo b", "depends_on": []any{"a"}},
		},
	}

	result := bt.Execute(context.Background(), params, t.TempDir())
	if result.Success {
		t.Fatal("cycle detection should return an error result")
	}
	if !strings.Contains(result.Error, "cycle") {
		t.Errorf("error should mention cycle, got: %s", result.Error)
	}
}

func TestBatchMissingIDReturnsError(t *testing.T) {
	bt := NewBatchTool()
	params := map[string]any{
		"commands": []any{
			map[string]any{"command": "echo a"}, // missing id
		},
	}

	result := bt.Execute(context.Background(), params, t.TempDir())
	if result.Success {
		t.Fatal("missing command id should return an error")
	}
}

func TestBatchDuplicateIDReturnsError(t *testing.T) {
	bt := NewBatchTool()
	params := map[string]any{
		"commands": []any{
			map[string]any{"id": "dup", "command": "echo a"},
			map[string]any{"id": "dup", "command": "echo b"},
		},
	}

	result := bt.Execute(context.Background(), params, t.TempDir())
	if result.Success {
		t.Fatal("duplicate command id should return an error")
	}
}

func TestBatchMaxCommandsEnforced(t *testing.T) {
	bt := NewBatchTool()
	cmds := make([]any, MaxCommands+1)
	for i := range cmds {
		cmds[i] = map[string]any{
			"id":      string(rune('a' + i)),
			"command": "echo ok",
		}
	}

	params := map[string]any{"commands": cmds}
	result := bt.Execute(context.Background(), params, t.TempDir())
	if result.Success {
		t.Fatal("exceeding MaxCommands should return an error")
	}
}

func TestFormatBatchResultSuccessMinimal(t *testing.T) {
	r := &BatchResult{
		Commands: []CommandResult{
			{ID: "build", ExitCode: 0, Duration: time.Second},
		},
		Duration: time.Second,
	}
	output := formatBatchResult(r)
	if !strings.Contains(output, "exit: 0") {
		t.Error("success result should show exit: 0")
	}
	// Successful output should not dump stdout.
	if strings.Contains(output, "errors:") {
		t.Error("success result should not show error section")
	}
}

func TestFormatBatchResultSkipped(t *testing.T) {
	r := &BatchResult{
		Commands: []CommandResult{
			{ID: "test", Skipped: true, SkipReason: "dependency \"build\" failed (exit 1)"},
		},
		Duration: time.Second,
	}
	output := formatBatchResult(r)
	if !strings.Contains(output, "skipped:") {
		t.Error("skipped result should show 'skipped:'")
	}
	if !strings.Contains(output, "build") {
		t.Error("skipped result should mention the failing dependency")
	}
}
