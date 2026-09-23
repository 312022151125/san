package finalize

import (
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/san/internal/tool/batch"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// CheckState is the outcome state of a single verification check.
// Every check in a Result carries exactly one of these states.
type CheckState string

const (
	// StatePass — check ran and succeeded.
	StatePass CheckState = "PASS"
	// StateFailed — check ran and failed.
	StateFailed CheckState = "FAILED"
	// StateSkipped — check was not started because a dependency failed.
	StateSkipped CheckState = "SKIPPED"
	// StateCached — check result was reused from the session cache.
	// Reserved for Phase 2; present here so the type is defined from the start.
	StateCached CheckState = "CACHED"
	// StateNotRun — check was excluded (tier below threshold, reviewer off, etc.).
	StateNotRun CheckState = "NOT_RUN"
)

// CheckResult holds the result of a single verification check.
type CheckResult struct {
	// ID is the command ID from the BatchCommand (e.g. "test", "vet", "build").
	ID string
	// State is the outcome state.
	State CheckState
	// Duration is how long the check took (zero for SKIPPED/NOT_RUN).
	Duration time.Duration
	// Output is the compacted inline output (failures only).
	Output string
	// LogRef is the artifact reference for large output (e.g. "log:/path").
	LogRef string
	// SkipReason explains why the check was skipped.
	SkipReason string
}

// Result is the aggregated output of a Finalize pipeline run.
type Result struct {
	// Pass is true when all checks passed (or were NOT_RUN/SKIPPED by design).
	Pass bool
	// Checks contains the outcome of each verification step.
	Checks []CheckResult
	// ReviewerResult holds the reviewer's verdict and issues.
	Reviewer ReviewerResult
	// Tier is the verification tier that was used.
	Tier Tier
	// Duration is total wall-clock time from Run() start to finish.
	Duration time.Duration
	// FailureCount is the number of checks that returned StateFailed.
	FailureCount int
}

// String returns the compact output string sent back to the model.
// On PASS the output is minimal; on FAIL it contains only actionable information.
func (r Result) String() string {
	if r.Pass {
		return r.formatPass()
	}
	return r.formatFail()
}

func (r Result) formatPass() string {
	var sb strings.Builder
	sb.WriteString("Verification PASS\n")

	for _, c := range r.Checks {
		if c.State == StateNotRun {
			continue
		}
		sb.WriteString(fmt.Sprintf("  %-8s %s", c.ID+":", string(c.State)))
		if c.State == StateCached {
			sb.WriteString(" (cached)")
		}
		if c.Duration > 0 {
			sb.WriteString(fmt.Sprintf(" [%s]", toolresult.FormatDuration(c.Duration)))
		}
		sb.WriteString("\n")
	}

	if !r.Reviewer.Skipped && r.Reviewer.Err == "" {
		verdict := r.Reviewer.Verdict
		if verdict == "" {
			verdict = "approve"
		}
		sb.WriteString(fmt.Sprintf("  %-8s %s\n", "review:", verdict))
	}

	sb.WriteString(fmt.Sprintf("  %s\n", toolresult.FormatDuration(r.Duration)))
	return strings.TrimRight(sb.String(), "\n")
}

func (r Result) formatFail() string {
	var sb strings.Builder
	sb.WriteString("Verification FAILED\n\n")

	for _, c := range r.Checks {
		if c.State != StateFailed {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s FAILED\n", strings.ToUpper(c.ID)))
		if c.Output != "" {
			for _, line := range strings.Split(strings.TrimRight(c.Output, "\n"), "\n") {
				if line != "" {
					sb.WriteString("  ")
					sb.WriteString(line)
					sb.WriteString("\n")
				}
			}
		}
		if c.LogRef != "" {
			sb.WriteString(fmt.Sprintf("  Full log: %s\n", c.LogRef))
		}
		sb.WriteString("\n")
	}

	// PASS / SKIPPED summary for non-failing checks.
	var passing []string
	for _, c := range r.Checks {
		if c.State == StatePass || c.State == StateCached {
			passing = append(passing, c.ID)
		}
	}
	if len(passing) > 0 {
		for _, id := range passing {
			sb.WriteString(fmt.Sprintf("%-8s PASS\n", strings.ToUpper(id)+":"))
		}
	}

	// Reviewer section.
	if !r.Reviewer.Skipped {
		if r.Reviewer.Err != "" {
			sb.WriteString(fmt.Sprintf("Review: error (%s)\n", r.Reviewer.Err))
		} else {
			verdict := r.Reviewer.Verdict
			if verdict == "" && r.Reviewer.Content != "" {
				verdict = "see content"
			}
			sb.WriteString(fmt.Sprintf("Review: %s\n", verdict))
			for _, issue := range r.Reviewer.Issues {
				sb.WriteString(fmt.Sprintf("  %s\n", issue))
			}
		}
	}

	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("%d actionable failure(s).", r.FailureCount))
	return strings.TrimRight(sb.String(), "\n")
}

// Aggregate converts a *batch.BatchResult and a ReviewerResult into a Result.
// All states are explicit: no implicit mapping.
func Aggregate(batchRes *batch.BatchResult, reviewerRes ReviewerResult) Result {
	if batchRes == nil {
		batchRes = &batch.BatchResult{}
	}

	checks := make([]CheckResult, 0, len(batchRes.Commands))
	failureCount := 0
	allPass := true

	for _, cmd := range batchRes.Commands {
		cr := CheckResult{
			ID:       cmd.ID,
			Duration: cmd.Duration,
			Output:   cmd.Output,
			LogRef:   string(cmd.LogRef),
		}
		switch {
		case cmd.Skipped:
			cr.State = StateSkipped
			cr.SkipReason = cmd.SkipReason
		case cmd.Err != "" || cmd.ExitCode != 0:
			cr.State = StateFailed
			failureCount++
			allPass = false
		default:
			cr.State = StatePass
		}
		checks = append(checks, cr)
	}

	// Reviewer failure counts as actionable even if Batch passed.
	if !reviewerRes.Skipped && reviewerRes.Err == "" {
		verdict := strings.ToLower(reviewerRes.Verdict)
		if strings.Contains(verdict, "request-changes") || strings.Contains(verdict, "request changes") {
			allPass = false
		}
	}

	return Result{
		Pass:         allPass,
		Checks:       checks,
		Reviewer:     reviewerRes,
		Duration:     batchRes.Duration,
		FailureCount: failureCount,
	}
}
