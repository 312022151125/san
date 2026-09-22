package input

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/setting"
)

// key runs a keypress against the panel: k is the key string form the
// handler switches on ("enter", "down", "left", ...).
func memKey(p *memoryPanel, k string) (tea.Cmd, bool) {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		msg = tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		msg = tea.KeyPressMsg{Code: tea.KeyRight}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEsc}
	default:
		msg = tea.KeyPressMsg{Text: k}
	}
	return p.HandleKey(msg)
}

// readUserMemorySettings loads the user-level settings file written by the
// panel (HOME isolated by the caller).
func readUserMemorySettings(t *testing.T, home string) (setting.MemorySettings, string, int) {
	t.Helper()
	path := filepath.Join(home, ".san", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var raw struct {
		Memory           setting.MemorySettings `json:"memory"`
		SearchURL        string                 `json:"searchUrl"`
		SearchMaxResults int                    `json:"searchMaxResults"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return raw.Memory, raw.SearchURL, raw.SearchMaxResults
}

// TestMemoryPanelEnterSaveEmits drives the panel: flip the backend to
// Hindsight, edit the server, save, and confirm the user settings file
// carries the block plus a MemorySavedMsg.
func TestMemoryPanelEnterSaveEmits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	p := newMemoryPanel(nil)
	p.Enter()

	// Row 0 is the MEMORY section header (not editable); the cursor parks on
	// the first editable row — "Off". Move down to "Hindsight" and apply.
	if p.cursor == 0 {
		t.Fatalf("cursor should park on an editable row, got 0 (%q)", p.rows[0].label)
	}
	memKey(p, "down") // Off → Hindsight
	if p.rows[p.cursor].label != "Hindsight" {
		t.Fatalf("cursor on %q, want Hindsight", p.rows[p.cursor].label)
	}
	cmd, _ := memKey(p, "enter")
	if cmd != nil {
		t.Fatal("radio apply must not emit a command")
	}
	if !p.Dirty() {
		t.Fatal("applying a different backend must mark the panel dirty")
	}

	// Find the first memText row (Server), edit it.
	serverIdx := -1
	for i, r := range p.rows {
		if r.kind == memText {
			serverIdx = i
			break
		}
	}
	if serverIdx < 0 {
		t.Fatal("no text row in panel")
	}
	p.cursor = serverIdx
	memKey(p, "enter") // start editing
	if !p.editing {
		t.Fatal("enter on a text row must start editing")
	}
	for _, ch := range "http://memory:8888" {
		memKey(p, string(ch))
	}
	memKey(p, "enter") // commit
	if p.editing {
		t.Fatal("enter must end editing")
	}

	// Save: find the save row.
	saveIdx := -1
	for i, r := range p.rows {
		if r.kind == memSave {
			saveIdx = i
		}
	}
	if saveIdx < 0 {
		t.Fatal("no save row")
	}
	p.cursor = saveIdx
	cmd, done := memKey(p, "enter")
	if !done || cmd == nil {
		t.Fatalf("save must emit a command and dismiss (done=%v cmd=%v)", done, cmd != nil)
	}
	msg, ok := cmd().(MemorySavedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want MemorySavedMsg", cmd())
	}
	if msg.Scope != "user" {
		t.Errorf("scope = %q, want user", msg.Scope)
	}
	if p.Dirty() {
		t.Error("save must advance the baseline")
	}

	mem, searchURL, maxResults := readUserMemorySettings(t, home)
	if mem.Backend != setting.MemoryBackendHindsight {
		t.Errorf("backend = %q, want hindsight", mem.Backend)
	}
	if mem.URL != "http://memory:8888" {
		t.Errorf("url = %q", mem.URL)
	}
	if mem.Scope != setting.MemoryScopeProject {
		t.Errorf("scope = %q, want project default", mem.Scope)
	}
	if !mem.AutoRecallOn() {
		t.Error("autoRecall should default on")
	}
	if mem.AutoRetain {
		t.Error("autoRetain should default off")
	}
	if mem.MaxResults != 5 {
		t.Errorf("maxResults = %d, want 5", mem.MaxResults)
	}
	// Web-search block untouched by default.
	if searchURL != "" || maxResults != 0 {
		t.Errorf("search block = (%q, %d), want zero", searchURL, maxResults)
	}
}

// TestMemoryPanelOffIsZeroCostOnDisk confirms the default save writes a
// backend-off memory block (the explicit disabled value), so a user who
// opened the panel and saved without changes gets the documented default.
func TestMemoryPanelOffIsDefault(t *testing.T) {
	p := newMemoryPanel(nil)
	p.Enter()
	if p.snap.backend != setting.MemoryBackendOff {
		t.Errorf("default backend = %q, want off", p.snap.backend)
	}
	if p.Dirty() {
		t.Error("fresh panel must not be dirty")
	}
	if p.snap.autoRecall != true {
		t.Error("fresh panel must default auto-recall on")
	}
	if p.snap.autoRetain {
		t.Error("fresh panel must default auto-retain off")
	}
	if p.snap.maxResults != 5 {
		t.Errorf("default maxResults = %d, want 5", p.snap.maxResults)
	}
}

// TestMemoryPanelScopeToggle confirms ←→ flips the save target and the save
// message reports it.
func TestMemoryPanelScopeToggle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	p := newMemoryPanel(nil)
	p.Enter()
	if p.scope != "user" {
		t.Fatalf("initial scope = %q", p.scope)
	}
	memKey(p, "right")
	if p.scope != "project" {
		t.Fatalf("scope after right = %q, want project", p.scope)
	}

	saveIdx := 0
	for i, r := range p.rows {
		if r.kind == memSave {
			saveIdx = i
		}
	}
	p.cursor = saveIdx
	cmd, done := memKey(p, "enter")
	if !done || cmd == nil {
		t.Fatal("save must succeed")
	}
	if msg := cmd().(MemorySavedMsg); msg.Scope != "project" {
		t.Errorf("saved scope = %q, want project", msg.Scope)
	}
}

// TestMemoryPanelValidationRejectsBadBackend confirms an unknown backend
// cannot be saved (inline error, popup stays).
func TestMemoryPanelValidationRejectsBadBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	p := newMemoryPanel(nil)
	p.Enter()
	p.snap.backend = "redis" // corrupt the working buffer directly
	saveIdx := 0
	for i, r := range p.rows {
		if r.kind == memSave {
			saveIdx = i
		}
	}
	p.cursor = saveIdx
	_, done := memKey(p, "enter")
	if done {
		t.Fatal("invalid backend must not save/dismiss")
	}
	if p.saveErr == nil {
		t.Fatal("invalid backend must surface an inline error")
	}
}
