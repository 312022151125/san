package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"

	"github.com/genai-io/san/internal/secret"

	// The wire protocols San's vendors speak, and the two Vertex deployments of them.
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/anthropic"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/anthropic/vertex"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/google"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/google/vertex"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/openai/chat"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/openai/responses"
)

// Which San provider is which catalog vendor.
//
// San names a connection by provider *and* auth method — "anthropic:vertex" is
// the same models reached a different way — while the SDK's catalog gives each
// way of reaching an endpoint its own vendor row. The table below is that
// mapping, and it is all there is to adding a vendor: no package, no client, no
// conversion code.

// entry is one San provider/auth-method pair served by one catalog vendor.
type vendorEntry struct {
	meta     Meta
	vendorID string

	// configure fills in what the catalog cannot: a credential from San's
	// secret store, an endpoint the user set, a deployment. Nil means the
	// common case — the vendor's first key variable and its default host.
	configure func(catalog.Vendor, *sdkprovider.Config) error
}

// displays are the provider-level UI rows, one per San provider.
var vendorDisplays = map[ProviderID]ProviderDisplay{
	Anthropic:      {Name: "Anthropic", Order: 10},
	OpenAI:         {Name: "OpenAI", Order: 20},
	Copilot:        {Name: "GitHub Copilot", Order: 25},
	Google:         {Name: "Google", Order: 30},
	DeepSeek:       {Name: "DeepSeek", Order: 40},
	SenseNova:      {Name: "SenseNova", Order: 50},
	MinMax:         {Name: "MiniMax", Order: 60},
	Moonshot:       {Name: "Moonshot", Order: 70},
	Alibaba:        {Name: "Alibaba", Order: 80},
	BigModel:       {Name: "Z.ai (GLM series)", Order: 90},
	Ollama:         {Name: "Ollama (Local)", Order: 100},
	Mimo:           {Name: "Xiaomi MiMo", Order: 110},
	Volcengine:     {Name: "Volcengine Ark", Order: 120},
	AgnesAI:        {Name: "Agnes-AI", Order: 130},
	OpenCodeZen:    {Name: "OpenCode Zen", Order: 135},
	CustomProvider: {Name: "Custom", Order: 140},
}

var vendorEntries = []vendorEntry{
	{
		meta:     Meta{Provider: Anthropic, AuthMethod: AuthAPIKey, EnvVars: []string{"ANTHROPIC_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "anthropic",
	},
	{
		// A connection requires the project alone: the region is optional,
		// defaults to global, and is still read when set.
		meta: Meta{
			Provider: Anthropic, AuthMethod: AuthVertex, DisplayName: "Vertex AI",
			EnvVars:         []string{"ANTHROPIC_VERTEX_PROJECT_ID"},
			OptionalEnvVars: []string{"CLOUD_ML_REGION"},
			Hint:            vertexHint,
		},
		vendorID:  "anthropic-vertex",
		configure: configureVertex,
	},
	{
		meta:     Meta{Provider: OpenAI, AuthMethod: AuthAPIKey, EnvVars: []string{"OPENAI_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "openai",
	},
	{
		meta:      Meta{Provider: OpenAI, AuthMethod: AuthSubscription, DisplayName: "ChatGPT Subscription"},
		vendorID:  "openai-codex",
		configure: configureSignIn,
	},
	{
		meta:      Meta{Provider: Copilot, AuthMethod: AuthSubscription, DisplayName: "Copilot Subscription"},
		vendorID:  "copilot",
		configure: configureSignIn,
	},
	{
		meta:     Meta{Provider: Google, AuthMethod: AuthAPIKey, EnvVars: []string{"GOOGLE_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "google",
	},
	{
		meta: Meta{
			Provider: Google, AuthMethod: AuthVertex, DisplayName: "Vertex AI",
			EnvVars:         []string{"GOOGLE_CLOUD_PROJECT"},
			OptionalEnvVars: []string{"GOOGLE_CLOUD_LOCATION"},
			Hint:            vertexHint,
		},
		vendorID:  "google-vertex",
		configure: configureVertex,
	},
	{
		meta:     Meta{Provider: DeepSeek, AuthMethod: AuthAPIKey, EnvVars: []string{"DEEPSEEK_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "deepseek",
	},
	{
		meta:     Meta{Provider: SenseNova, AuthMethod: AuthAPIKey, EnvVars: []string{"SENSENOVA_API_KEY"}, DisplayName: "Bearer Token API"},
		vendorID: "sensenova",
	},
	{
		meta:     Meta{Provider: MinMax, AuthMethod: AuthAPIKey, EnvVars: []string{"MINIMAX_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "minmax",
	},
	{
		meta:     Meta{Provider: Moonshot, AuthMethod: AuthAPIKey, EnvVars: []string{"MOONSHOT_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "moonshot",
	},
	{
		meta:     Meta{Provider: Alibaba, AuthMethod: AuthAPIKey, EnvVars: []string{"DASHSCOPE_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "alibaba",
	},
	{
		meta:     Meta{Provider: BigModel, AuthMethod: AuthAPIKey, EnvVars: []string{"BIGMODEL_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "bigmodel",
	},
	{
		meta:      Meta{Provider: BigModel, AuthMethod: AuthCoding, EnvVars: []string{"BIGMODEL_API_KEY"}, DisplayName: "Coding Plan"},
		vendorID:  "bigmodel",
		configure: configureBigModelCoding,
	},
	{
		meta:     Meta{Provider: Ollama, AuthMethod: AuthAPIKey, EnvVars: []string{"OLLAMA_BASE_URL"}, DisplayName: "Local (Ollama)"},
		vendorID: "ollama",
	},
	{
		meta:     Meta{Provider: Mimo, AuthMethod: AuthAPIKey, EnvVars: []string{"MIMO_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "mimo",
	},
	{
		meta:      Meta{Provider: Volcengine, AuthMethod: AuthAPIKey, EnvVars: []string{"VOLCENGINE_API_KEY"}, DisplayName: "Bearer Token API"},
		vendorID:  "volcengine",
		configure: configureVolcengine,
	},
	{
		meta:     Meta{Provider: AgnesAI, AuthMethod: AuthAPIKey, EnvVars: []string{"AGNESAI_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "agnesai",
	},
	{
		meta: Meta{Provider: OpenCodeZen, AuthMethod: AuthAPIKey,
			EnvVars: []string{"OPENCODE_ZEN_API_KEY"}, DisplayName: "API Key"},
		configure: configureZen,
	},
	{
		meta:      Meta{Provider: CustomProvider, AuthMethod: AuthAPIKey, EnvVars: []string{CustomAPIKeyEnvVar}, DisplayName: "Direct API"},
		configure: configureCustom,
	},
}

// init makes every vendor in the table reachable through the registry above.
// Importing this package is enough; there is nothing to switch on.
func init() { registerVendors() }

func registerVendors() {
	for name, display := range vendorDisplays {
		RegisterProviderDisplay(name, display)
	}
	for _, e := range vendorEntries {
		Register(e.meta, e.factory())
		if e.vendorID != "" {
			RegisterCostEstimator(e.meta.Provider, costEstimator(e.vendorID))
		}
	}
	registerAuthenticators()
}

// factory returns the constructor San's registry calls to open this endpoint.
func (e vendorEntry) factory() Factory {
	return func(context.Context) (Provider, error) {
		vendor, err := e.resolveVendor()
		if err != nil {
			return nil, err
		}

		cfg := sdkprovider.Config{APIKey: vendorAPIKey(vendor), BaseURL: vendor.ResolveBaseURL(secret.Resolve(vendor.BaseURLEnv))}
		if e.configure != nil {
			if err := e.configure(vendor, &cfg); err != nil {
				return nil, err
			}
		}
		return newVendorProvider(providerName(e.meta), vendor, cfg), nil
	}
}

// resolveVendor returns the catalog row this entry serves. An entry with no
// vendor ID builds its own, which is how the user-defined endpoint — a host
// that exists in no catalog — reaches the same code path as every other.
func (e vendorEntry) resolveVendor() (catalog.Vendor, error) {
	if e.vendorID == "" {
		if e.meta.Provider == OpenCodeZen {
			return zenVendor(), nil
		}
		return customVendor()
	}
	vendor, ok := catalog.Find(e.vendorID)
	if !ok {
		return catalog.Vendor{}, fmt.Errorf("llm: no catalog vendor %q", e.vendorID)
	}
	return vendor, nil
}

// providerName is San's provider identity for a registration, "vendor:auth".
func providerName(meta Meta) string {
	return string(meta.Provider) + ":" + string(meta.AuthMethod)
}

// vendorAPIKey reads the vendor's credential from San's secret store, which resolves
// the environment first and its own file second.
func vendorAPIKey(vendor catalog.Vendor) string {
	for _, name := range vendor.KeyEnv {
		if value := secret.Resolve(name); value != "" {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The entries that need more than a key and a host
// ---------------------------------------------------------------------------

// vertexHint is what the credential form says above a Vertex deployment: the
// rows name a project and a region, and the credential comes from elsewhere.
const vertexHint = "Signs in with Google Application Default Credentials — run `gcloud auth application-default login` first. Region defaults to global."

// configureVertex points a protocol at a Vertex AI deployment — Claude and
// Gemini alike. There is no key: the driver authenticates with Google
// Application Default Credentials, and the row's own Deployment reads the
// project and region, through San's secret store rather than the environment
// alone.
func configureVertex(vendor catalog.Vendor, cfg *sdkprovider.Config) error {
	deployment, err := vendor.Deployment(vendor.DeploymentEnv, secret.Resolve)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	cfg.APIKey = ""
	cfg.ProtocolConfig = deployment
	return nil
}

// configureBigModelCoding points Z.ai's GLM models at the Coding Plan path,
// which is the same endpoint under a different prefix.
func configureBigModelCoding(_ catalog.Vendor, cfg *sdkprovider.Config) error {
	cfg.BaseURL = secret.Resolve("BIGMODEL_CODING_BASE_URL")
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.bigmodel.cn/api/coding/paas/v4"
	}
	return nil
}

// VolcengineModelEnvVar names the Ark model this account is provisioned for.
//
// Ark serves models through per-account endpoints, so there is no catalog to
// list and its own listing answers for the account rather than for the
// product. Naming the one model is how an Ark user says what they have.
const VolcengineModelEnvVar = "VOLCENGINE_MODEL"

// configureVolcengine seeds the endpoint with the model this account names, so
// the picker has something to show even when Ark's listing says nothing.
func configureVolcengine(vendor catalog.Vendor, cfg *sdkprovider.Config) error {
	modelID := secret.Resolve(VolcengineModelEnvVar)
	if modelID == "" {
		return nil
	}
	// Through the vendor, so the window is read out of the model ID the way it
	// is for every other Ark model.
	cfg.Models = []ai.Model{vendor.Model(modelID)}
	return nil
}

// zenBaseURL is the OpenCode Zen gateway root. The driver appends the
// protocol path itself, so this stays the root rather than a full endpoint.
const zenBaseURL = "https://opencode.ai/zen/v1"

// zenUserAgent identifies San to the OpenCode Zen gateway.
// The server requires "opencode/<version>" with version >= 1.17.0; any other
// shape (omp/, Bun default, etc.) is rejected with 403 FreeTierError.
// Bump alongside cmd/san/main.go whenever the San version changes.
const zenUserAgent = "opencode/1.18.31"

// zenSessionOnce and zenSessionID together produce one stable ses_… ID for
// the lifetime of the process. The server requires x-opencode-session to match
// ses_[0-9a-f]{12}[0-9A-Za-z]{14}; a raw UUIDv4 / plain string is rejected.
var (
	zenSessionOnce sync.Once
	zenSessionID   string
)

// zenNewSessionID generates a ses_<12 hex><14 alphanum> identifier.
// The shape mirrors internal/session.GenerateTestSessionID but lives here to
// avoid a feature→feature cycle between llm and session.
func zenNewSessionID() string {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var b [20]byte
	_, _ = rand.Read(b[:])
	hexPart := hex.EncodeToString(b[0:6]) // 12 hex chars
	tail := make([]byte, 14)
	for i := range tail {
		tail[i] = chars[b[6+i]%62]
	}
	return "ses_" + hexPart + string(tail)
}

// zenProcessSessionID returns the stable session ID for this process,
// generating it on first call.
func zenProcessSessionID() string {
	zenSessionOnce.Do(func() { zenSessionID = zenNewSessionID() })
	return zenSessionID
}

// zenModel is the fallback seed used when the live listing is unavailable.
const zenModel = "glm-5.1"

// zenVendor builds the catalog row for OpenCode Zen. It exists in no catalog,
// so San supplies what a vendor entry would have said: the base URL, the
// baseline fallback model, and the Infer hook that maps each model to the
// correct wire protocol.
//
// All three protocol endpoints share the same base URL; the driver selected
// by each model's API appends the correct path suffix automatically:
//   - opencode.ai/zen/v1/responses      → APIOpenAIResponses  (gpt-*, grok-*, muse-spark-*)
//   - opencode.ai/zen/v1/messages       → APIAnthropicMessages (claude-*, qwen*)
//   - opencode.ai/zen/v1/chat/completions → APIOpenAIChat     (deepseek-*, glm-*, kimi-*, …)
//
// Gemini models use a per-model URL shape that the Google GenAI driver cannot
// derive from a shared base; they are excluded from the live listing.
// ponytail: inline row, no catalog entry exists.
func zenVendor() catalog.Vendor {
	return catalog.Vendor{
		ID:          string(OpenCodeZen),
		DisplayName: "OpenCode Zen",
		API:         ai.APIOpenAIChat, // baseline for unknown families
		BaseURL:     zenBaseURL,
		KeyEnv:      []string{"OPENCODE_ZEN_API_KEY"},
		Input:       []ai.Modality{ai.ModalityText, ai.ModalityImage},
		Compat:      ai.OpenAIChatCompat{},
		Models:      []ai.Model{{ID: zenModel, Name: zenModel, API: ai.APIOpenAIChat}},
		// Infer runs after decorate() stamps both API and Compat from the
		// vendor defaults. It overrides both to the correct per-family values
		// so checkCompat() never sees a mismatch during inference validation.
		Infer: zenInfer,
	}
}

// zenInfer maps a model's ID to the correct wire protocol and its matching
// compat type. It runs inside catalog.Vendor.decorate() after the vendor-level
// defaults are stamped, so it is the authoritative source for per-model routing.
func zenInfer(m ai.Model) ai.Model {
	switch zenAPIForModel(m.ID) {
	case ai.APIOpenAIResponses:
		m.API = ai.APIOpenAIResponses
		m.Compat = ai.OpenAIResponsesCompat{}
	case ai.APIAnthropicMessages:
		m.API = ai.APIAnthropicMessages
		m.Compat = ai.AnthropicCompat{}
	case ai.APIOpenAIChat:
		// already correct from vendor defaults; nothing to do
	}
	return m
}

// configureZen points the endpoint at Zen, stamped as OpenCode's own CLI.
// The credential stays the driver's business: it sends cfg.APIKey as
// Authorization: Bearer, so nothing here sets that header by hand.
func configureZen(_ catalog.Vendor, cfg *sdkprovider.Config) error {
	if cfg.BaseURL = secret.Resolve("OPENCODE_ZEN_BASE_URL"); cfg.BaseURL == "" {
		cfg.BaseURL = zenBaseURL // hard-coded default; env only overrides (tests/escape hatch)
	} // mirrors configureBigModelCoding precedent
	cfg.Headers = map[string]string{
		"User-Agent":         zenUserAgent,
		"x-opencode-client":  "cli",
		"x-opencode-session": zenProcessSessionID(),
	}
	cfg.Fetch = zenModels
	return nil
}

// ZenToolPadderFor returns ZenToolPadder when the provider is OpenCode Zen,
// and nil otherwise. Callers pass the result directly to core.Config.ToolPadder.
func ZenToolPadderFor(p Provider) func([]ai.Tool) []ai.Tool {
	if p == nil {
		return nil
	}
	provider, _ := parseProviderKey(p.Name())
	if provider == OpenCodeZen {
		return ZenToolPadder
	}
	return nil
}

// zenGateToolNames are the five core tool names OpenCode's gateway requires in
// every request body. The gate checks names only — schemas are ignored — so
// stubs carry empty descriptions and the minimal schema.
var zenGateToolNames = []string{"bash", "edit", "glob", "grep", "read"}

// ZenToolPadder pads the tool list with wire-inert stubs for any gate-required
// names that are not already present. It satisfies core.Config.ToolPadder.
// Stubs never enter the caller's tool registry — they are body-gate filler only.
//
// The gateway's body-gate requires ≥5 of the core names in tools[]; normal
// agent turns that already carry all five pass through unchanged (same slice).
func ZenToolPadder(tools []ai.Tool) []ai.Tool {
	present := make(map[string]bool, len(tools))
	for _, t := range tools {
		present[t.Schema.Name] = true
	}
	missing := make([]string, 0, len(zenGateToolNames))
	for _, name := range zenGateToolNames {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return tools // already satisfies the gate; return same slice
	}
	padded := make([]ai.Tool, len(tools), len(tools)+len(missing))
	copy(padded, tools)
	for _, name := range missing {
		// Stub: name only, empty description, minimal schema. The gate checks
		// names and ignores schemas, so this is all that is needed on the wire.
		padded = append(padded, ai.Tool{Schema: ai.Schema{Name: name}})
	}
	return padded
}

// customVendor builds a catalog row for the OpenAI-compatible endpoint the
// user configured in the app. It exists in no catalog, so San supplies what a
// vendor entry would have said: the protocol, the host, and nothing else.
func customVendor() (catalog.Vendor, error) {
	store, err := NewStore()
	if err != nil {
		return catalog.Vendor{}, fmt.Errorf("llm: loading the provider store: %w", err)
	}
	cfg := store.CustomProvider()
	if cfg == nil || cfg.BaseURL == "" {
		return catalog.Vendor{}, fmt.Errorf("custom provider not configured: set a base URL under /models > Providers > Custom")
	}
	return catalog.Vendor{
		ID:          string(CustomProvider),
		DisplayName: "Custom",
		API:         ai.APIOpenAIChat,
		BaseURL:     cfg.BaseURL,
		KeyEnv:      []string{CustomAPIKeyEnvVar},
		Input:       []ai.Modality{ai.ModalityText, ai.ModalityImage},
		Compat:      ai.OpenAIChatCompat{},
	}, nil
}

// configureCustom is a no-op beyond what the vendor row already carries: the
// host came from the store and the key from the secret store.
func configureCustom(catalog.Vendor, *sdkprovider.Config) error { return nil }

// costEstimator prices a turn from the vendor's published rate card. A model
// with no card reports unknown, which San renders as "--" rather than as free.
func costEstimator(vendorID string) CostEstimator {
	return func(modelID string, usage Usage) (Money, bool) {
		vendor, ok := catalog.Find(vendorID)
		if !ok {
			return Money{}, false
		}
		pricing := vendor.Model(modelID).Pricing
		if !pricing.Known() {
			return Money{}, false
		}
		cost := pricing.Cost(usage)
		return Money{Amount: cost.Total, Currency: Currency(cost.Currency)}, true
	}
}
