package subagent

import "github.com/genai-io/san/internal/tool"

const advisorSystemPrompt = `You are a read-only reasoning advisor. Your job is to help the parent agent or worker make difficult decisions — not to implement solutions.

When invoked, you receive a question or decision point plus relevant context (files, findings, implementation options). Use Read, Grep, and Glob to gather additional repository context when needed. WebSearch covers external references (docs, libraries, APIs); recall/reflect query long-term project memory when it is enabled — both are background evidence, not authority.

Respond with this structure:
1. Recommendation — one or two sentences stating the preferred approach.
2. Reasoning — concise explanation of trade-offs and why this approach wins.
3. Risks / Caveats — correctness, concurrency, security, or design risks.
4. Relevant Files — (optional) key files and a brief note for each.

Rules:
- Return a concise recommendation, not a long transcript.
- Do not write code or modify files.
- Be direct. Omit pleasantries.`

const advisorWhenToUse = "Use when architectural trade-offs exist between multiple approaches; the implementation is uncertain; repeated attempts have failed; concurrency, security, or correctness implications are non-trivial; or the user explicitly requests a second opinion. Do NOT invoke for simple edits or routine tasks."

// BuiltinAdvisorConfig returns the compiled-in default advisor agent
// definition. It is registered at the lowest priority so any user- or
// project-level advisor.md overrides it.
func BuiltinAdvisorConfig() *AgentConfig {
	return &AgentConfig{
		Name:           "advisor",
		Description:    "Read-only reasoning consultant for difficult decisions",
		WhenToUse:      advisorWhenToUse,
		PermissionMode: PermissionExplore,
		// WebSearch is a built-in (always resolvable). recall/reflect resolve
		// only when the Hindsight backend is on and their schemas are injected
		// as ExtraTools — an absent schema simply never matches this list, so
		// the entries are free when memory is off. retain is deliberately
		// absent (plus parent-only): advisors read memory, they never write it.
		AllowTools: ToolNames(tool.ToolRead, tool.ToolGrep, tool.ToolGlob,
			tool.ToolWebSearch, tool.ToolRecall, tool.ToolReflect),
		Model:          "inherit",
		MaxSteps:       30,
		Source:         "builtin",
		SystemPrompt:   advisorSystemPrompt,
	}
}
