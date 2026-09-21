package llm

import (
	"context"
	"fmt"
	"net/http"
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
