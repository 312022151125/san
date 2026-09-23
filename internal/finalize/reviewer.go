package finalize

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/genai-io/san/internal/tool"
)

// reviewerDiffLimit is the maximum number of bytes of diff content that is
// sent inline to the reviewer. Diffs larger than this are truncated
// intelligently (preserving file headers and hunk headers) and the full diff
// is stored as an artifact. The reviewer receives the truncated content plus
// an artifact reference so it can retrieve more only when necessary.
const reviewerDiffLimit = 16 * 1024 // 16 KiB

// reviewerAgentName is the built-in reviewer agent used for all finalize reviews.
const reviewerAgentName = "reviewer"

// ReviewerTask is the minimal interface over *task.AgentTask that the finalize
// package needs. Defining it here (consumer side) avoids importing
// internal/task directly from internal/finalize.
type ReviewerTask interface {
	// GetOutput returns the accumulated output so far.
	GetOutput() string
	// IsRunning reports whether the task is still executing.
	IsRunning() bool
	// WaitForCompletion blocks until the task finishes or the timeout expires.
	// Returns true if completed, false on timeout.
	WaitForCompletion(timeout time.Duration) bool
	// GetStatus returns status info including the error field.
	GetStatus() ReviewerTaskStatus
}

// ReviewerTaskStatus is the projection of task.TaskInfo that we care about.
type ReviewerTaskStatus struct {
	Error  string
	Output string
}

// SubagentExecutor is the minimal interface over *subagent.Executor that
// FinalizePipeline requires to launch a background reviewer. Defined here
// (consumer side) so internal/finalize does not import internal/subagent.
//
// The composition layer (cmd/san / internal/app) passes the concrete
// *subagent.Executor, which satisfies this interface via adapter.
type SubagentExecutor interface {
	RunBackground(req tool.AgentExecRequest) (ReviewerTask, error)
}

// ReviewerResult holds the outcome of a speculative reviewer run.
type ReviewerResult struct {
	// Content is the raw reviewer output (verdict + issues + notes).
	Content string
	// Verdict is the one-line verdict extracted from content.
	Verdict string
	// Issues lists concrete reviewer issues.
	Issues []string
	// Skipped is true when the reviewer was not invoked.
	Skipped bool
	// SkipReason explains why the reviewer was skipped.
	SkipReason string
	// Err is non-empty when the reviewer failed to complete.
	Err string
}

// ReviewerHandle is returned by LaunchReviewer. It holds the background task
// and provides a Wait method that blocks until the reviewer finishes.
type ReviewerHandle struct {
	task ReviewerTask
	// skipped captures a statically-skipped reviewer (not launched).
	skipped    bool
	skipReason string
}

// nopReviewerTask implements ReviewerTask for the "not launched" case.
type nopReviewerTask struct{ reason string }

func (n *nopReviewerTask) GetOutput() string                            { return "" }
func (n *nopReviewerTask) IsRunning() bool                             { return false }
func (n *nopReviewerTask) WaitForCompletion(_ time.Duration) bool      { return true }
func (n *nopReviewerTask) GetStatus() ReviewerTaskStatus               { return ReviewerTaskStatus{} }

// SkippedReviewer returns a ReviewerHandle for a reviewer that was not launched.
func SkippedReviewer(reason string) ReviewerHandle {
	return ReviewerHandle{
		task:       &nopReviewerTask{reason: reason},
		skipped:    true,
		skipReason: reason,
	}
}

// Wait blocks until the reviewer finishes and returns the result.
// maxWait bounds how long we wait (the reviewer runs concurrently with Batch
// checks; after maxWait we return whatever output has been collected).
func (h ReviewerHandle) Wait(maxWait time.Duration) ReviewerResult {
	if h.skipped {
		return ReviewerResult{Skipped: true, SkipReason: h.skipReason}
	}
	h.task.WaitForCompletion(maxWait)
	status := h.task.GetStatus()
	output := h.task.GetOutput()
	if output == "" {
		output = status.Output
	}
	res := ReviewerResult{Content: output}
	if status.Error != "" {
		res.Err = status.Error
	}
	res.Verdict, res.Issues = extractReviewerVerdict(output)
	return res
}

// LaunchReviewer starts the reviewer agent in the background.
// It returns immediately — the reviewer runs concurrently with Batch checks.
// Call ReviewerHandle.Wait to collect results.
//
// If exec is nil or reviewer is not needed, returns a SkippedReviewer.
func LaunchReviewer(ctx context.Context, exec SubagentExecutor, prompt string) ReviewerHandle {
	if exec == nil {
		return SkippedReviewer("no subagent executor configured")
	}
	task, err := exec.RunBackground(tool.AgentExecRequest{
		Agent:       reviewerAgentName,
		Prompt:      prompt,
		Description: "Finalize: speculative code review",
		Mode:        "explore",
	})
	if err != nil {
		return ReviewerHandle{
			task: &nopReviewerTask{reason: err.Error()},
			skipped:    true,
			skipReason: fmt.Sprintf("reviewer launch failed: %v", err),
		}
	}
	return ReviewerHandle{task: task}
}

// BuildDiff runs "git diff HEAD" in cwd and returns the diff content.
// When the diff exceeds reviewerDiffLimit bytes, it is truncated intelligently
// (preserving all diff --git headers and @@ hunk headers) and the full diff
// is stored as an artifact in outputDir.
//
// Returns:
//   - truncated (or full) diff string
//   - artifact path of the full diff (empty when diff fit inline)
//   - error
func BuildDiff(ctx context.Context, cwd, outputDir string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		// Not a git repo or no diff — return empty, not an error.
		return "", "", nil //nolint:nilerr
	}
	full := string(out)
	if len(full) <= reviewerDiffLimit {
		return full, "", nil
	}
	// Store the full diff as an artifact.
	var artifactRef string
	if outputDir != "" {
		path := filepath.Join(outputDir, "finalize-review-diff.patch")
		if writeErr := os.WriteFile(path, out, 0o644); writeErr == nil {
			artifactRef = "log:" + path
		}
	}
	truncated := truncateDiff(full, reviewerDiffLimit)
	return truncated, artifactRef, nil
}

// truncateDiff truncates a unified diff to maxBytes while preserving:
// - all "diff --git" file headers
// - all "@@" hunk headers
// - context lines around changes
//
// Strategy: collect all file headers and hunk headers first, then fill
// remaining budget with content lines from the beginning. This ensures
// files changed later in the diff are still visible to the reviewer.
func truncateDiff(diff string, maxBytes int) string {
	lines := strings.Split(diff, "\n")
	var headers []string
	var contentLines []string

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "+++ ") ||
			strings.HasPrefix(line, "@@") {
			headers = append(headers, line)
		} else {
			contentLines = append(contentLines, line)
		}
	}

	headerBlock := strings.Join(headers, "\n")
	if len(headerBlock) >= maxBytes {
		// Even headers exceed limit — just truncate to limit.
		if maxBytes > 50 {
			return headerBlock[:maxBytes-50] + "\n...(diff truncated)"
		}
		return "...(diff truncated)"
	}

	remaining := maxBytes - len(headerBlock) - 100 // reserve for truncation notice
	var content strings.Builder
	for _, line := range contentLines {
		if content.Len()+len(line)+1 > remaining {
			break
		}
		content.WriteString(line)
		content.WriteString("\n")
	}

	return headerBlock + "\n" + content.String() + "\n...(diff truncated; see artifact for full diff)"
}

// BuildReviewerPrompt constructs the compact prompt sent to the reviewer agent.
// The prompt includes the original task, the diff (possibly truncated), and
// the list of changed files.
func BuildReviewerPrompt(task, diff, artifactRef string, files []string) string {
	var sb strings.Builder

	sb.WriteString("Review the following implementation changes for correctness, bugs, and quality.\n\n")

	if task != "" {
		sb.WriteString("## Task\n")
		sb.WriteString(task)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Changed Files\n")
	for _, f := range files {
		sb.WriteString("  ")
		sb.WriteString(f)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	if diff != "" {
		sb.WriteString("## Diff\n```diff\n")
		sb.WriteString(diff)
		sb.WriteString("\n```\n")
		if artifactRef != "" {
			sb.WriteString("\nFull diff: ")
			sb.WriteString(artifactRef)
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\nRespond with verdict (approve / approve-with-notes / request-changes), ")
	sb.WriteString("concrete issues (severity: critical/major/minor), and optional notes.")

	return sb.String()
}

// extractReviewerVerdict extracts the verdict line and issues from raw reviewer
// output. This is a best-effort heuristic; the full content is always preserved.
func extractReviewerVerdict(content string) (verdict string, issues []string) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if verdict == "" {
			if strings.HasPrefix(lower, "verdict") ||
				strings.Contains(lower, "approve") ||
				strings.Contains(lower, "request-changes") ||
				strings.Contains(lower, "request changes") {
				verdict = strings.TrimSpace(line)
			}
		}
		// Collect numbered or bulleted issue lines.
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 2 {
			if (trimmed[0] == '-' || trimmed[0] == '*') && trimmed[1] == ' ' {
				issues = append(issues, trimmed[2:])
			}
		}
	}
	return
}

// buildStatDiff runs "git diff --stat HEAD" for a compact summary.
func buildStatDiff(ctx context.Context, cwd string) string {
	cmd := exec.CommandContext(ctx, "git", "diff", "--stat", "HEAD")
	cmd.Dir = cwd
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}
