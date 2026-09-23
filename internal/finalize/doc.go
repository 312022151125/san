// Package finalize implements the Smart Parallel Finalization pipeline.
//
// After implementation is complete, calling Pipeline.Run replaces the
// sequential model loop:
//
//	test → model → vet → model → build → model → reviewer → model
//
// with a single model round trip:
//
//	model → Finalize → parallel{test, vet, build, reviewer} → aggregated result → model
//
// Architecture:
//
//	Pipeline.Run(ctx, Request)
//	     │
//	     ├─ DetectChangedFiles      (git diff --name-only HEAD)
//	     ├─ ExtractGoPackages       (map paths → ./pkg patterns)
//	     ├─ ClassifyTier            (FAST / STANDARD / FULL — no model call)
//	     ├─ BuildDAG                (BatchCommand graph via existing Batch engine)
//	     ├─ LaunchReviewer          (RunBackground — concurrent with DAG)
//	     ├─ Engine.Execute(dag)     (parallel wave execution)
//	     ├─ await reviewer result
//	     └─ Aggregate → Result
//
// Trust: Pipeline constructs all verification commands from known strategies.
// Model-supplied shell strings never flow through this package.
package finalize
