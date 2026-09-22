package subagent

import (
	"context"
	"os"
	"testing"

	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool"
)

// planModeOn is a PlanModeChecker that always reports plan mode active.
type planModeOn struct{}

func (planModeOn) IsPlanMode() bool { return true }

// planModeOff is a PlanModeChecker that always reports plan mode inactive.
type planModeOff struct{}

func (planModeOff) IsPlanMode() bool { return false }

// TestBuildBatchChildPromptNoContext verifies that when there is no shared
// context, only the task delta is returned.
func TestBuildBatchChildPromptNoContext(t *testing.T) {
	got := buildBatchChildPrompt("", "do something")
	if got != "do something" {
		t.Errorf("got %q, want %q", got, "do something")
	}
}

// TestBuildBatchChildPromptNoTask verifies that when there is no task delta,
// only the shared context is returned.
func TestBuildBatchChildPromptNoTask(t *testing.T) {
	got := buildBatchChildPrompt("shared ctx", "")
	if got != "shared ctx" {
		t.Errorf("got %q, want %q", got, "shared ctx")
	}
}

// TestBuildBatchChildPromptCombines verifies that both parts are combined with
// the separator.
func TestBuildBatchChildPromptCombines(t *testing.T) {
	got := buildBatchChildPrompt("shared", "delta")
	if got != "shared\n\n---\n\ndelta" {
		t.Errorf("got %q", got)
	}
}

// TestIsWriteCapableMode verifies explore is not write-capable and the others are.
func TestIsWriteCapableMode(t *testing.T) {
	cases := []struct {
		mode PermissionMode
		want bool
	}{
		{PermissionExplore, false},
		{PermissionDefault, true},
		{PermissionAcceptEdits, true},
		{PermissionBypass, true},
		{PermissionDontAsk, true},
	}
	for _, tc := range cases {
		if got := isWriteCapableMode(tc.mode); got != tc.want {
			t.Errorf("isWriteCapableMode(%q) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

// TestRunBatchEmptyTasksReturnsError verifies that an empty task list is rejected.
func TestRunBatchEmptyTasksReturnsError(t *testing.T) {
	executor := &Executor{registry: Default()}
	_, err := executor.RunBatch(context.Background(), tool.AgentBatchRequest{}, "", nil)
	if err == nil {
		t.Fatal("expected error for empty task list")
	}
}

// TestRunBatchDuplicateIDRejected verifies that duplicate IDs in the batch
// are caught in preflight before any agent starts.
func TestRunBatchDuplicateIDRejected(t *testing.T) {
	executor := &Executor{registry: Default()}
	req := tool.AgentBatchRequest{
		Tasks: []tool.AgentBatchItem{
			{ID: "dup", Task: "task A"},
			{ID: "dup", Task: "task B"},
		},
	}
	_, err := executor.RunBatch(context.Background(), req, "", nil)
	if err == nil {
		t.Fatal("expected error for duplicate item IDs")
	}
}

// TestRunBatchDuplicateNameRejected verifies that duplicate display names in
// the batch are caught in preflight.
func TestRunBatchDuplicateNameRejected(t *testing.T) {
	executor := &Executor{registry: Default()}
	req := tool.AgentBatchRequest{
		Tasks: []tool.AgentBatchItem{
			{Name: "same", Task: "task A"},
			{Name: "same", Task: "task B"},
		},
	}
	_, err := executor.RunBatch(context.Background(), req, "", nil)
	if err == nil {
		t.Fatal("expected error for duplicate item names")
	}
}

// TestRunBatchEmptyTaskPromptRejected verifies that a task with an empty
// prompt string is rejected in preflight.
func TestRunBatchEmptyTaskPromptRejected(t *testing.T) {
	executor := &Executor{registry: Default()}
	req := tool.AgentBatchRequest{
		Tasks: []tool.AgentBatchItem{
			{Task: ""},
		},
	}
	_, err := executor.RunBatch(context.Background(), req, "", nil)
	if err == nil {
		t.Fatal("expected error for empty task prompt")
	}
}

// TestRunBatchPlanModeRejectsWriteCapableItems verifies that write-capable
// agents are rejected atomically when plan mode is active.
func TestRunBatchPlanModeRejectsWriteCapableItems(t *testing.T) {
	registry := NewRegistry()
	exploreConfig := BuiltinExploreConfig()
	registry.Register(exploreConfig)
	executor := &Executor{registry: registry}

	// An agent with default permission mode is write-capable.
	req := tool.AgentBatchRequest{
		Tasks: []tool.AgentBatchItem{
			// explore agent — should be OK
			{Agent: "explore", Task: "investigate something", Mode: "explore"},
			// unnamed agent defaults to PermissionDefault → write-capable
			{Task: "implement something", Mode: "default"},
		},
	}

	_, err := executor.RunBatch(context.Background(), req, "", planModeOn{})
	if err == nil {
		t.Fatal("expected error: batch contains write-capable agent in Plan Mode")
	}
}

// TestRunBatchPlanModeAllowsExploreOnlyBatch verifies that a batch containing
// only explore-mode agents does not fail at plan mode preflight (it fails later
// at actual execution in this unit test, but not at plan mode check).
func TestRunBatchPlanModeAllowsExploreOnlyBatch(t *testing.T) {
	registry := NewRegistry()
	exploreConfig := BuiltinExploreConfig()
	registry.Register(exploreConfig)
	executor := &Executor{
		registry:       registry,
		concurrencySem: make(chan struct{}, 3),
		writerSem:      make(chan struct{}, 1),
	}

	// All items are explore-mode — preflight should pass plan mode check.
	// The run itself will fail because there is no LLM provider, but the
	// plan mode check must NOT be the reason.
	req := tool.AgentBatchRequest{
		Context: "shared context",
		Tasks: []tool.AgentBatchItem{
			{Agent: "explore", Task: "read files", Mode: "explore"},
		},
	}

	_, err := executor.RunBatch(context.Background(), req, "", planModeOn{})
	// We expect an error here (no provider configured), but NOT a plan mode error.
	if err != nil {
		msg := err.Error()
		if containsStr(msg, "Plan Mode") {
			t.Errorf("explore-only batch should not fail plan mode check, got: %v", err)
		}
	}
}

// TestRunBatchPlanModeNilCheckerAllowed verifies that a nil PlanModeChecker
// means plan mode is not active (no restriction).
func TestRunBatchPlanModeNilCheckerAllowed(t *testing.T) {
	executor := &Executor{
		registry:       Default(),
		concurrencySem: make(chan struct{}, 3),
		writerSem:      make(chan struct{}, 1),
	}

	req := tool.AgentBatchRequest{
		Tasks: []tool.AgentBatchItem{
			{Task: "do something"},
		},
	}

	// With nil checker, plan mode is inactive — should reach execution (which
	// fails without a provider, but not at plan mode preflight).
	_, err := executor.RunBatch(context.Background(), req, "", nil)
	if err != nil {
		msg := err.Error()
		if containsStr(msg, "Plan Mode") {
			t.Errorf("nil plan mode checker must not cause plan mode rejection: %v", err)
		}
	}
}

// TestStoreBatchArtifactWritesAndReturnsRef verifies that storeBatchArtifact
// creates the file and returns an "agent://<id>" reference.
func TestStoreBatchArtifactWritesAndReturnsRef(t *testing.T) {
	dir := t.TempDir()
	content := "large result content"
	ref, err := storeBatchArtifact(dir, "abc123", content)
	if err != nil {
		t.Fatalf("storeBatchArtifact error: %v", err)
	}
	if ref != "agent://abc123" {
		t.Errorf("ref = %q, want agent://abc123", ref)
	}

	// Verify file was written.
	data, readErr := os.ReadFile(dir + "/abc123.md")
	if readErr != nil {
		t.Fatalf("file not created: %v", readErr)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", data, content)
	}
}

// ---- extractYield tests (P1-A) ----

// TestExtractYieldEmpty verifies that empty content returns an empty summary.
func TestExtractYieldEmpty(t *testing.T) {
	y := extractYield("")
	if y == nil {
		t.Fatal("expected non-nil AgentYield for empty content")
	}
	if y.Summary != "" {
		t.Errorf("expected empty summary for empty content, got %q", y.Summary)
	}
}

// TestExtractYieldSummaryHeading verifies that a "## Summary" section is
// extracted as the structured summary.
func TestExtractYieldSummaryHeading(t *testing.T) {
	content := `## Summary
Provider routing uses a round-robin strategy.

## Details
Some other details.`
	y := extractYield(content)
	if y == nil || y.Summary == "" {
		t.Fatalf("expected non-empty summary, got %+v", y)
	}
	if !containsStr(y.Summary, "round-robin") {
		t.Errorf("summary should contain extracted text, got %q", y.Summary)
	}
}

// TestExtractYieldSingleHashHeading verifies that a "# Summary" heading also works.
func TestExtractYieldSingleHashHeading(t *testing.T) {
	content := "# Summary\nFound 3 files.\n"
	y := extractYield(content)
	if y == nil || y.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !containsStr(y.Summary, "Found 3 files") {
		t.Errorf("unexpected summary: %q", y.Summary)
	}
}

// TestExtractYieldFallbackFirstParagraph verifies that when there is no
// Summary heading, the first paragraph is used.
func TestExtractYieldFallbackFirstParagraph(t *testing.T) {
	content := "Explored the provider package. Found routing in provider.go.\n\nOther info follows."
	y := extractYield(content)
	if y == nil || y.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !containsStr(y.Summary, "provider package") {
		t.Errorf("unexpected summary: %q", y.Summary)
	}
}

// TestExtractYieldTruncatesLongContent verifies that content longer than
// summaryMaxChars is truncated with an ellipsis marker.
func TestExtractYieldTruncatesLongContent(t *testing.T) {
	longContent := make([]byte, summaryMaxChars+200)
	for i := range longContent {
		longContent[i] = 'a'
	}
	y := extractYield(string(longContent))
	if y == nil {
		t.Fatal("expected non-nil yield")
	}
	if len(y.Summary) > summaryMaxChars+10 {
		t.Errorf("summary not truncated: len=%d", len(y.Summary))
	}
}

// ---- Depth propagation tests (P2-B) ----

// TestAgentExecRequestDepthField verifies that the Depth field exists and
// can be set (compilation check plus basic sanity).
func TestAgentExecRequestDepthField(t *testing.T) {
	req := tool.AgentExecRequest{Depth: 1}
	if req.Depth != 1 {
		t.Errorf("Depth field not propagated: got %d", req.Depth)
	}
}

// ---- StatusQueued / MarkRunning tests (P2-A) ----

// TestNewQueuedAgentTaskInitialStatus verifies a queued task starts with
// StatusQueued and IsRunning returns true (it is active work).
func TestNewQueuedAgentTaskInitialStatus(t *testing.T) {
	at := task.NewQueuedAgentTask("id1", "explore", "map routing", "")
	if at.Status != task.StatusQueued {
		t.Errorf("expected StatusQueued, got %q", at.Status)
	}
	if !at.IsRunning() {
		t.Error("IsRunning should return true for a queued task")
	}
}

// TestMarkRunningTransitionsStatus verifies that MarkRunning moves the task
// from StatusQueued to StatusRunning.
func TestMarkRunningTransitionsStatus(t *testing.T) {
	at := task.NewQueuedAgentTask("id2", "worker", "implement X", "")
	at.MarkRunning()
	if at.Status != task.StatusRunning {
		t.Errorf("expected StatusRunning after MarkRunning, got %q", at.Status)
	}
}

// TestMarkRunningIsIdempotent verifies that calling MarkRunning on a task
// that is already Running (or terminal) is a no-op.
func TestMarkRunningIsIdempotent(t *testing.T) {
	at := task.NewQueuedAgentTask("id3", "worker", "implement Y", "")
	at.MarkRunning()
	at.MarkRunning() // second call should not panic or change state
	if at.Status != task.StatusRunning {
		t.Errorf("expected StatusRunning, got %q", at.Status)
	}
}

// TestQueuedTaskFinalizeBeforeMarkRunning verifies that Complete() works on a
// queued task that never became Running (semaphore error path).
func TestQueuedTaskFinalizeBeforeMarkRunning(t *testing.T) {
	at := task.NewQueuedAgentTask("id4", "worker", "never ran", "")
	at.Complete(nil) // should not deadlock or panic
	if at.IsRunning() {
		t.Error("completed task should not report IsRunning")
	}
}

// containsStr is a simple substring check without importing strings to keep
// this file dependency-light.
func containsStr(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
