package batch

import "github.com/genai-io/san/internal/core"

// Schema returns the model-facing tool definition for Batch.
// The schema is intentionally minimal (D1: Bash = primitive, Batch = orchestration).
// Keep it small to minimize schema token cost.
func (t *BatchTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Batch",
		Description: `Run multiple shell commands as a dependency graph in a single call.

Use Batch when you already know all the commands needed and they do not require model reasoning between them. Independent commands run in parallel; later commands wait for their depends_on dependencies. Failed dependencies cause their dependents to be skipped automatically.

Batch = deterministic execution graph. For semantic work requiring judgment between steps, use Agent instead.

Each command needs an id, a command string, and optional depends_on list referencing earlier ids. Up to 8 commands per call.

Examples:
  Build then test: build (no deps) → test (depends_on: [build])
  Parallel validation: fmt, test, vet all independent (no depends_on)`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"commands": map[string]any{
					"type":        "array",
					"description": "Commands to execute. Independent commands run in parallel. Max 8.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{
								"type":        "string",
								"description": "Unique identifier for this command within the batch. Referenced by depends_on.",
							},
							"command": map[string]any{
								"type":        "string",
								"description": "Shell command to execute (via bash -c).",
							},
							"depends_on": map[string]any{
								"type":        "array",
								"description": "IDs of commands that must succeed before this one runs. Omit for independent commands.",
								"items":       map[string]any{"type": "string"},
							},
							"timeout_ms": map[string]any{
								"type":        "number",
								"description": "Per-command timeout in milliseconds. Default 120000 (2 minutes).",
							},
						},
						"required": []string{"id", "command"},
					},
				},
			},
			"required": []string{"commands"},
		},
	}
}
