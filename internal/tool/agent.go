package tool

import (
	"context"
	"time"

	"github.com/genai-io/san/internal/core"
)

const (
	// IconAgent is the display icon for agent tool results.
	IconAgent = "a"
)

// PlanModeChecker reports whether the session is currently in Plan Mode.
// Inject this into tools that must refuse write-capable operations during planning.
type PlanModeChecker interface {
	IsPlanMode() bool
}

// messagesGetterKey is the context key for parent messages getter (used by fork).
type messagesGetterKey struct{}

// agentIDKey carries the broker address of the agent whose tools are running,
// so SendMessage can stamp the sender. Empty for the main conversation.
type agentIDKey struct{}

// WithAgentID marks the context with the running subagent's broker address.
func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentIDKey{}, id)
}

// AgentIDFromContext returns the running subagent's broker address, or "" when
// the caller is the main conversation.
func AgentIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(agentIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithMessagesGetter returns a context carrying a messages getter for fork support.
func WithMessagesGetter(ctx context.Context, getter MessagesGetter) context.Context {
	return context.WithValue(ctx, messagesGetterKey{}, getter)
}

// GetMessagesGetter returns the messages getter from context, if any.
func GetMessagesGetter(ctx context.Context) MessagesGetter {
	if g, ok := ctx.Value(messagesGetterKey{}).(MessagesGetter); ok {
		return g
	}
	return nil
}

// AgentExecutor is the interface for executing agents.
// This allows the Agent tool to be decoupled from the agent package.
type AgentExecutor interface {
	Run(ctx context.Context, req AgentExecRequest) (*AgentExecResult, error)
	RunBackground(req AgentExecRequest) (AgentTaskInfo, error)
	RunBatch(ctx context.Context, req AgentBatchRequest) (*AgentBatchResult, error)
	GetAgentConfig(name string) (AgentConfigInfo, bool)
	ResolveAgentSelection(name string) (AgentConfigInfo, any, bool)
	GetParentModelID() string
}

// ActivityFunc is called when the agent reports activity.
type ActivityFunc func(msg string)

// MessagesGetter returns the current parent conversation messages.
// Used by fork to inherit conversation context.
type MessagesGetter func() []core.Message

// AgentExecRequest contains parameters for agent execution.
type AgentExecRequest struct {
	Agent               string
	ResolvedAgentConfig any // exact approval-time configuration; never model-facing
	Prompt              string
	Description         string
	Background          bool
	Model               string
	MaxSteps            int
	Mode                string
	// TaskID is the background-task id of this run; the executor registers it
	// with the broker so main can message the subagent while it runs. Empty
	// for foreground runs.
	TaskID     string
	OnActivity ActivityFunc
	OnQuestion AskQuestionFunc
	// Depth tracks nesting level from the root session.
	// 0 = main session, 1 = direct batch child, 2 = grandchild batch, etc.
	// Used for logging and for enforcing spawn-depth limits in future phases.
	Depth int
}

// AgentYield is the structured compact result a batch child returns to its parent.
// It replaces the raw transcript/activity trail in parent context, keeping
// only what the parent needs to continue its work.
type AgentYield struct {
	Summary  string   // one-paragraph summary of what was done/found
	Findings []string // key discoveries (for explore/advisor agents)
	Files    []string // files created or modified (for worker agents)
	Changes  []string // brief descriptions of changes made
	Tests    []string // test outcomes
	Risks    []string // concerns or issues the parent should know about
}

// AgentExecResult contains the result of agent execution.
type AgentExecResult struct {
	AgentID           string
	AgentName         string
	OutputFile        string
	Model             string
	Success           bool
	Content           string
	StepCount         int
	ToolUses          int
	TotalInputTokens  int
	TotalOutputTokens int
	Duration          time.Duration
	Activity          []string
	Error             string
	// Yield is the structured compact result for batch children. Non-nil when
	// the agent was run as part of a batch and produced structured output.
	Yield *AgentYield
	// ResultRef is set when the full result was too large to inline and was
	// stored to a file. Format: "agent://<id>". The model can use this
	// reference to retrieve the full content if needed.
	ResultRef string
}

// AgentBatchItem is a single task in an Agent batch call.
type AgentBatchItem struct {
	// ID is pre-allocated before any agent starts so it is deterministic.
	ID   string
	// Name is the display label for this task (optional).
	Name string
	// Agent is the agent definition to use (e.g. "explore", "worker").
	Agent string
	// Task is the task-specific prompt for this item. The shared batch context
	// is prepended automatically; this should contain only the delta.
	Task string
	// Mode overrides the agent's configured permission mode.
	// Valid values: "explore", "edit", "default", "".
	Mode string
}

// AgentBatchRequest is the input to a batch Agent call.
// Context is shared across all tasks and sent once; each Task contains
// only its specific delta. This avoids duplicating goal/constraints/architecture
// into every item.
type AgentBatchRequest struct {
	// Context is shared information sent to all agents in the batch.
	// Include: goal, constraints, relevant architecture, shared contract.
	// Keep it concise — hundreds of tokens, not thousands.
	Context string
	// Tasks is the list of independent semantic tasks to execute concurrently.
	Tasks []AgentBatchItem
}

// AgentBatchResult is the result of a batch Agent call.
type AgentBatchResult struct {
	Results  []AgentExecResult
	Duration time.Duration
}

// AgentTaskInfo contains info about a background agent task.
type AgentTaskInfo struct {
	TaskID     string
	AgentName  string
	OutputFile string
}

// AgentConfigInfo contains agent configuration for display. It is the single
// projection of an agent definition, shared by the Agent tool (GetAgentConfig)
// and the TUI agent selector.
type AgentConfigInfo struct {
	Name           string
	Description    string
	Color          string
	Model          string
	PermissionMode string
	Tools          []string // nil = all tools
	SourceFile     string
	// Source indicates where the agent definition came from:
	// "user", "project", or a plugin scope. Empty defaults to project.
	Source string
}
