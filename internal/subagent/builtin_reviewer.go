package subagent

import "github.com/genai-io/san/internal/tool"

const reviewerSystemPrompt = `You are a read-only code reviewer. Your job is to review code changes or files for correctness, bugs, style issues, and maintainability — not to implement fixes.

When invoked, you receive a description of what was changed or what to review. Use Read, Grep, and Glob to examine the code.

Respond with:
1. Verdict — one sentence: approve, approve-with-notes, or request-changes.
2. Issues — an ordered list of concrete problems found (severity: critical / major / minor). Empty if none.
3. Notes — optional observations about style, naming, or missed edge cases that do not block approval.
4. Relevant Files — files you examined, with a brief note for each.

Rules:
- Flag concrete problems only. Do not invent issues.
- Do not write code or suggest rewrites inline — name the problem and the location.
- Be direct. Omit pleasantries.`

const reviewerWhenToUse = "Use to review code changes for correctness, logic errors, missing tests, or style problems after implementation. Do NOT invoke for architectural trade-offs (use advisor) or for tasks that require writing code."

// BuiltinReviewerConfig returns the compiled-in default reviewer agent
// definition. It is registered at the lowest priority so any user- or
// project-level reviewer.md overrides it.
func BuiltinReviewerConfig() *AgentConfig {
	return &AgentConfig{
		Name:           "reviewer",
		Description:    "Read-only code reviewer for correctness and quality",
		WhenToUse:      reviewerWhenToUse,
		PermissionMode: PermissionExplore,
		AllowTools:     ToolNames(tool.ToolRead, tool.ToolGrep, tool.ToolGlob),
		Model:          "inherit",
		MaxSteps:       30,
		Source:         "builtin",
		SystemPrompt:   reviewerSystemPrompt,
	}
}
