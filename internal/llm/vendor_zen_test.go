package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/san/internal/core"
)

// sessionIDPattern matches the canonical ses_<12 hex><14 alphanum> format
// the OpenCode Zen gateway requires for x-opencode-session.
var sessionIDPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// openZen points the opencode-zen entry at the stub the way production opens
// it: through the table's own factory, so the test covers the row as shipped.
func openZen(t *testing.T, url string) *vendorProvider {
	t.Helper()
	t.Setenv("OPENCODE_ZEN_API_KEY", "zen-test-key")
	t.Setenv("OPENCODE_ZEN_BASE_URL", url)
	for _, e := range vendorEntries {
		if e.meta.Provider != OpenCodeZen {
			continue
		}
		p, err := e.factory()(context.Background())
		if err != nil {
			t.Fatalf("open zen: %v", err)
		}
		vp, ok := p.(*vendorProvider)
		if !ok {
			t.Fatalf("zen factory built %T, want *vendorProvider", p)
		}
		return vp
	}
	t.Fatal("no opencode-zen entry")
	return nil
}

func TestZenTurnSendsIdentityAndSurfacesErrors(t *testing.T) {
	e := serve(t,
		[2]string{"message", `{"id":"c1","model":"glm-5.1","choices":[{"index":0,"delta":{"content":"hi"}}]}`},
		[2]string{"message", "[DONE]"},
	)
	p := openZen(t, e.server.URL)

	text, _, _, err := collect(t, p, CompletionOptions{
		Model: zenModel, Messages: []core.Message{core.UserMessage("hi", nil)},
	})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if text != "hi" {
		t.Fatalf("text = %q, want %q", text, "hi")
	}
	if got := e.headers.Get("Authorization"); got != "Bearer zen-test-key" {
		t.Errorf("Authorization = %q, want Bearer zen-test-key", got)
	}
	if got := e.headers.Get("User-Agent"); got != zenUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, zenUserAgent)
	}
	if got := e.headers.Get("x-opencode-session"); !sessionIDPattern.MatchString(got) {
		t.Errorf("x-opencode-session = %q, want ses_<12 hex><14 alphanum>", got)
	}
	if !strings.Contains(fmt.Sprint(e.body), zenModel) {
		t.Errorf("request body %v names no model", e.body)
	}

	e.status = http.StatusForbidden
	e.error = `{"error":{"message":"zen quota exhausted","type":"forbidden"}}`
	_, _, _, err = collect(t, p, CompletionOptions{
		Model: zenModel, Messages: []core.Message{core.UserMessage("hi", nil)},
	})
	if err == nil {
		t.Fatal("a 403 produced no error")
	}
	if !strings.Contains(err.Error(), "zen quota exhausted") {
		t.Errorf("error = %v, want the upstream body verbatim", err)
	}
}

func TestZenNewSessionID(t *testing.T) {
	id := zenNewSessionID()
	if !sessionIDPattern.MatchString(id) {
		t.Fatalf("zenNewSessionID = %q, want ses_<12 hex><14 alphanum>", id)
	}
	// Two calls must not collide.
	if zenNewSessionID() == id {
		t.Fatal("zenNewSessionID returned the same ID twice — entropy problem")
	}
}

// TestZenAPIForModelFamily verifies the family→protocol mapping used both by
// zenModels (listing filter) and zenVendor's Infer hook (inference routing).
// Gemini is intentionally excluded: its per-model endpoint URL cannot be
// derived from the shared base URL that the Google GenAI driver expects.
func TestZenAPIForModelFamily(t *testing.T) {
	cases := []struct {
		id   string
		want ai.API
	}{
		{"gpt-5.5", ai.APIOpenAIResponses},
		{"gpt-5.5-mini", ai.APIOpenAIResponses},
		{"grok-4.5", ai.APIOpenAIResponses},
		{"muse-spark-1.3", ai.APIOpenAIResponses},
		{"muse-spark-1.3-contributor", ai.APIOpenAIResponses},
		{"claude-sonnet-5", ai.APIAnthropicMessages},
		{"claude-opus-4", ai.APIAnthropicMessages},
		{"qwen3.7-plus", ai.APIAnthropicMessages},
		{"qwen3.7-max", ai.APIAnthropicMessages},
		{"glm-5.1", ai.APIOpenAIChat},
		{"deepseek-v4-pro", ai.APIOpenAIChat},
		{"kimi-k2.5", ai.APIOpenAIChat},
		{"minimax-m3", ai.APIOpenAIChat},
		{"big-pickle", ai.APIOpenAIChat},
		// gemini-* excluded: per-model URL, not routable from a shared base.
		{"gemini-3-flash", ai.API("")},
		{"gemini-3-pro", ai.API("")},
		{"jev-1.13", ai.API("")},
	}
	for _, tc := range cases {
		if got := zenAPIForModel(tc.id); got != tc.want {
			t.Errorf("zenAPIForModel(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

// TestZenListModelsKeepsKnownFamilies checks that zenModels retains models from
// all three routable protocol families and drops entries whose family is unknown
// or excluded (gemini, jev, etc.).
//
// Protocol correctness (each model's API field) is asserted via the vendor's
// Infer hook through p.endpoint.Models(), which is the path inference uses.
func TestZenListModelsKeepsKnownFamilies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"object": "list",
			"data": [
				{"id": "gpt-5.5"},
				{"id": "muse-spark-1.3"},
				{"id": "grok-4.5"},
				{"id": "claude-sonnet-5"},
				{"id": "qwen3.7-plus"},
				{"id": "glm-5.1"},
				{"id": "deepseek-v4-pro"},
				{"id": "kimi-k2.5"},
				{"id": "gemini-3-flash"},
				{"id": "jev-1.13"}
			]
		}`)
	}))
	t.Cleanup(server.Close)

	p := openZen(t, server.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	// gemini and jev are excluded; the rest must appear.
	want := map[string]ai.API{
		"gpt-5.5":         ai.APIOpenAIResponses,
		"muse-spark-1.3":  ai.APIOpenAIResponses,
		"grok-4.5":        ai.APIOpenAIResponses,
		"claude-sonnet-5": ai.APIAnthropicMessages,
		"qwen3.7-plus":    ai.APIAnthropicMessages,
		"glm-5.1":         ai.APIOpenAIChat,
		"deepseek-v4-pro": ai.APIOpenAIChat,
		"kimi-k2.5":       ai.APIOpenAIChat,
	}

	ids := make(map[string]bool, len(models))
	for _, m := range models {
		ids[m.ID] = true
	}
	for id := range want {
		if !ids[id] {
			t.Errorf("ListModels missing %q", id)
		}
	}
	if ids["jev-1.13"] {
		t.Errorf("ListModels kept jev-1.13, unknown families must be skipped")
	}
	if ids["gemini-3-flash"] {
		t.Errorf("ListModels kept gemini-3-flash, gemini is excluded from the shared-base routing")
	}

	// Protocol routing: p.endpoint.Models() goes through the vendor's Infer
	// hook, which is the same path Client() uses for inference routing.
	live := make(map[string]ai.API)
	for _, m := range p.endpoint.Models() {
		live[m.ID] = m.API
	}
	for id, w := range want {
		got, ok := live[id]
		if !ok {
			t.Errorf("endpoint models missing %q", id)
			continue
		}
		if got != w {
			t.Errorf("model %q API = %q, want %q", id, got, w)
		}
	}
	if _, ok := live["jev-1.13"]; ok {
		t.Errorf("endpoint kept jev-1.13, unknown families must be skipped")
	}
	if _, ok := live["gemini-3-flash"]; ok {
		t.Errorf("endpoint kept gemini-3-flash, gemini excluded from shared-base routing")
	}
}

func TestZenListModelsFallbackOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `internal error`)
	}))
	t.Cleanup(server.Close)

	p := openZen(t, server.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels fallback: %v", err)
	}
	if len(models) != 1 || models[0].ID != zenModel {
		t.Errorf("got %v, want fallback [%s]", models, zenModel)
	}
}
