package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"
)

// zenFreeTierGatePattern matches the two forms OpenCode's gateway uses to
// signal a free-tier identity denial. The API key is valid — paid models on
// the same key keep serving — so the error is client policy, not a bad
// credential, and must not be classified as KindAuth.
var zenFreeTierGatePattern = regexp.MustCompile(`(?i)free tier can only be used from within|\bFreeTierError\b`)

// isZenFreeTierError reports whether the response body string from a Zen 403
// is the free-tier gate denial rather than a genuine credential failure.
func isZenFreeTierError(body string) bool {
	return zenFreeTierGatePattern.MatchString(body)
}

// The ChatGPT subscription backend publishes its lineup at its own catalog
// endpoint rather than through the Responses protocol's model listing: the
// request carries the Codex client version, and the answer is keyed under
// "models" with a slug where the protocol would put an id. Asking it the
// protocol's own question gets a 401.
//
// It is a listing, not a protocol, which is why it lives here rather than in
// the SDK's driver — provider.Config.Fetch is the seam for exactly this.

const (
	// codexClientVersion is the Codex CLI release this client presents itself
	// as. The backend returns the lineup for the version it is told; bump it
	// if the list ever goes stale.
	codexClientVersion = "0.154.0"

	// codexListTimeout bounds the catalog request, so a slow endpoint delays
	// the model picker rather than wedging it.
	codexListTimeout = 8 * time.Second
)

// codexModels reads the model lineup this subscription is entitled to.
func codexModels(ctx context.Context, p *sdkprovider.Provider) ([]ai.Model, error) {
	cfg := p.ConfigFor(ai.Model{ID: "-", API: ai.APIOpenAIResponses})
	client := cfg.HTTPClient
	if client == nil {
		return nil, fmt.Errorf("llm: the ChatGPT subscription endpoint needs a signed-in client")
	}

	ctx, cancel := context.WithTimeout(ctx, codexListTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL()+"/models?client_version="+codexClientVersion, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for name, value := range cfg.MergedHeaders() {
		req.Header.Set(name, value)
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, &ai.Error{
			Driver: "openai-responses", Kind: codexErrorKind(res.StatusCode), Status: res.StatusCode,
			Message: "the ChatGPT subscription catalog declined: " + string(body),
		}
	}

	var listing struct {
		Models []struct {
			Slug                     string `json:"slug"`
			DisplayName              string `json:"display_name"`
			ContextWindow            int    `json:"context_window"`
			ShowInPicker             *bool  `json:"show_in_picker"`
			DefaultReasoningLevel    string `json:"default_reasoning_level"`
			SupportedReasoningLevels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil, err
	}

	models := make([]ai.Model, 0, len(listing.Models))
	for _, m := range listing.Models {
		// A null show_in_picker means shown; only an explicit false hides one.
		if m.Slug == "" || (m.ShowInPicker != nil && !*m.ShowInPicker) {
			continue
		}
		model := ai.Model{
			ID:            m.Slug,
			Name:          m.DisplayName,
			API:           ai.APIOpenAIResponses,
			ContextWindow: m.ContextWindow,
		}
		for _, level := range m.SupportedReasoningLevels {
			if level.Effort == "" {
				continue
			}
			model.Reasoning = append(model.Reasoning, ai.ReasoningLevel{
				Effort:  ai.Effort(level.Effort),
				Value:   level.Effort,
				Default: level.Effort == m.DefaultReasoningLevel,
			})
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("llm: the ChatGPT subscription catalog listed no models")
	}
	return models, nil
}

// codexErrorKind classifies the catalog endpoint's refusal. A rejected
// credential must surface: connecting verifies the account by listing models,
// so a signed-out one recorded as connected would fail on the first real turn.
func codexErrorKind(status int) ai.ErrorKind {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ai.KindAuth
	case status == http.StatusTooManyRequests:
		return ai.KindRateLimit
	case status >= 500:
		return ai.KindOverloaded
	default:
		return ai.KindInvalidRequest
	}
}

const zenListTimeout = 8 * time.Second

// zenModels fetches the live model listing from OpenCode Zen and tags each
// entry with the protocol its family speaks. Entries from no known family
// are skipped, never defaulted to chat.
// ponytail: probe ID "-" borrows vendor-level URL/auth like codexModels; the
// model ID is irrelevant to ConfigFor here, so no zenModel dependency.
func zenModels(ctx context.Context, p *sdkprovider.Provider) ([]ai.Model, error) {
	cfg := p.ConfigFor(ai.Model{ID: "-", API: ai.APIOpenAIChat})
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	ctx, cancel := context.WithTimeout(ctx, zenListTimeout)
	defer cancel()

	url := strings.TrimRight(cfg.URL(), "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	for name, value := range cfg.MergedHeaders() {
		req.Header.Set(name, value)
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		bodyStr := string(body)
		kind := zenErrorKind(res.StatusCode, bodyStr)
		msg := "the OpenCode Zen catalog declined: " + bodyStr
		if res.StatusCode == http.StatusForbidden && isZenFreeTierError(bodyStr) {
			msg = "OpenCode rejected this request with 403 FreeTierError: its free tier can " +
				"only be used from within OpenCode. Your API key is valid — paid models on " +
				"the same key keep working. Pick a paid model with /model; if this model is " +
				"expected to work in San, the gateway's gate has likely changed and should be reported."
		}
		return nil, &ai.Error{
			Driver:  "openai-chat",
			Kind:    kind,
			Status:  res.StatusCode,
			Message: msg,
		}
	}

	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil, err
	}

	models := make([]ai.Model, 0, len(listing.Data))
	for _, m := range listing.Data {
		api := zenAPIForModel(m.ID)
		if m.ID == "" || api == "" {
			continue // unknown family or explicitly excluded (e.g. gemini)
		}
		models = append(models, ai.Model{
			ID:   m.ID,
			Name: m.ID,
			API:  api,
		})
	}
	if len(models) == 0 {
		// ponytail: one chat seed keeps the picker non-empty offline; ceiling
		// is single-family fallback. Per-family seeds when picker needs them.
		return []ai.Model{{ID: zenModel, Name: zenModel, API: ai.APIOpenAIChat}}, nil
	}
	return models, nil
}

// zenAPIForModel maps a Zen model ID to the wire protocol its family uses at
// the Zen gateway. All three families share the base URL; the driver appends
// the correct path suffix (/responses, /messages, /chat/completions).
//
// Gemini models are intentionally excluded (return ""): their endpoint is
// per-model (https://opencode.ai/zen/v1/models/<id>) and the Google GenAI
// driver cannot derive that path from a shared base URL.
//
// Unknown families also return "" so callers skip the entry instead of
// guessing a protocol that may reject the request.
func zenAPIForModel(id string) ai.API {
	lower := strings.ToLower(id)
	switch {
	case strings.HasPrefix(lower, "gpt-"),
		strings.HasPrefix(lower, "grok-"),
		strings.HasPrefix(lower, "muse-spark-"):
		return ai.APIOpenAIResponses
	case strings.HasPrefix(lower, "claude-"),
		strings.HasPrefix(lower, "qwen"):
		return ai.APIAnthropicMessages
	case strings.HasPrefix(lower, "deepseek-"),
		strings.HasPrefix(lower, "minimax-"),
		strings.HasPrefix(lower, "glm-"),
		strings.HasPrefix(lower, "kimi-"),
		strings.HasPrefix(lower, "mimo-"),
		strings.HasPrefix(lower, "ling-"),
		strings.HasPrefix(lower, "nemotron-"),
		lower == "big-pickle":
		return ai.APIOpenAIChat
	default:
		// gemini-* and unknown families: not routable through the shared base.
		return ""
	}
}

// zenErrorKind classifies a Zen gateway error. A 403 carrying the free-tier
// gate body is client policy (wrong identity), not a credential failure: the
// same key keeps serving paid SKUs, so KindAuth — which reads as "bad key" —
// is incorrect and would mislead the user. Every other 403 is a true auth
// denial.
func zenErrorKind(status int, body string) ai.ErrorKind {
	switch {
	case status == http.StatusUnauthorized:
		return ai.KindAuth
	case status == http.StatusForbidden:
		if isZenFreeTierError(body) {
			return ai.KindInvalidRequest
		}
		return ai.KindAuth
	case status == http.StatusTooManyRequests:
		return ai.KindRateLimit
	case status >= 500:
		return ai.KindOverloaded
	default:
		return ai.KindInvalidRequest
	}
}

// Model Studio serves hundreds of models and publishes no window for any of
// them in its listing — asking for all of them up front would be hundreds of
// round trips before the picker could draw. It does answer per model, so the
// window is fetched when one is actually chosen, which is what San's
// ModelLimitsFetcher exists for.

// modelDetailTimeout bounds one model-detail lookup. It sits on the path that
// resolves a context window, so a slow answer must not stall a turn.
const modelDetailTimeout = 8 * time.Second

// FetchModelLimits reports one model's token limits, for the endpoints that
// answer per model rather than in their listing.
//
// It reports an error for every other vendor, which is the honest answer:
// San's resolver only reaches for it when the listing already came back
// without a window, and a vendor with nothing further to ask has nothing
// further to say.
func (p *vendorProvider) FetchModelLimits(ctx context.Context, modelID string) (inputLimit, outputLimit int, err error) {
	if p.vendor.ID != alibabaVendor {
		return 0, 0, fmt.Errorf("llm: %s publishes no per-model limits", p.vendor.ID)
	}
	return dashscopeModelLimits(ctx, p, modelID)
}

// alibabaVendor is the one endpoint here with a per-model detail lookup.
const alibabaVendor = "alibaba"

// dashscopeModelLimits reads extra_info.default_envs off a Model Studio model.
func dashscopeModelLimits(ctx context.Context, p *vendorProvider, modelID string) (int, int, error) {
	cfg := p.endpoint.ConfigFor(p.model(modelID))

	ctx, cancel := context.WithTimeout(ctx, modelDetailTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL()+"/models/"+modelID, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	for name, value := range cfg.MergedHeaders() {
		req.Header.Set(name, value)
	}

	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, 0, err
	}
	if res.StatusCode >= 400 {
		return 0, 0, fmt.Errorf("llm: model detail for %s: http %d", modelID, res.StatusCode)
	}

	var detail struct {
		ExtraInfo struct {
			DefaultEnvs struct {
				MaxInputTokens  int `json:"max_input_tokens"`
				MaxOutputTokens int `json:"max_output_tokens"`
			} `json:"default_envs"`
		} `json:"extra_info"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return 0, 0, err
	}
	return detail.ExtraInfo.DefaultEnvs.MaxInputTokens, detail.ExtraInfo.DefaultEnvs.MaxOutputTokens, nil
}
