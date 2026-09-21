package llm

import (
	"context"
	"errors"
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
	if got := e.headers.Get("x-opencode-client"); got != "cli" {
		t.Errorf("x-opencode-client = %q, want \"cli\"", got)
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

// TestZenIsFreeTierError checks the pattern that classifies the free-tier gate
// denial so it is not mistaken for a credential failure.
func TestZenIsFreeTierError(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		// Canonical gateway response shape.
		{`{"error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode"}}`, true},
		// Just the marker type without the full sentence.
		{`403 request rejected (type=FreeTierError)`, true},
		// The sentence without the type name.
		{`{"message":"free tier can only be used from within OpenCode"}`, true},
		// A different 403: genuine auth failure, must not match.
		{`{"error":{"type":"AuthError","message":"invalid api key"}}`, false},
		// A paid-SKU access denial must also not match.
		{`{"error":{"message":"access to this model is denied for your plan"}}`, false},
	}
	for _, tc := range cases {
		if got := isZenFreeTierError(tc.body); got != tc.want {
			t.Errorf("isZenFreeTierError(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// TestZenErrorKindFreeTierIsInvalidRequest verifies that a 403 carrying the
// free-tier gate body is KindInvalidRequest (client policy), not KindAuth
// (bad credential). A genuine 403 without the marker stays KindAuth.
func TestZenErrorKindFreeTierIsInvalidRequest(t *testing.T) {
	freeTierBody := `{"error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode"}}`
	if got := zenErrorKind(http.StatusForbidden, freeTierBody); got != ai.KindInvalidRequest {
		t.Errorf("zenErrorKind(403, freeTierBody) = %q, want KindInvalidRequest", got)
	}
	// A bare FreeTierError marker also qualifies.
	if got := zenErrorKind(http.StatusForbidden, "403 FreeTierError: denied"); got != ai.KindInvalidRequest {
		t.Errorf("zenErrorKind(403, bare marker) = %q, want KindInvalidRequest", got)
	}
	// A genuine credential 403 must still be KindAuth.
	if got := zenErrorKind(http.StatusForbidden, `{"error":{"message":"invalid api key"}}`); got != ai.KindAuth {
		t.Errorf("zenErrorKind(403, credential body) = %q, want KindAuth", got)
	}
	// 401 is always KindAuth regardless of body.
	if got := zenErrorKind(http.StatusUnauthorized, freeTierBody); got != ai.KindAuth {
		t.Errorf("zenErrorKind(401, freeTierBody) = %q, want KindAuth", got)
	}
}

// TestZenFreeTierErrorMessageIsHelpful verifies that zenModels rewrites a
// free-tier 403 body into an actionable explanation rather than surfacing the
// raw gateway JSON, and that the "FreeTierError" marker survives in the
// rewritten message for downstream pattern matching.
//
// Note: ListModels has a fallback path that suppresses the error when the
// vendor seed is present (by design — a transient listing failure should not
// block the picker). The test exercises zenModels directly as the Fetch hook
// so the rewriting logic is tested in isolation.
func TestZenFreeTierErrorMessageIsHelpful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	p := openZen(t, server.URL)
	// Call zenModels directly with the configured endpoint (the *sdkprovider.Provider),
	// as the SDK's Refresh hook does. This tests the error-message rewriting
	// without going through ListModels' fallback, which suppresses listing errors.
	_, err := zenModels(context.Background(), p.endpoint)
	if err == nil {
		t.Fatal("expected an error from the free-tier 403, got nil")
	}
	msg := err.Error()
	// The rewritten message must mention the key is valid and explain the restriction.
	if !strings.Contains(msg, "API key is valid") {
		t.Errorf("error message does not mention key validity: %v", msg)
	}
	// The FreeTierError marker must survive for downstream isZenFreeTierError calls.
	if !strings.Contains(msg, "FreeTierError") {
		t.Errorf("error message drops the FreeTierError marker needed for downstream classification: %v", msg)
	}
	// It must not be classified as KindAuth.
	var aiErr *ai.Error
	if errors.As(err, &aiErr) && aiErr.Kind == ai.KindAuth {
		t.Errorf("free-tier 403 classified as KindAuth, want KindInvalidRequest")
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

// TestZenInferSetsCompatWithAPI verifies that zenInfer sets the Compat type
// that matches each model's API, so the SDK's checkCompat() validation never
// sees the "compat mismatch" error that surfaced for muse-spark-1.3-contributor-free
// and any other Responses/Anthropic model the vendor default would have stamped
// with OpenAIChatCompat.
func TestZenInferSetsCompatWithAPI(t *testing.T) {
	cases := []struct {
		id        string
		wantAPI   ai.API
		wantCompat any
	}{
		{"gpt-5.5", ai.APIOpenAIResponses, ai.OpenAIResponsesCompat{}},
		{"muse-spark-1.3-contributor-free", ai.APIOpenAIResponses, ai.OpenAIResponsesCompat{}},
		{"grok-4.5", ai.APIOpenAIResponses, ai.OpenAIResponsesCompat{}},
		{"claude-sonnet-5", ai.APIAnthropicMessages, ai.AnthropicCompat{}},
		{"qwen3.7-plus", ai.APIAnthropicMessages, ai.AnthropicCompat{}},
		{"glm-5.1", ai.APIOpenAIChat, ai.OpenAIChatCompat{}},
		{"deepseek-v4-pro", ai.APIOpenAIChat, ai.OpenAIChatCompat{}},
		{"kimi-k2.5", ai.APIOpenAIChat, ai.OpenAIChatCompat{}},
		// Unknown families pass through unchanged (vendor defaults remain).
		{"jev-1.13", ai.APIOpenAIChat, ai.OpenAIChatCompat{}},
	}
	for _, tc := range cases {
		// Simulate decorate(): stamp vendor defaults then call Infer.
		m := ai.Model{ID: tc.id, API: ai.APIOpenAIChat, Compat: ai.OpenAIChatCompat{}}
		m = zenInfer(m)
		if m.API != tc.wantAPI {
			t.Errorf("zenInfer(%q).API = %q, want %q", tc.id, m.API, tc.wantAPI)
		}
		if m.Compat != tc.wantCompat {
			t.Errorf("zenInfer(%q).Compat = %T(%v), want %T(%v)", tc.id, m.Compat, m.Compat, tc.wantCompat, tc.wantCompat)
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
