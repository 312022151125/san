package subagent

import "github.com/genai-io/san/internal/tool"

const workerSystemPrompt = `You are a focused implementation agent. Your job is to execute a clearly scoped coding task — write, edit, or refactor code as instructed.

When invoked, you receive a task description and relevant context. Implement the task directly using the available tools.

Rules:
- Implement exactly what is asked. Do not add features, refactors, or abstractions beyond the stated task.
- Every changed line should trace directly to the request.
- Prefer editing existing files over rewriting them.
- Run the relevant validation (build, lint, tests) after making changes.
- Do not ask clarifying questions mid-task. If the task is ambiguous, make the minimal reasonable assumption and note it in your result.
- Be concise in status output. Report what changed and any validation results.`

const workerWhenToUse = "Use to implement a clearly scoped coding task: write a function, fix a bug, add a field, update a test. The task must have a well-defined outcome. Do NOT invoke for open-ended exploration or architectural decisions."

// BuiltinWorkerConfig returns the compiled-in default worker agent
// definition. It is registered at the lowest priority so any user- or
// project-level worker.md overrides it.
func BuiltinWorkerConfig() *AgentConfig {
	return &AgentConfig{
		Name:           "worker",
		Description:    "Focused coding implementation agent",
		WhenToUse:      workerWhenToUse,
		PermissionMode: PermissionAcceptEdits,
		AllowTools:     ToolNames(tool.ToolRead, tool.ToolGrep, tool.ToolGlob, tool.ToolEdit, tool.ToolWrite, tool.ToolBash),
		Model:          "inherit",
		MaxSteps:       50,
		Source:         "builtin",
		SystemPrompt:   workerSystemPrompt,
	}
}
