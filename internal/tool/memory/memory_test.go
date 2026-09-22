package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hindsight "github.com/genai-io/san/internal/memory"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool"
)

// hindsightReset clears the cached backend between tests.
func hindsightReset() { hindsight.Reset() }

// withMemorySettings installs a Settings service carrying cfg for the test
// and tears it down (the hindsight backend gate reads setting.DefaultIfInit).
func withMemorySettings(t *testing.T, cfg setting.MemorySettings) {
	t.Helper()
	prev := setting.DefaultIfInit()
	setting.SetDefaultSettings(setting.New(&setting.Data{Memory: cfg}))
	t.Cleanup(func() {
		setting.ResetDefaultSettings()
		if prev != nil {
			setting.SetDefaultSettings(prev)
		}
		hindsightReset()
	})
}

func TestSchemasNilWhenBackendOff(t *testing.T) {
	withMemorySettings(t, setting.MemorySettings{}) // backend off
	if got := Schemas(); got != nil {
		t.Fatalf("Schemas() with memory off = %v, want nil (zero schema cost)", got)
	}
	if BackendEnabled() {
		t.Fatal("BackendEnabled() must be false when backend is off")
	}
}

func TestSchemasPresentWhenBackendOn(t *testing.T) {
	withMemorySettings(t, setting.MemorySettings{Backend: setting.MemoryBackendHindsight})
	schemas := Schemas()
	if len(schemas) != 3 {
		t.Fatalf("len(Schemas()) = %d, want 3", len(schemas))
	}
	names := map[string]bool{}
	for _, s := range schemas {
		names[s.Name] = true
	}
	for _, want := range []string{tool.ToolRetain, tool.ToolRecall, tool.ToolReflect} {
		if !names[want] {
			t.Errorf("schema %q missing", want)
		}
	}
}

func TestExecuteRefusesWhenBackendOff(t *testing.T) {
	withMemorySettings(t, setting.MemorySettings{})
	cwd := t.TempDir()

	cases := []struct {
		tool   tool.Tool
		params map[string]any
	}{
		{&RecallTool{}, map[string]any{"query": "q"}},
		{&RetainTool{}, map[string]any{"items": []any{map[string]any{"content": "c"}}}},
		{&ReflectTool{}, map[string]any{"query": "q"}},
	}
	for _, tc := range cases {
		res := tc.tool.Execute(context.Background(), tc.params, cwd)
		if res.Success {
			t.Errorf("%s.Execute with memory off succeeded, want error", tc.tool.Name())
		}
	}
}

func TestRecallFormatsBulletsAndCaps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"text": "hashline edits are atomic", "type": "experience", "mentioned_at": "2026-01-02T10:00:00Z"},
				{"text": "tests run with GOCACHE", "type": "world", "mentioned_at": "2026-01-03T10:00:00Z"},
				{"text": "third memory that must be cut", "type": "world"},
			},
		})
	}))
	defer srv.Close()

	withMemorySettings(t, setting.MemorySettings{
		Backend:    setting.MemoryBackendHindsight,
		URL:        srv.URL,
		MaxResults: 2,
	})
	// Environment URL would override settings; ensure it is unset for the test.
	t.Setenv("HINDSIGHT_API_URL", "")

	// Get() caches — clear any state a prior test left behind.
	hindsightReset()

	res := (&RecallTool{}).Execute(context.Background(), map[string]any{"query": "how are edits done"}, t.TempDir())
	if !res.Success {
		t.Fatalf("Recall failed: %s", res.Output)
	}
	out := res.Output
	for _, want := range []string{"Found 2 relevant memories", "- hashline edits are atomic [experience] (2026-01-02)", "- tests run with GOCACHE [world] (2026-01-03)"} {
		if !contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if contains(out, "third memory") {
		t.Errorf("maxResults cap not applied:\n%s", out)
	}
}

func TestRetainValidatesItems(t *testing.T) {
	withMemorySettings(t, setting.MemorySettings{Backend: setting.MemoryBackendHindsight, URL: "http://127.0.0.1:1"})
	t.Setenv("HINDSIGHT_API_URL", "")
	hindsightReset()

	cases := []map[string]any{
		{},                              // missing items
		{"items": []any{}},              // empty
		{"items": []any{"not-object"}},  // wrong shape
		{"items": []any{map[string]any{"content": "  "}}}, // blank content
	}
	for i, params := range cases {
		res := (&RetainTool{}).Execute(context.Background(), params, t.TempDir())
		if res.Success {
			t.Errorf("case %d: retain accepted invalid items %+v", i, params)
		}
	}
}

func TestRetainAndReflectUnreachableServerError(t *testing.T) {
	withMemorySettings(t, setting.MemorySettings{Backend: setting.MemoryBackendHindsight, URL: "http://127.0.0.1:1"})
	t.Setenv("HINDSIGHT_API_URL", "")
	hindsightReset()

	// Connection refused must surface as a clean tool error, never a panic
	// and never a "success".
	res := (&RetainTool{}).Execute(context.Background(),
		map[string]any{"items": []any{map[string]any{"content": "durable fact"}}}, t.TempDir())
	if res.Success {
		t.Error("retain must fail when the Hindsight server is down")
	}
	res = (&ReflectTool{}).Execute(context.Background(), map[string]any{"query": "q"}, t.TempDir())
	if res.Success {
		t.Error("reflect must fail when the Hindsight server is down")
	}
}

func TestRetainIsParentOnly(t *testing.T) {
	if !tool.IsParentOnlyTool(tool.ToolRetain) {
		t.Error("retain must be parent-only (subagents never write memory)")
	}
	if tool.IsParentOnlyTool(tool.ToolRecall) || tool.IsParentOnlyTool(tool.ToolReflect) {
		t.Error("recall/reflect are read-only and must stay available to allowed subagents")
	}
}

// contains wraps strings.Contains for assertions.
func contains(s, sub string) bool { return strings.Contains(s, sub) }
