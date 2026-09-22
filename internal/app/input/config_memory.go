// /config Memory panel: the optional Hindsight memory backend plus the two
// URL-based web-search keys, edited as one flat list under two section
// headings.
//
//   MEMORY      — Backend (Off / Hindsight), Server, Scope (Project /
//                 Global), Auto Recall (On / Off), Auto Retain (On / Off),
//                 Max Results.
//   WEB SEARCH  — Server (settings searchUrl, read by the SearXNG provider),
//                 Max Results (global WebSearch cap). The provider itself is
//                 chosen in the existing /search selector — noted here, not
//                 duplicated.
//
// Defaults: memory backend off, so a fresh install shows a panel that
// changes nothing. Every field is read per operation by the consumers (tool
// Execute, auto-recall gate, provider endpoint()), so a save affects the
// next operation with no global reset.
//
// Scope: ←→ flips user / project like the self-learning form; Save writes
// only the blocks this panel owns (memory, searchUrl/searchMaxResults),
// leaving every other setting in the file untouched (UpdateMemoryAt /
// UpdateSearchAt semantics).
package input

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/setting"
)

// MemorySavedMsg is emitted after the memory panel persists its blocks so
// the app can refresh its settings handle and show a confirmation. Values
// are already on disk when this fires.
type MemorySavedMsg struct {
	Scope string
}

// memSnap is the panel's working buffer — all fields comparable so Dirty is
// an ==.
type memSnap struct {
	backend          string  // "off" | "hindsight"
	url              string
	scope            string  // "project" | "global"
	autoRecall       bool
	autoRetain       bool
	maxResults       int
	searchURL        string
	searchMaxResults int
}

// memRowKind discriminates the row shapes this panel renders.
type memRowKind int

const (
	memSection memRowKind = iota
	memRadio   // one option of a group: enter applies it
	memText    // inline-edited free text (enter)
	memInt     // inline-edited number (enter)
	memNote    // static muted hint (not editable, not selectable)
	memSave
)

// memRow is one renderable row; fields unused by the kind stay zero.
type memRow struct {
	kind  memRowKind
	label string
	desc  string

	// radio
	apply func(*memSnap)
	match func(memSnap) bool

	// text
	strGet      func(memSnap) string
	strSet      func(*memSnap, string)
	placeholder string

	// int
	intGet func(memSnap) int
	intSet func(*memSnap, int)
	intMin int
	intMax int
}

// memEditable reports whether the row can hold the cursor.
func (r memRow) memEditable() bool {
	switch r.kind {
	case memRadio, memText, memInt, memSave:
		return true
	default:
		return false
	}
}

// findMemEditable is findEditable for memRow lists (navigation helper).
func findMemEditable(rows []memRow, start, step, fallback int) int {
	for i := start; i >= 0 && i < len(rows); i += step {
		if rows[i].memEditable() {
			return i
		}
	}
	return fallback
}

type memoryPanel struct {
	settings *setting.Settings

	rows   []memRow
	cursor int

	snap     memSnap
	baseline memSnap
	scope    string // "user" | "project"

	editing       bool
	editingBuffer string

	saveErr error
}

func newMemoryPanel(settings *setting.Settings) *memoryPanel {
	return &memoryPanel{settings: settings}
}

func (p *memoryPanel) Title() string { return "memory" }

func (p *memoryPanel) Enter() {
	p.editing = false
	p.editingBuffer = ""
	p.scope = "user"
	p.snap = memSnap{
		backend:    setting.MemoryBackendOff,
		scope:      setting.MemoryScopeProject,
		autoRecall: true,
		maxResults: 5,
	}
	if p.settings != nil {
		if data := p.settings.Snapshot(); data != nil {
			m := data.Memory
			p.snap.backend = m.Backend
			if p.snap.backend == "" {
				p.snap.backend = setting.MemoryBackendOff
			}
			p.snap.url = m.URL
			p.snap.scope = m.ResolvedScope()
			p.snap.autoRecall = m.AutoRecallOn()
			p.snap.autoRetain = m.AutoRetain
			p.snap.maxResults = m.ResolvedMaxResults()
			p.snap.searchURL = data.SearchURL
			p.snap.searchMaxResults = data.SearchMaxResults
		}
	}
	p.baseline = p.snap
	p.rows = p.buildRows()
	p.cursor = findMemEditable(p.rows, 0, +1, 0)
	p.saveErr = nil
}

func (p *memoryPanel) Dirty() bool { return p.snap != p.baseline }

func (p *memoryPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if p.editing {
		return p.handleEditingKey(msg), false
	}
	rows := p.rows
	if p.cursor >= len(rows) {
		p.cursor = findMemEditable(rows, 0, +1, 0)
	}
	switch msg.String() {
	case "up", "k":
		p.cursor = findMemEditable(rows, p.cursor-1, -1, p.cursor)
		p.saveErr = nil
	case "down", "j":
		p.cursor = findMemEditable(rows, p.cursor+1, +1, p.cursor)
		p.saveErr = nil
	case "left", "right":
		if p.scope == "user" {
			p.scope = "project"
		} else {
			p.scope = "user"
		}
	case "enter":
		row := rows[p.cursor]
		switch row.kind {
		case memRadio:
			row.apply(&p.snap)
			p.saveErr = nil
		case memText:
			p.editing = true
			p.editingBuffer = row.strGet(p.snap)
		case memInt:
			p.editing = true
			p.editingBuffer = strconv.Itoa(row.intGet(p.snap))
		case memSave:
			return p.save()
		}
	}
	return nil, false
}

// save validates the memory block and persists both blocks at the selected
// scope. A validation failure keeps the popup open with the inline error.
func (p *memoryPanel) save() (tea.Cmd, bool) {
	cfg := setting.MemorySettings{
		Backend:    p.snap.backend,
		URL:        strings.TrimSpace(p.snap.url),
		Scope:      p.snap.scope,
		AutoRecall: &p.snap.autoRecall,
		AutoRetain: p.snap.autoRetain,
		MaxResults: p.snap.maxResults,
	}
	if err := cfg.Validate(); err != nil {
		p.saveErr = err
		return nil, false
	}
	userLevel := p.scope == "user"
	if err := setting.UpdateMemoryAt(cfg, userLevel); err != nil {
		p.saveErr = err
		return nil, false
	}
	if err := setting.UpdateSearchAt(p.snap.searchURL, p.snap.searchMaxResults, userLevel); err != nil {
		p.saveErr = err
		return nil, false
	}
	p.baseline = p.snap
	scope := p.scope
	return func() tea.Msg { return MemorySavedMsg{Scope: scope} }, true
}

func (p *memoryPanel) handleEditingKey(msg tea.KeyMsg) tea.Cmd {
	row := p.rows[p.cursor]
	switch msg.String() {
	case "esc":
		p.editing = false
		p.editingBuffer = ""
	case "enter":
		p.commitEdit(row)
		p.editing = false
		p.editingBuffer = ""
		p.saveErr = nil
	case "backspace":
		if r := []rune(p.editingBuffer); len(r) > 0 {
			p.editingBuffer = string(r[:len(r)-1])
		}
	default:
		t := msg.Key().Text
		switch row.kind {
		case memInt:
			if len(t) == 1 && t[0] >= '0' && t[0] <= '9' && len(p.editingBuffer) < 3 {
				p.editingBuffer += t
			}
		case memText:
			if t != "" && len(p.editingBuffer) < 512 {
				p.editingBuffer += t
			}
		}
	}
	return nil
}

func (p *memoryPanel) commitEdit(row memRow) {
	switch row.kind {
	case memInt:
		if v, err := strconv.Atoi(p.editingBuffer); err == nil {
			row.intSet(&p.snap, min(max(v, row.intMin), row.intMax))
		}
	case memText:
		row.strSet(&p.snap, strings.TrimSpace(p.editingBuffer))
	}
}

func (p *memoryPanel) HintLine() string {
	return keycap("↑↓") + " navigate  " + keycap("enter") + " apply/edit  " +
		keycap("←→") + " scope  " + keycap("esc") + " close"
}

func (p *memoryPanel) Render(width int, _ int) string {
	var b strings.Builder
	b.WriteString(p.renderScopeControl())
	b.WriteString("\n\n")

	prevSection := ""
	for i, row := range p.rows {
		switch row.kind {
		case memSection:
			if prevSection != "" {
				b.WriteString("\n")
			}
			b.WriteString(renderAppearanceSection(row.label, width))
			b.WriteString("\n\n")
			prevSection = row.label
		case memRadio:
			b.WriteString(renderRadioRow(row.label, row.desc, i == p.cursor, row.match(p.snap)))
			b.WriteString("\n")
		case memText:
			b.WriteString(p.renderTextRow(i, row))
			b.WriteString("\n")
		case memInt:
			b.WriteString(p.renderIntRow(i, row))
			b.WriteString("\n")
		case memNote:
			b.WriteString("  " + selflearnMutedStyle.Render(row.desc))
			b.WriteString("\n")
		case memSave:
			style := selflearnSaveButtonStyle
			b.WriteString("  " + style.Render("Save") +
				selflearnMutedStyle.Render("  or " + keycap("esc") + selflearnMutedStyle.Render(" to discard")))
			b.WriteString("\n")
		}
	}

	if p.saveErr != nil {
		b.WriteString("\n")
		b.WriteString(selflearnErrorStyle.Render("⚠ "+p.saveErr.Error()))
		b.WriteString("\n")
	}
	return b.String()
}

// renderScopeControl mirrors the self-learning form's user/project pills.
func (p *memoryPanel) renderScopeControl() string {
	seg := func(name string) string {
		if p.scope == name {
			return selflearnScopeActiveStyle.Render(name)
		}
		return selflearnScopeIdleStyle.Render(name)
	}
	return seg("user") + selflearnMutedStyle.Render(" · ") + seg("project")
}

func (p *memoryPanel) renderTextRow(i int, row memRow) string {
	caret := "  "
	label := row.label
	value := row.strGet(p.snap)
	if i == p.cursor {
		caret = selflearnCursorStyle.Render("▸ ")
		label = selflearnCursorStyle.Render(label)
		if p.editing {
			value = p.editingBuffer + "▌"
		} else if value == "" && row.placeholder != "" {
			value = selflearnMutedStyle.Render(row.placeholder)
		}
	}
	pad := strings.Repeat(" ", max(14-lipgloss.Width(row.label), 1))
	return "  " + caret + label + pad + value
}

func (p *memoryPanel) renderIntRow(i int, row memRow) string {
	caret := "  "
	label := row.label
	value := strconv.Itoa(row.intGet(p.snap))
	if i == p.cursor {
		caret = selflearnCursorStyle.Render("▸ ")
		label = selflearnCursorStyle.Render(label)
		if p.editing {
			value = p.editingBuffer + "▌"
		}
	}
	pad := strings.Repeat(" ", max(14-lipgloss.Width(row.label), 1))
	return "  " + caret + label + pad + valueChip(value)
}

// buildRows is the full row list, in display order.
func (p *memoryPanel) buildRows() []memRow {
	backendRow := func(label, backend, desc string) memRow {
		return memRow{
			kind: memRadio, label: label, desc: desc,
			apply: func(s *memSnap) { s.backend = backend },
			match: func(s memSnap) bool { return s.backend == backend },
		}
	}
	scopeRow := func(label, scope, desc string) memRow {
		return memRow{
			kind: memRadio, label: label, desc: desc,
			apply: func(s *memSnap) { s.scope = scope },
			match: func(s memSnap) bool { return s.scope == scope },
		}
	}
	boolRow := func(label, desc string, get func(memSnap) bool, set func(*memSnap, bool)) memRow {
		return memRow{
			kind: memRadio, label: label, desc: desc,
			apply: func(s *memSnap) { set(s, !get(*s)) },
			match: func(s memSnap) bool { return get(s) },
		}
	}

	return []memRow{
		{kind: memSection, label: "MEMORY"},
		backendRow("Off", setting.MemoryBackendOff, "no long-term memory — zero overhead"),
		backendRow("Hindsight", setting.MemoryBackendHindsight, "remote Hindsight server (long-term memory)"),
		{kind: memText, label: "Server", placeholder: "http://localhost:8888",
			strGet: func(s memSnap) string { return s.url },
			strSet: func(s *memSnap, v string) { s.url = v }},
		{kind: memSection, label: "SCOPE"},
		scopeRow("Project", setting.MemoryScopeProject, "one memory bank per repository"),
		scopeRow("Global", setting.MemoryScopeGlobal, "one shared bank across repositories"),
		{kind: memSection, label: "AUTOMATIC"},
		boolRow("Auto Recall", "recall relevant memories once per task",
			func(s memSnap) bool { return s.autoRecall },
			func(s *memSnap, v bool) { s.autoRecall = v }),
		boolRow("Auto Retain", "store a consolidated task digest at turn end",
			func(s memSnap) bool { return s.autoRetain },
			func(s *memSnap, v bool) { s.autoRetain = v }),
		{kind: memInt, label: "Max Results", intMin: 1, intMax: 50,
			intGet: func(s memSnap) int { return s.maxResults },
			intSet: func(s *memSnap, v int) { s.maxResults = v }},
		{kind: memSection, label: "WEB SEARCH"},
		{kind: memNote, desc: "provider is chosen with /search (Exa, Tavily, Serper, Brave, SearXNG)"},
		{kind: memText, label: "Server", placeholder: "SearXNG endpoint, e.g. http://localhost:8080",
			strGet: func(s memSnap) string { return s.searchURL },
			strSet: func(s *memSnap, v string) { s.searchURL = v }},
		{kind: memInt, label: "Max Results", intMin: 0, intMax: 50,
			intGet: func(s memSnap) int { return s.searchMaxResults },
			intSet: func(s *memSnap, v int) { s.searchMaxResults = v }},
		{kind: memSave},
	}
}
