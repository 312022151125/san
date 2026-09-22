// Agent-model override helpers: intercept ProviderSelector results when a
// pending agent-model pick is in flight, and rewire the live executor after a
// new override is persisted.
package app

import (
	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/log"
)

// interceptAgentModelPick checks whether a providerModelSelectedMsg arrived
// while an agent-model pick was in flight (pendingAgentModelName != ""). If so,
// it routes the selection to the agents panel instead of the normal
// session-model switch path and returns (cmd, true). When nothing is pending it
// returns (nil, false) so the caller falls through to the normal handler.
func (m *model) interceptAgentModelPick(msg tea.Msg) (tea.Cmd, bool) {
	if m.pendingAgentModelName == "" {
		return nil, false
	}

	modelID, providerName, ok := input.ExtractProviderModelSelected(msg)
	if !ok {
		return nil, false
	}

	agentName := m.pendingAgentModelName
	m.pendingAgentModelName = ""

	// Build the model ref as "provider/model" (same convention the executor uses).
	modelRef := providerName + "/" + modelID

	// Delegate to the agents panel to persist and emit AgentModelSavedMsg.
	cmd, _ := m.userInput.Config.ApplyAgentModelSave(agentName, modelRef)

	// Close the ProviderSelector — the selection was consumed by the agent
	// override path; the session model must not change.
	m.userInput.Provider.Selector.Cancel()

	if cmd == nil {
		log.Logger().Warn("agent model save returned no command",
			zap.String("agent", agentName), zap.String("model", modelRef))
	}
	return cmd, true
}

// rewireAgentModelOverride propagates a freshly persisted agent model override
// (or its clearance) to the live executor by rebuilding the agent tool via
// ReconfigureAgentTool, which re-reads the current settings snapshot. This is
// safe to call at any time; it is a no-op when no provider is connected.
func (m *model) rewireAgentModelOverride(_ string, _ string) {
	// ReconfigureAgentTool rebuilds the executor from scratch using the current
	// settings snapshot, picking up whatever UpdateAgentModelAt just wrote.
	m.ReconfigureAgentTool()
}
