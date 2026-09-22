// Package memory is the optional long-term memory backend (Hindsight).
//
// The package is entirely lazy: nothing here runs unless
// setting.MemorySettings.Enabled() is true. When memory is off there is no
// client, no HTTP connection, and no tool schema — zero runtime overhead.
//
// The HTTP client is hand-rolled net/http against the small slice of the
// Hindsight API San actually uses (retain / recall / reflect + best-effort
// bank creation), mirroring OMP's decision to drop its SDK for a fetch
// client over the same three endpoints. Every call takes the caller's
// context, applies an operation timeout, and returns errors to the caller —
// no background retries, no shared mutable state.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Operation deadlines. Reflect is a server-side synthesis and can take
// noticeably longer than a recall; retain batches a write.
const (
	recallTimeout  = 30 * time.Second
	retainTimeout  = 60 * time.Second
	reflectTimeout = 120 * time.Second
	defaultTimeout = 30 * time.Second

	// maxResponseSize caps a Hindsight response body (matches the 2MB cap
	// the search package applies to provider responses).
	maxResponseSize = 2 << 20
)

// Client is a minimal Hindsight HTTP client. It is immutable after
// construction and safe for concurrent use — every request is an
// independent http call carrying the caller's context.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a client for the given Hindsight base URL. An empty
// token means no Authorization header. httpClient may be nil (tests inject
// a client pointed at httptest; production uses a shared default).
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpClient,
	}
}

// Memory is one recalled memory item. Only the fields San surfaces to the
// model are decoded; Hindsight's response carries more.
type Memory struct {
	Text        string `json:"text"`
	Type        string `json:"type,omitempty"`
	MentionedAt string `json:"mentioned_at,omitempty"`
}

// RetainItem is one durable fact to store. Context is optional provenance.
type RetainItem struct {
	Content string `json:"content"`
	Context string `json:"context,omitempty"`
}

type retainRequest struct {
	Items []RetainItem `json:"items"`
}

type recallRequest struct {
	Query     string `json:"query"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type recallResponse struct {
	Results []Memory `json:"results"`
}

type reflectRequest struct {
	Query   string `json:"query"`
	Context string `json:"context,omitempty"`
}

type reflectResponse struct {
	Text string `json:"text"`
}

// Retain stores durable items in the bank in a single batched request.
// Failures are returned, never swallowed — the caller decides whether a
// failed write is worth surfacing (tool: yes; auto-retain: log only).
func (c *Client) Retain(ctx context.Context, bankID string, items []RetainItem, tags []string) error {
	if len(items) == 0 {
		return nil
	}
	body := struct {
		retainRequest
		Tags []string `json:"tags,omitempty"`
	}{retainRequest: retainRequest{Items: items}, Tags: tags}
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/default/banks/%s/memories", urlPathEscape(bankID)),
		retainTimeout, body, nil)
}

// Recall searches the bank and returns at most limit relevant memories.
// The request context's deadline (set by the caller or the built-in
// timeout) cancels the HTTP call on session shutdown or turn cancellation.
func (c *Client) Recall(ctx context.Context, bankID, query string, limit int) ([]Memory, error) {
	var resp recallResponse
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/default/banks/%s/memories/recall", urlPathEscape(bankID)),
		recallTimeout, recallRequest{Query: query, MaxTokens: 1024}, &resp)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(resp.Results) > limit {
		resp.Results = resp.Results[:limit]
	}
	return resp.Results, nil
}

// Reflect asks the server to synthesize an answer over accumulated memory
// and returns its text. Empty responses become a clear "nothing there"
// message rather than an empty string.
func (c *Client) Reflect(ctx context.Context, bankID, query, extra string) (string, error) {
	var resp reflectResponse
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/default/banks/%s/reflect", urlPathEscape(bankID)),
		reflectTimeout, reflectRequest{Query: query, Context: extra}, &resp)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return "No relevant information found to reflect on.", nil
	}
	return resp.Text, nil
}

// EnsureBank best-effort creates the bank before the first write, once per
// process per bank. Failure is swallowed: a read (recall) works against an
// existing bank, and a write against a missing bank surfaces as a normal
// error the caller already handles. This mirrors OMP's ensureBankExists.
func (c *Client) EnsureBank(ctx context.Context, bankID string) {
	if _, loaded := ensuredBanks.LoadOrStore(bankID, struct{}{}); loaded {
		return
	}
	_ = c.do(ctx, http.MethodPut,
		fmt.Sprintf("/v1/default/banks/%s", urlPathEscape(bankID)),
		defaultTimeout, map[string]string{"id": bankID}, nil)
}

// ensuredBanks tracks banks already offered to the server this process.
var ensuredBanks sync.Map

// do encodes body (when non-nil), performs the request under a timeout
// derived from the caller's context, and decodes into out (when non-nil).
// Response bodies are read through io.LimitReader; non-2xx statuses become
// errors carrying the status and a truncated body excerpt.
func (c *Client) do(ctx context.Context, method, path string, timeout time.Duration, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("memory: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("memory: build %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("memory: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("memory: %s %s: read: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt := strings.TrimSpace(string(data))
		if len(excerpt) > 200 {
			excerpt = excerpt[:200] + "..."
		}
		return fmt.Errorf("memory: %s %s: HTTP %d: %s", method, path, resp.StatusCode, excerpt)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("memory: %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

// urlPathEscape escapes a bank id for a single URL path segment.
func urlPathEscape(s string) string {
	return url.PathEscape(s)
}
