package setting

import (
	"encoding/json"
	"testing"
)

func TestMemorySettingsDefaultsDisabled(t *testing.T) {
	var m MemorySettings
	if m.Enabled() {
		t.Fatal("zero MemorySettings must be disabled")
	}
	if got := m.ResolvedURL(); got != "http://localhost:8888" {
		t.Errorf("ResolvedURL() = %q, want Hindsight standard default", got)
	}
	if got := m.ResolvedScope(); got != MemoryScopeProject {
		t.Errorf("ResolvedScope() = %q, want %q", got, MemoryScopeProject)
	}
	if !m.AutoRecallOn() {
		t.Error("AutoRecallOn() must default to true")
	}
	if got := m.ResolvedMaxResults(); got != 5 {
		t.Errorf("ResolvedMaxResults() = %d, want 5", got)
	}
}

func TestMemorySettingsEnabledAndOverrides(t *testing.T) {
	off := false
	m := MemorySettings{
		Backend:    MemoryBackendHindsight,
		URL:        "http://memory:9999",
		Scope:      MemoryScopeGlobal,
		AutoRecall: &off,
		AutoRetain: true,
		MaxResults: 3,
	}
	if !m.Enabled() {
		t.Error("backend hindsight must enable the backend")
	}
	if got := m.ResolvedURL(); got != "http://memory:9999" {
		t.Errorf("ResolvedURL() = %q", got)
	}
	if got := m.ResolvedScope(); got != MemoryScopeGlobal {
		t.Errorf("ResolvedScope() = %q", got)
	}
	if m.AutoRecallOn() {
		t.Error("explicit false autoRecall must disable auto-recall")
	}
	if got := m.ResolvedMaxResults(); got != 3 {
		t.Errorf("ResolvedMaxResults() = %d, want 3", got)
	}
}

func TestMemorySettingsValidate(t *testing.T) {
	valid := []MemorySettings{
		{},
		{Backend: MemoryBackendOff},
		{Backend: MemoryBackendHindsight, Scope: MemoryScopeProject},
		{Backend: MemoryBackendHindsight, Scope: MemoryScopeGlobal, MaxResults: 5},
	}
	for _, m := range valid {
		if err := m.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", m, err)
		}
	}
	invalid := []MemorySettings{
		{Backend: "redis"},
		{Scope: "repo"},
		{MaxResults: -1},
	}
	for _, m := range invalid {
		if err := m.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want error", m)
		}
	}
}

func TestMergeMemoryKeepsBaseWhenOverlayUnset(t *testing.T) {
	base := MemorySettings{Backend: MemoryBackendHindsight, URL: "http://base:8888", MaxResults: 7}
	got := mergeMemory(base, MemorySettings{})
	if got.Backend != MemoryBackendHindsight || got.URL != "http://base:8888" || got.MaxResults != 7 {
		t.Errorf("merge dropped base memory config: %+v", got)
	}
}

func TestMergeMemoryOverlayWinsAndBools(t *testing.T) {
	on := true
	base := MemorySettings{Backend: MemoryBackendHindsight, AutoRecall: &on, AutoRetain: true}
	overlay := MemorySettings{Backend: MemoryBackendOff, AutoRetain: false}
	got := mergeMemory(base, overlay)
	if got.Backend != MemoryBackendOff {
		t.Errorf("overlay backend must win, got %q", got.Backend)
	}
	if !got.AutoRetain {
		t.Error("autoRetain must OR (enable-anywhere wins)")
	}
	if !got.AutoRecallOn() {
		t.Error("unset overlay autoRecall must keep base's explicit true")
	}
}

func TestCloneMemoryDeepCopiesAutoRecallPointer(t *testing.T) {
	on := true
	src := NewData()
	src.Memory = MemorySettings{Backend: MemoryBackendHindsight, AutoRecall: &on}
	src.SearchURL = "http://search:8080"
	src.SearchMaxResults = 5

	dst := src.Clone()
	if dst.Memory.AutoRecall == src.Memory.AutoRecall {
		t.Fatal("Clone must dup the AutoRecall pointer")
	}
	if dst.SearchURL != src.SearchURL || dst.SearchMaxResults != src.SearchMaxResults {
		t.Errorf("search fields not cloned: %+v", dst)
	}
	off := false
	*dst.Memory.AutoRecall = off
	if !*src.Memory.AutoRecall {
		t.Error("mutating the clone must not affect the source")
	}
}

func TestMemorySettingsJSONRoundTrip(t *testing.T) {
	raw := `{"memory":{"backend":"hindsight","url":"http://localhost:8888","scope":"project","autoRecall":true,"autoRetain":false,"maxResults":5},"searchUrl":"http://localhost:8080","searchMaxResults":5}`
	var d Data
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !d.Memory.Enabled() {
		t.Error("backend must parse as enabled")
	}
	if d.SearchURL != "http://localhost:8080" || d.SearchMaxResults != 5 {
		t.Errorf("search fields: url=%q max=%d", d.SearchURL, d.SearchMaxResults)
	}
	out, err := json.Marshal(&d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Data
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if back.Memory.Backend != MemoryBackendHindsight || back.SearchURL != d.SearchURL {
		t.Errorf("round trip mismatch: %+v", back.Memory)
	}
}
