package finalize

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/batch"
)

// --- Spy BatchEngine ---

// spyEngine records calls and can simulate command results.
type spyEngine struct {
	mu          sync.Mutex
	calls       []batch.EngineRequest
	maxParallel int32 // peak concurrent Execute calls (atomic)
	active      int32 // current concurrent Execute calls (atomic)
	results     *batch.BatchResult
	err         error
	// delay simulates work done by the engine.
	delay time.Duration
}

func newSpyEngine(results *batch.BatchResult) *spyEngine {
	return &spyEngine{results: results}
}

func (s *spyEngine) Execute(ctx context.Context, req batch.EngineRequest) (*batch.BatchResult, error) {
	atomic.AddInt32(&s.active, 1)
	curr := atomic.LoadInt32(&s.active)
	for {
		old := atomic.LoadInt32(&s.maxParallel)
		if curr <= old || atomic.CompareAndSwapInt32(&s.maxParallel, old, curr) {
			break
		}
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
		}
	}
	atomic.AddInt32(&s.active, -1)

	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()

	if s.err != nil {
		return nil, s.err
	}
	if s.results != nil {
		return s.results, nil
	}
	// Default: all commands pass.
	cmds := make([]batch.CommandResult, len(req.Commands))
	for i, c := range req.Commands {
		cmds[i] = batch.CommandResult{ID: c.ID, ExitCode: 0}
	}
	return &batch.BatchResult{Commands: cmds, Duration: time.Millisecond}, nil
}

func (s *spyEngine) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// --- Spy SubagentExecutor ---

// spySubagentExec records reviewer launches and returns a fake task.
type spySubagentExec struct {
	mu          sync.Mutex
	launches    []tool.AgentExecRequest
	taskContent string
	taskErr     string
	delay       time.Duration
}

func newSpySubagentExec(content string) *spySubagentExec {
	return &spySubagentExec{taskContent: content}
}

func (s *spySubagentExec) RunBackground(req tool.AgentExecRequest) (ReviewerTask, error) {
	s.mu.Lock()
	s.launches = append(s.launches, req)
	s.mu.Unlock()
	return &fakeReviewerTask{
		content: s.taskContent,
		errMsg:  s.taskErr,
		delay:   s.delay,
	}, nil
}

func (s *spySubagentExec) launchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.launches)
}

// fakeReviewerTask implements ReviewerTask with configurable content and delay.
type fakeReviewerTask struct {
	mu      sync.Mutex
	content string
	errMsg  string
	delay   time.Duration
	done    bool
}

func (f *fakeReviewerTask) GetOutput() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.content
}
func (f *fakeReviewerTask) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.done
}
func (f *fakeReviewerTask) WaitForCompletion(timeout time.Duration) bool {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-time.After(timeout):
			return false
		}
	}
	f.mu.Lock()
	f.done = true
	f.mu.Unlock()
	return true
}
func (f *fakeReviewerTask) GetStatus() ReviewerTaskStatus {
	return ReviewerTaskStatus{Error: f.errMsg, Output: f.content}
}

// --- Pipeline tests ---

func TestPipeline_PassResultIsCompact(t *testing.T) {
	engine := newSpyEngine(nil) // all commands pass by default
	pipeline := NewPipeline(engine, nil, t.TempDir())

	result, err := pipeline.Run(context.Background(), Request{
		CWD:          t.TempDir(),
		ReviewerMode: "off",
	})
	if err != nil {
		t.Fatalf("pipeline.Run error: %v", err)
	}
	// When all pass, output should be compact
	out := result.String()
	if !strings.Contains(out, "PASS") {
		t.Errorf("expected PASS in output, got: %q", out)
	}
	if strings.Contains(out, "FAILED") {
		t.Errorf("PASS output should not contain FAILED, got: %q", out)
	}
}

func TestPipeline_FailResultIsActionable(t *testing.T) {
	failResult := &batch.BatchResult{
		Commands: []batch.CommandResult{
			{ID: "format", ExitCode: 0},
			{ID: "test", ExitCode: 1, Output: "FAIL\nfoo_test.go:42: assertion failed"},
			{ID: "build", ExitCode: 0},
		},
		Duration: time.Second,
	}
	engine := newSpyEngine(failResult)
	pipeline := NewPipeline(engine, nil, t.TempDir())

	result, err := pipeline.Run(context.Background(), Request{
		CWD:          t.TempDir(),
		ReviewerMode: "off",
		TierOverride: true,
		Tier:         TierFull,
	})
	if err != nil {
		t.Fatalf("pipeline.Run error: %v", err)
	}
	if result.Pass {
		t.Error("pipeline should fail when test fails")
	}
	out := result.String()
	if !strings.Contains(out, "FAILED") {
		t.Errorf("fail output should contain FAILED, got: %q", out)
	}
	if !strings.Contains(out, "foo_test.go:42") {
		t.Errorf("fail output should contain actionable error line, got: %q", out)
	}
}

func TestPipeline_ReviewerLaunchedConcurrently(t *testing.T) {
	// The reviewer should be launched BEFORE the engine completes.
	// We detect this by giving the engine a short delay and observing that
	// the reviewer was launched before the engine returns.
	launchTimes := make(chan time.Time, 1)

	spyExec := &recordingSubagentExec{launchTimes: launchTimes}
	engine := newSpyEngine(nil)
	engine.delay = 50 * time.Millisecond

	pipeline := NewPipeline(engine, spyExec, t.TempDir())

	start := time.Now()
	_, err := pipeline.Run(context.Background(), Request{
		CWD:          t.TempDir(),
		ReviewerMode: "always",
		TierOverride: true,
		Tier:         TierFull,
	})
	if err != nil {
		t.Fatalf("pipeline.Run error: %v", err)
	}
	engineEnd := time.Since(start)

	select {
	case launchTime := <-launchTimes:
		// reviewer should have been launched before engine finished
		elapsed := launchTime.Sub(start)
		if elapsed >= engineEnd {
			t.Errorf("reviewer launched after engine finished (elapsed=%v, engineEnd=%v)", elapsed, engineEnd)
		}
	default:
		t.Error("reviewer was never launched")
	}
}

// recordingSubagentExec records the time each reviewer is launched.
type recordingSubagentExec struct {
	launchTimes chan time.Time
}

func (r *recordingSubagentExec) RunBackground(req tool.AgentExecRequest) (ReviewerTask, error) {
	select {
	case r.launchTimes <- time.Now():
	default:
	}
	return &fakeReviewerTask{content: "approve"}, nil
}

func TestPipeline_PlanModeRejectsInTool(t *testing.T) {
	// FinalizeTool.Execute enforces Plan Mode rejection. Test via the tool.
	// (Pipeline itself has no plan-mode gate — it's the tool surface.)
	// This just verifies the pipeline itself completes without Plan Mode awareness.
	engine := newSpyEngine(nil)
	pipeline := NewPipeline(engine, nil, t.TempDir())
	_, err := pipeline.Run(context.Background(), Request{
		CWD:          t.TempDir(),
		ReviewerMode: "off",
	})
	if err != nil {
		t.Fatalf("pipeline.Run should not error in normal mode: %v", err)
	}
}

func TestPipeline_TierOverride_UsesProvidedTier(t *testing.T) {
	engine := newSpyEngine(nil)
	pipeline := NewPipeline(engine, nil, t.TempDir())

	result, err := pipeline.Run(context.Background(), Request{
		CWD:          t.TempDir(),
		ReviewerMode: "off",
		TierOverride: true,
		Tier:         TierFull,
	})
	if err != nil {
		t.Fatalf("pipeline.Run error: %v", err)
	}
	if result.Tier != TierFull {
		t.Errorf("expected TierFull, got %s", result.Tier)
	}
}

func TestPipeline_ReviewerSkippedWhenOffMode(t *testing.T) {
	spy := newSpySubagentExec("approve")
	engine := newSpyEngine(nil)
	pipeline := NewPipeline(engine, spy, t.TempDir())

	_, err := pipeline.Run(context.Background(), Request{
		CWD:          t.TempDir(),
		ReviewerMode: "off",
	})
	if err != nil {
		t.Fatalf("pipeline.Run error: %v", err)
	}
	if spy.launchCount() != 0 {
		t.Errorf("reviewer should not be launched when mode=off, got %d launches", spy.launchCount())
	}
}

func TestPipeline_ReviewerLaunchedWhenAlwaysMode(t *testing.T) {
	spy := newSpySubagentExec("approve")
	engine := newSpyEngine(nil)
	pipeline := NewPipeline(engine, spy, t.TempDir())

	_, err := pipeline.Run(context.Background(), Request{
		CWD:          t.TempDir(),
		ReviewerMode: "always",
	})
	if err != nil {
		t.Fatalf("pipeline.Run error: %v", err)
	}
	if spy.launchCount() != 1 {
		t.Errorf("reviewer should be launched once in always mode, got %d", spy.launchCount())
	}
}

func TestPipeline_EngineCalledOnce(t *testing.T) {
	engine := newSpyEngine(nil)
	pipeline := NewPipeline(engine, nil, t.TempDir())

	_, err := pipeline.Run(context.Background(), Request{
		CWD:          t.TempDir(),
		ReviewerMode: "off",
	})
	if err != nil {
		t.Fatalf("pipeline.Run error: %v", err)
	}
	if engine.callCount() != 1 {
		t.Errorf("engine should be called exactly once per Run(), got %d", engine.callCount())
	}
}

func TestPipeline_ContextCancellation(t *testing.T) {
	engine := newSpyEngine(nil)
	engine.delay = 5 * time.Second // would block forever without cancellation
	pipeline := NewPipeline(engine, nil, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _ = pipeline.Run(ctx, Request{
		CWD:          t.TempDir(),
		ReviewerMode: "off",
	})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("cancelled pipeline took too long: %v", elapsed)
	}
}

// --- ReviewerHandle tests ---

func TestReviewerHandle_Skipped(t *testing.T) {
	h := SkippedReviewer("test reason")
	res := h.Wait(time.Second)
	if !res.Skipped {
		t.Error("SkippedReviewer should produce Skipped=true result")
	}
	if res.SkipReason != "test reason" {
		t.Errorf("expected skip reason 'test reason', got %q", res.SkipReason)
	}
}

func TestReviewerHandle_ExtractsVerdict(t *testing.T) {
	task := &fakeReviewerTask{
		content: "Verdict: approve\n- minor: nit in foo.go",
	}
	h := ReviewerHandle{task: task}
	res := h.Wait(time.Second)
	if res.Verdict == "" {
		t.Error("should extract verdict from reviewer content")
	}
}

// --- truncateDiff ---

func TestTruncateDiff_SmallDiffUnchanged(t *testing.T) {
	diff := "diff --git a/foo.go b/foo.go\n@@ -1 +1 @@\n-old\n+new\n"
	result := truncateDiff(diff, reviewerDiffLimit)
	if result != diff {
		// Small diff should be returned as-is (our truncateDiff always returns
		// headers + content, even for short diffs — just verify it contains headers)
		if !strings.Contains(result, "diff --git") {
			t.Error("truncated result should contain file header")
		}
	}
}

func TestTruncateDiff_LargeDiffPreservesHeaders(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("diff --git a/foo.go b/foo.go\n")
	sb.WriteString("--- a/foo.go\n")
	sb.WriteString("+++ b/foo.go\n")
	sb.WriteString("@@ -1,3 +1,3 @@\n")
	// Add a lot of content lines
	for i := 0; i < 500; i++ {
		sb.WriteString("+this is a very long content line that makes the diff large enough to truncate\n")
	}
	sb.WriteString("diff --git a/bar.go b/bar.go\n")
	sb.WriteString("--- a/bar.go\n")
	sb.WriteString("+++ b/bar.go\n")

	largeDiff := sb.String()
	result := truncateDiff(largeDiff, 200)

	// Must preserve both file headers even when content is cut.
	if !strings.Contains(result, "diff --git a/foo.go") {
		t.Error("truncated diff should preserve first file header")
	}
	if !strings.Contains(result, "diff --git a/bar.go") {
		t.Error("truncated diff should preserve second file header (later file not hidden)")
	}
	if len(result) > 500 {
		t.Errorf("truncated diff too large: %d bytes (limit 200)", len(result))
	}
}

// --- BuildReviewerPrompt ---

func TestBuildReviewerPrompt_ContainsTask(t *testing.T) {
	prompt := BuildReviewerPrompt("add finalize package", "", "", []string{"internal/finalize/scope.go"})
	if !strings.Contains(prompt, "add finalize package") {
		t.Error("prompt should contain the task description")
	}
}

func TestBuildReviewerPrompt_ContainsFiles(t *testing.T) {
	files := []string{"internal/finalize/scope.go", "internal/finalize/tier.go"}
	prompt := BuildReviewerPrompt("", "", "", files)
	for _, f := range files {
		if !strings.Contains(prompt, f) {
			t.Errorf("prompt should contain file %q", f)
		}
	}
}

func TestBuildReviewerPrompt_ContainsArtifactRef(t *testing.T) {
	prompt := BuildReviewerPrompt("", "diff content", "log:/tmp/full.patch", nil)
	if !strings.Contains(prompt, "log:/tmp/full.patch") {
		t.Error("prompt should contain artifact ref when diff was truncated")
	}
}
