package finalize

import (
	"context"
	"time"

	"github.com/genai-io/san/internal/tool/batch"
)

// reviewerWaitTimeout is the maximum time Pipeline.Run waits for the reviewer
// after Batch checks have completed. The reviewer runs concurrently so this
// budget is consumed in parallel with Batch execution; in practice this is the
// residual wait after Batch finishes.
const reviewerWaitTimeout = 3 * time.Minute

// Request is the input to Pipeline.Run. It contains only context that the
// pipeline itself cannot derive — it never contains model-supplied shell strings.
type Request struct {
	// CWD is the working directory for all verification commands.
	CWD string

	// Task is the original task description, injected into the reviewer prompt.
	// Optional — the reviewer still runs without it.
	Task string

	// AcceptanceCriteria is injected into the reviewer prompt when non-empty.
	AcceptanceCriteria string

	// ReviewerMode controls when the reviewer agent is invoked.
	// Valid values: "smart" (default), "always", "off".
	ReviewerMode string

	// Tier overrides automatic tier classification when non-zero.
	// When zero, the tier is derived from changed files.
	Tier Tier

	// TierOverride, when true, means the Tier field is used directly and
	// changed-file classification is skipped.
	TierOverride bool
}

// Pipeline is the Smart Parallel Finalize pipeline.
//
// It holds the two injected engines (batch executor + subagent executor) and
// the output directory for artifacts. The pipeline constructs all verification
// commands from its own trusted logic — no model-supplied shell strings flow
// through it.
//
// Thread safety: Pipeline is safe for concurrent calls to Run from different
// goroutines. Each Run call is independent.
type Pipeline struct {
	batchEngine   batch.Engine
	subagentExec  SubagentExecutor // may be nil; reviewer is skipped when nil
	outputDir     string
}

// NewPipeline creates a Pipeline with the provided engines.
// batchEngine must not be nil. subagentExec may be nil (reviewer skipped).
func NewPipeline(batchEngine batch.Engine, subagentExec SubagentExecutor, outputDir string) *Pipeline {
	return &Pipeline{
		batchEngine:  batchEngine,
		subagentExec: subagentExec,
		outputDir:    outputDir,
	}
}

// Run executes the finalization pipeline and returns an aggregated Result.
//
// Steps:
//  1. Detect changed files (git diff --name-only HEAD).
//  2. Extract affected Go packages.
//  3. Classify verification tier (unless overridden).
//  4. Build the verification DAG.
//  5. Launch reviewer concurrently (if needed).
//  6. Execute the Batch DAG.
//  7. Wait for reviewer.
//  8. Aggregate and return.
//
// On PASS the Result.String() is intentionally minimal.
// On FAIL it contains only actionable information.
func (p *Pipeline) Run(ctx context.Context, req Request) (Result, error) {
	start := time.Now()

	// 1. Detect changed files.
	files, err := DetectChangedFiles(ctx, req.CWD)
	if err != nil {
		// git not available or not a git repo — proceed with empty file list,
		// which forces FULL tier.
		files = nil
	}
	// Fallback: try unstaged when HEAD diff is empty (e.g. initial commit,
	// all changes staged but not committed).
	if len(files) == 0 {
		files, _ = DetectChangedFilesUnstaged(ctx, req.CWD)
	}

	// 2. Extract affected packages.
	packages := ExtractGoPackages(req.CWD, files)

	// 3. Classify tier.
	tier := req.Tier
	if !req.TierOverride {
		tier = ClassifyTier(files)
	}

	// 4. Build the verification DAG.
	dag := BuildDAG(tier, packages, req.CWD)

	// 5. Launch reviewer concurrently (before Batch starts).
	reviewerMode := req.ReviewerMode
	if reviewerMode == "" {
		reviewerMode = "smart"
	}
	var reviewerHandle ReviewerHandle
	if IsReviewerNeeded(files, reviewerMode) && p.subagentExec != nil {
		diff, artifactRef, _ := BuildDiff(ctx, req.CWD, p.outputDir)
		prompt := BuildReviewerPrompt(req.Task, diff, artifactRef, files)
		reviewerHandle = LaunchReviewer(ctx, p.subagentExec, prompt)
	} else {
		reason := "smart mode: trivial change"
		if reviewerMode == "off" {
			reason = "reviewer disabled"
		} else if p.subagentExec == nil {
			reason = "no subagent executor configured"
		}
		reviewerHandle = SkippedReviewer(reason)
	}

	// 6. Execute the Batch DAG.
	batchRes, batchErr := p.batchEngine.Execute(ctx, batch.EngineRequest{
		Commands:  dag,
		CWD:       req.CWD,
		OutputDir: p.outputDir,
	})
	if batchErr != nil {
		// Engine error (not a command failure) — return with error.
		return Result{}, batchErr
	}

	// 7. Wait for reviewer (uses remaining time budget after Batch finishes).
	reviewerRes := reviewerHandle.Wait(reviewerWaitTimeout)

	// 8. Aggregate.
	result := Aggregate(batchRes, reviewerRes)
	result.Tier = tier
	result.Duration = time.Since(start)

	return result, nil
}
