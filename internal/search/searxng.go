package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/genai-io/san/internal/secret"
	"github.com/genai-io/san/internal/setting"
)

const (
	searxngEnvEndpoint = "SEARXNG_ENDPOINT"
	searxngEnvToken    = "SEARXNG_TOKEN"
)

// SearXNGProvider queries a self-hosted SearXNG instance's JSON search API
// (https://docs.searxng.org/dev/search_api.html). Endpoint resolution
// follows OMP's precedence: settings searchUrl → SEARXNG_ENDPOINT env →
// unavailable. There is no hardcoded default URL — a self-hosted instance
// has no standard port (OMP leaves searxng.endpoint unset for the same
// reason). The optional bearer token comes from SEARXNG_TOKEN (env or the
// secret store, via secret.Resolve — credentials never live in
// settings.json).
type SearXNGProvider struct {
	// endpointOverride and tokenOverride let tests inject values without
	// touching settings or the environment.
	endpointOverride string
	tokenOverride    string
}

// NewSearXNGProvider creates a provider bound to the configured endpoint.
func NewSearXNGProvider(endpointOverride ...string) *SearXNGProvider {
	p := &SearXNGProvider{}
	if len(endpointOverride) > 0 {
		p.endpointOverride = endpointOverride[0]
	}
	return p
}

func (p *SearXNGProvider) Name() ProviderName   { return ProviderSearXNG }
func (p *SearXNGProvider) DisplayName() string  { return "SearXNG (self-hosted)" }
func (p *SearXNGProvider) RequiresAPIKey() bool { return false }
func (p *SearXNGProvider) EnvVars() []string    { return []string{searxngEnvEndpoint, searXNGEnvToken} }

// IsAvailable reports whether an endpoint is configured (settings or env).
// A token is optional — most instances run without auth.
func (p *SearXNGProvider) IsAvailable() bool { return p.endpoint() != "" }

// endpoint resolves the instance URL: override → settings searchUrl →
// SEARXNG_ENDPOINT env.
func (p *SearXNGProvider) endpoint() string {
	if p.endpointOverride != "" {
		return strings.TrimRight(p.endpointOverride, "/")
	}
	if s := setting.DefaultIfInit(); s != nil {
		if u := strings.TrimSpace(s.SearchURL()); u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	return strings.TrimRight(secret.Resolve(searxngEnvEndpoint), "/")
}

// token resolves the optional bearer token: override → SEARXNG_TOKEN
// (env first, then the secret store).
func (p *SearXNGProvider) token() string {
	if p.tokenOverride != "" {
		return p.tokenOverride
	}
	return secret.Resolve(searxngEnvToken)
}

// searxngResponse mirrors the JSON API's result list; other payload fields
// (answers, corrections, suggestions, unresponsive_engines) are dropped.
type searxngResponse struct {
	Results []searxngResult `json:"results"`
}

type searxngResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"` // SearXNG's snippet field (aka "snippet")
	Snippet string `json:"snippet"`
}

// Search runs one query against the configured instance and normalizes to
// SearchResult. Raw JSON never leaves this function — the model sees only
// title/url/snippet lines.
func (p *SearXNGProvider) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	endpoint := p.endpoint()
	if endpoint == "" {
		return nil, fmt.Errorf("SearXNG endpoint not set (settings searchUrl or %s)", searxngEnvEndpoint)
	}

	numResults := opts.NumResults
	if numResults <= 0 {
		numResults = 10
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	// SearXNG pages (default 10 results/page); ask for the first page and
	// cap locally below.
	params.Set("pageno", "1")

	u := endpoint + "/search?" + params.Encode()
	client := &http.Client{Timeout: getTimeout(opts)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token := p.token(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxSearchResponseSize))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var parsed searxngResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	results := make([]SearchResult, 0, min(numResults, len(parsed.Results)))
	for _, r := range parsed.Results {
		if len(results) >= numResults {
			break
		}
		if !matchesDomainFilter(r.URL, opts.AllowedDomains, opts.BlockedDomains) {
			continue
		}
		snippet := r.Content
		if snippet == "" {
			snippet = r.Snippet
		}
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: truncateSnippet(snippet, 200),
		})
	}
	return results, nil
}
