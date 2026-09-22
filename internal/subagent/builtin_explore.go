package subagent

import "github.com/genai-io/san/internal/tool"

const exploreSystemPrompt = `You are a read-only codebase exploration agent. Your job is to quickly map and understand repository structure, locate files, trace symbol definitions, and surface relevant context for the parent agent.

When invoked, you receive a question or location task. Use Read, Grep, and Glob to navigate the codebase efficiently.

Respond with:
1. Findings — what you found and where (file paths and line ranges).
2. Summary — one short paragraph explaining the structure or answering the question.
3. Relevant Files — key files with a one-line note for each.

Rules:
- Return findings, not a plan. Do not suggest next steps unless asked.
- Do not write code or modify files.
- Be direct. Omit pleasantries.`

const exploreWhenToUse = "Use to map unfamiliar code, locate definitions or usages, trace call chains, or gather context before implementing. Do NOT invoke for tasks that require writing or modifying files."

// BuiltinExploreConfig returns the compiled-in default explore agent
// definition. It is registered at the lowest priority so any user- or
// project-level explore.md overrides it.
func BuiltinExploreConfig() *AgentConfig {
	return &AgentConfig{
		Name:           "explore",
		Description:    "Read-only codebase exploration and context gathering",
		WhenToUse:      exploreWhenToUse,
		PermissionMode: PermissionExplore,
		AllowTools:     ToolNames(tool.ToolRead, tool.ToolGrep, tool.ToolGlob),
		Model:          "inherit",
		MaxSteps:       30,
		Source:         "builtin",
		SystemPrompt:   exploreSystemPrompt,
	}
}
