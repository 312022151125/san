package search

import (
	"context"
	"time"
)

const maxSearchResponseSize = 2 * 1024 * 1024 // 2MB limit for search API responses

// ProviderName identifies a search provider
type ProviderName string

const (
	ProviderExa    ProviderName = "exa"
	ProviderTavily ProviderName = "tavily"
	ProviderSerper ProviderName = "serper"
	ProviderBrave  ProviderName = "brave"
	// ProviderSearXNG is a self-hosted SearXNG instance (no API key; the
	// endpoint comes from settings searchUrl or SEARXNG_ENDPOINT).
	ProviderSearXNG ProviderName = "searxng"
)

// SearchResult represents a single search result
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// SearchOptions configures search behavior
type SearchOptions struct {
	NumResults     int
	AllowedDomains []string
	BlockedDomains []string
	Timeout        time.Duration
}

// truncateSnippet truncates a snippet to maxLength runes
func truncateSnippet(s string, maxLength int) string {
	runes := []rune(s)
	if len(runes) <= maxLength {
		return s
	}
	return string(runes[:maxLength]) + "..."
}

// getTimeout returns the timeout or default if not set
func getTimeout(opts SearchOptions) time.Duration {
	if opts.Timeout <= 0 {
		return 30 * time.Second
	}
	return opts.Timeout
}

// Provider is the interface for search providers
type Provider interface {
	// Name returns the provider name
	Name() ProviderName

	// DisplayName returns the human-readable name
	DisplayName() string

	// RequiresAPIKey returns true if an API key is needed
	RequiresAPIKey() bool

	// EnvVars returns the environment variable names for credentials
	EnvVars() []string

	// IsAvailable checks if the provider is configured and ready
	IsAvailable() bool

	// Search performs a web search
	Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
}

// Meta contains metadata about a search provider
type Meta struct {
	Name           ProviderName
	DisplayName    string
	RequiresAPIKey bool
	// RequiresEndpoint marks providers configured by a URL rather than an
	// API key (currently SearXNG: settings searchUrl or SEARXNG_ENDPOINT).
	// Availability = an endpoint is configured.
	RequiresEndpoint bool
	EnvVars          []string
}

// AllProviders returns metadata for all search providers
func AllProviders() []Meta {
	return []Meta{
		{
			Name:           ProviderExa,
			DisplayName:    "Exa AI",
			RequiresAPIKey: false,
			EnvVars:        []string{}, // No API key required
		},
		{
			Name:           ProviderTavily,
			DisplayName:    "Tavily",
			RequiresAPIKey: true,
			EnvVars:        []string{"TAVILY_API_KEY"},
		},
		{
			Name:           ProviderSerper,
			DisplayName:    "Serper (Google)",
			RequiresAPIKey: true,
			EnvVars:        []string{"SERPER_API_KEY"},
		},
		{
			Name:           ProviderBrave,
			DisplayName:    "Brave Search",
			RequiresAPIKey: true,
			EnvVars:        []string{"BRAVE_API_KEY"},
		},
		{
			Name:             ProviderSearXNG,
			DisplayName:      "SearXNG (self-hosted)",
			RequiresAPIKey:   false,
			RequiresEndpoint: true,
			EnvVars:          []string{"SEARXNG_ENDPOINT", "SEARXNG_TOKEN"},
		},
	}
}
