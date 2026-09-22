package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/genai-io/san/internal/setting"
)

// TestSearXNGSearchMapsResults exercises request shape, auth, and response
// normalization against a fake SearXNG JSON endpoint.
func TestSearXNGSearchMapsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %s, want /search", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("format") != "json" {
			t.Errorf("format = %q, want json", q.Get("format"))
		}
		if q.Get("q") != "golang error handling" {
			t.Errorf("q = %q", q.Get("q"))
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sekrit" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "Errors are values", "url": "https://example.com/errors", "content": "Go treats errors as values..."},
				{"title": "Blocked", "url": "https://blocked.example.org/x", "content": "should be filtered"},
				{"title": "Snippet field only", "url": "https://example.com/snippet", "snippet": "legacy snippet field"},
				{"title": "Fourth", "url": "https://example.com/fourth", "content": "cut by num_results"},
			},
		})
	}))
	defer srv.Close()

	p := NewSearXNGProvider(srv.URL)
	p.tokenOverride = "sekrit"
	if !p.IsAvailable() {
		t.Fatal("provider with explicit endpoint must be available")
	}

	results, err := p.Search(context.Background(), "golang error handling", SearchOptions{
		NumResults:     3,
		BlockedDomains: []string{"blocked.example.org"},
		Timeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3 (blocked domain filtered out, cap applied)", len(results))
	}
	if results[0].Title != "Errors are values" || results[0].URL != "https://example.com/errors" {
		t.Errorf("results[0] = %+v", results[0])
	}
	if results[0].Snippet != "Go treats errors as values..." {
		t.Errorf("snippet = %q", results[0].Snippet)
	}
	// The third result only carries the legacy "snippet" field.
	if results[1].URL != "https://example.com/snippet" || results[1].Snippet != "legacy snippet field" {
		t.Errorf("results[1] = %+v", results[1])
	}
	// num_results=3: exactly three pass through even though four were sent.
	if results[2].URL != "https://example.com/fourth" {
		t.Errorf("results[2] = %+v", results[2])
	}
}

func TestSearXNGUnavailableWithoutEndpoint(t *testing.T) {
	p := NewSearXNGProvider()
	// Neutralize settings/env for the check: no override means endpoint()
	// reads settings searchUrl then SEARXNG_ENDPOINT.
	t.Setenv("SEARXNG_ENDPOINT", "")
	if p.IsAvailable() {
		// settings searchUrl may be set by another test's global state —
		// only assert the env-only path when settings are also clear.
		if s := setting.DefaultIfInit(); s != nil && s.SearchURL() != "" {
			t.Skip("settings searchUrl set by another test")
		}
		t.Fatal("provider without endpoint must be unavailable")
	}
	if _, err := p.Search(context.Background(), "q", SearchOptions{}); err == nil {
		t.Fatal("Search without endpoint must error, not guess a URL")
	}
}

func TestSearXNGHTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer srv.Close()

	p := NewSearXNGProvider(srv.URL)
	if _, err := p.Search(context.Background(), "q", SearchOptions{Timeout: time.Second}); err == nil {
		t.Fatal("want error on HTTP 502")
	}
}

func TestCreateProviderSearXNG(t *testing.T) {
	p := CreateProvider(ProviderSearXNG)
	if p.Name() != ProviderSearXNG {
		t.Errorf("Name = %q", p.Name())
	}
	if p.RequiresAPIKey() {
		t.Error("SearXNG must not require an API key")
	}
	// It must appear in the TUI metadata table with its endpoint requirement.
	found := false
	for _, meta := range AllProviders() {
		if meta.Name == ProviderSearXNG {
			found = true
			if !meta.RequiresEndpoint {
				t.Error("SearXNG meta must set RequiresEndpoint")
			}
		}
	}
	if !found {
		t.Error("SearXNG missing from AllProviders")
	}
}
