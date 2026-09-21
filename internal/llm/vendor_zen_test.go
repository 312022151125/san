package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
)

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

func TestZenListModelsFiltersChatCompletions(t *testing.T) {
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"object": "list",
			"data": [
				{"id": "claude-sonnet-4"},
				{"id": "gemini-3.8-flash"},
				{"id": "gpt-5.5"},
				{"id": "qwen3.8-flash"},
				{"id": "jev-1.13"},
				{"id": "deepseek-v4.1-flash"},
				{"id": "glm-5.1"},
				{"id": "minimax-m3"},
				{"id": "kimi-k3"},
				{"id": "big-pickle"},
				{"id": "mimo-v2.5-free"},
				{"id": "ling-3.0-flash-fin-free"},
				{"id": "nemotron-3-ultra-free"}
			]
		}`)
	}))
	t.Cleanup(server.Close)

	p := openZen(t, server.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if got := seenHeaders.Get("Authorization"); got != "Bearer zen-test-key" {
		t.Errorf("Authorization = %q, want Bearer zen-test-key", got)
	}
	if got := seenHeaders.Get("User-Agent"); got != zenUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, zenUserAgent)
	}

	gotIDs := make([]string, len(models))
	for i, m := range models {
		gotIDs[i] = m.ID
	}
	wantIDs := []string{
		"glm-5.1",
		"deepseek-v4.1-flash",
		"minimax-m3",
		"kimi-k3",
		"big-pickle",
		"mimo-v2.5-free",
		"ling-3.0-flash-fin-free",
		"nemotron-3-ultra-free",
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("got models %v, want %v", gotIDs, wantIDs)
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
