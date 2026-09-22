// /config Agents panel: shows all registered agents and their per-agent model
// overrides. Selecting a row opens the existing ProviderSelector so the user
// can pick any connected model (or choose "Inherit parent model" to clear the
// override). Changes are persisted immediately to user-level settings.json via
// setting.UpdateAgentModelAt and reported to the app via AgentModelSavedMsg so
// the live executor can pick up the new model without a restart.
package input

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/setting"
)

// AgentModelPickingMsg is emitted when the user presses Enter on an agent row.
// The app receives it, stores the agent name, and opens the ProviderSelector
// so the selection routes back through AgentModelSavedMsg instead of the
// normal session-model path.
type AgentModelPickingMsg struct {
	AgentName string
}

// AgentModelSavedMsg is emitted after an agent model override is persisted
// (including a clear). The app uses it to call executor.SetModelOverride so
// newly spawned subagents pick up the change immediately.
type AgentModelSavedMsg struct {
	AgentName string
	Model     string // "" = cleared (inherit)
}

// agentModelRow is one row in the agents panel.
type agentModelRow struct {
	name        string // agent name (display case)
	lowerName   string // canonical key
	configModel string // model stored in settings ("" = inherit)
}

// agentsPanel implements Panel for the /config Agents tab.
type agentsPanel struct {
	registry      AgentRegistry
	settings      *setting.Settings
	parentModelID func() string // returns live session model for "Inherit → X" label

	rows   []agentModelRow
	cursor int

	saveErr error // last persist error; cleared on navigation
}

func newAgentsPanel(reg AgentRegistry, settings *setting.Settings, parentModelID func() string) *agentsPanel {
	return &agentsPanel{
		registry:      reg,
		settings:      settings,
		parentModelID: parentModelID,
	}
}

func (p *agentsPanel) Title() string { return "agents" }

// Enter reloads the agent list and their current model overrides from settings.
func (p *agentsPanel) Enter() {
	p.saveErr = nil

	var snap *setting.Data
	if p.settings != nil {
		snap = p.settings.Snapshot()
	}

	if p.registry == nil {
		p.rows = nil
		return
	}
	configs := p.registry.ListConfigs()
	p.rows = make([]agentModelRow, 0, len(configs))
	for _, cfg := range configs {
		lower := strings.ToLower(cfg.Name)
		var stored string
		if snap != nil {
			stored = snap.Agents.GetAgentModel(lower)
		}
		p.rows = append(p.rows, agentModelRow{
			name:        cfg.Name,
			lowerName:   lower,
			configModel: stored,
		})
	}

	// Sort alphabetically so the list is stable regardless of registry order.
	for i := 1; i < len(p.rows); i++ {
		for j := i; j > 0 && p.rows[j].lowerName < p.rows[j-1].lowerName; j-- {
			p.rows[j], p.rows[j-1] = p.rows[j-1], p.rows[j]
		}
	}

	if p.cursor >= len(p.rows) {
		p.cursor = 0
	}
}

// Dirty is always false — saves happen immediately on selection, no working
// buffer. The shell's "● unsaved" indicator never shows for this panel.
func (p *agentsPanel) Dirty() bool { return false }

func (p *agentsPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if len(p.rows) == 0 {
		return nil, false
	}
	switch msg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
		p.saveErr = nil
	case "down", "j":
		if p.cursor < len(p.rows)-1 {
			p.cursor++
		}
		p.saveErr = nil
	case "enter":
		// Ask the app to open the ProviderSelector for this agent.
		row := p.rows[p.cursor]
		return func() tea.Msg {
			return AgentModelPickingMsg{AgentName: row.name}
		}, false // keep panel open; ProviderSelector takes over

	case "d", "delete":
		// Clear the override immediately.
		return p.clearOverride(p.rows[p.cursor])
	}
	return nil, false
}

// clearOverride removes the model override for a row and persists immediately.
func (p *agentsPanel) clearOverride(row agentModelRow) (tea.Cmd, bool) {
	if err := setting.UpdateAgentModelAt(row.lowerName, "", true); err != nil {
		p.saveErr = err
		return nil, false
	}
	// Update the in-memory row too so the render is instant.
	for i := range p.rows {
		if p.rows[i].lowerName == row.lowerName {
			p.rows[i].configModel = ""
			break
		}
	}
	name := row.name
	return func() tea.Msg {
		return AgentModelSavedMsg{AgentName: name, Model: ""}
	}, false
}

// ApplyModelSave is called by the app after the ProviderSelector returns a
// model for an agent. It persists and updates the in-memory row.
func (p *agentsPanel) ApplyModelSave(agentName, model string) (tea.Cmd, bool) {
	lower := strings.ToLower(agentName)
	if err := setting.UpdateAgentModelAt(lower, model, true); err != nil {
		p.saveErr = err
		return nil, false
	}
	for i := range p.rows {
		if p.rows[i].lowerName == lower {
			p.rows[i].configModel = model
			break
		}
	}
	return func() tea.Msg {
		return AgentModelSavedMsg{AgentName: agentName, Model: model}
	}, false
}

func (p *agentsPanel) HintLine() string {
	if len(p.rows) == 0 {
		return ""
	}
	return keycap("↑↓") + " navigate  " + keycap("enter") + " pick model  " + keycap("d") + " clear"
}

func (p *agentsPanel) Render(width, _ int) string {
	if len(p.rows) == 0 {
		return agentsPanelMutedStyle.Render("No agents registered.")
	}

	// Compute name column width.
	nameColW := 8
	for _, row := range p.rows {
		if w := lipgloss.Width(row.name); w > nameColW {
			nameColW = w
		}
	}
	nameColW = min(nameColW+2, 28)

	// Effective parent model for "Inherit → X" label.
	parentModel := ""
	if p.parentModelID != nil {
		parentModel = p.parentModelID()
	}

	var b strings.Builder
	for i, row := range p.rows {
		hovered := i == p.cursor

		caret := "  "
		name := agentsPanelNameStyle.Render(row.name)
		if hovered {
			caret = agentsPanelCursorStyle.Render("▸ ")
			name = agentsPanelCursorStyle.Render(row.name)
		}

		// Pad name to column width.
		pad := max(nameColW-lipgloss.Width(row.name), 1)
		paddedName := name + strings.Repeat(" ", pad)

		// Model label.
		var modelLabel string
		if row.configModel != "" {
			modelLabel = agentsPanelModelStyle.Render(row.configModel) + "  " + agentsPanelCurrentStyle.Render("●")
		} else {
			if parentModel != "" {
				modelLabel = agentsPanelInheritStyle.Render("Inherit → " + parentModel)
			} else {
				modelLabel = agentsPanelInheritStyle.Render("Inherit")
			}
		}

		b.WriteString(caret + paddedName + modelLabel + "\n")
	}

	if p.saveErr != nil {
		b.WriteString("\n")
		b.WriteString(agentsPanelErrorStyle.Render("⚠ couldn't save: " + p.saveErr.Error()))
		b.WriteString("\n")
	}
	_ = width
	return b.String()
}

var (
	agentsPanelCursorStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Accent).Bold(true)
	agentsPanelNameStyle    = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text)
	agentsPanelModelStyle   = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text)
	agentsPanelInheritStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	agentsPanelCurrentStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)
	agentsPanelMutedStyle   = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	agentsPanelErrorStyle   = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
)
