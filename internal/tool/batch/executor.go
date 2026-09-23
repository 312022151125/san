// Package batch — this file separates the execution engine from the tool
// surface so that internal/finalize can call the same DAG runner directly
// without routing through the model-facing BatchTool (which requires user
// permission approval).
//
// Architecture:
//
//	BatchTool.Execute()  →  permission check  →  Engine.Execute()
//	FinalizeTool             (no permission)  →  Engine.Execute()
//
// Both callers share the same wave scheduler, concurrency semaphore, timeout,
// cancellation, and artifact-storage logic. No duplication.
//
// Trust boundary: only FinalizePipeline constructs the commands that flow
// through Engine.Execute from the finalize path. Model-supplied shell strings
// must always go through BatchTool (with permission check).
package batch

import "context"

// EngineRequest is the input to an Engine.Execute call.
// It is intentionally separate from the model-facing params map so the engine
// surface stays typed and auditable.
type EngineRequest struct {
	Commands  []BatchCommand
	CWD       string
	OutputDir string
}

// Engine executes a deterministic command DAG.
// Both BatchTool and FinalizePipeline use this interface.
type Engine interface {
	Execute(ctx context.Context, req EngineRequest) (*BatchResult, error)
}

// concreteEngine is the single implementation. It wraps the executeBatch
// function that was previously inlined in BatchTool.
type concreteEngine struct{}

// NewEngine returns the canonical Engine implementation.
// All callers (BatchTool, FinalizePipeline) must obtain their engine through
// this constructor so they share the same execution semantics.
func NewEngine() Engine {
	return &concreteEngine{}
}

// Execute runs the command DAG and returns combined results.
// It delegates directly to executeBatch, which owns wave scheduling,
// concurrency, timeout, cancellation, and artifact storage.
func (e *concreteEngine) Execute(ctx context.Context, req EngineRequest) (*BatchResult, error) {
	return executeBatch(ctx, req.Commands, req.CWD, req.OutputDir)
}
