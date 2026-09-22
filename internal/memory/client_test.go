package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestServer asserts request shape and answers with a canned body.
func newTestServer(t *testing.T, wantMethod, wantPath string, wantBody any, answer string) (*httptest.Server, *Client) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != wantMethod {
			t.Errorf("method = %s, want %s", r.Method, wantMethod)
		}
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		if wantBody != nil {
			var got, want any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode body: %v", err)
			}
			wantBytes, _ := json.Marshal(wantBody)
			if err := json.Unmarshal(wantBytes, &want); err != nil {
				t.Fatalf("marshal want: %v", err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("body = %s, want %s", gotJSON, wantJSON)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	c := NewClient(srv.URL, "test-token", srv.Client())
	t.Cleanup(srv.Close)
	return srv, c
}

func TestRetainSendsBatchedItemsWithAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/default/banks/san-myrepo/memories" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body struct {
			Items []RetainItem `json:"items"`
			Tags  []string     `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if len(body.Items) != 2 || body.Items[0].Content != "always run tests" ||
			body.Items[0].Context != "convention" || body.Items[1].Content != "hashline edits are atomic" {
			t.Errorf("items = %+v", body.Items)
		}
		if len(body.Tags) != 1 || body.Tags[0] != "project:myrepo" {
			t.Errorf("tags = %v", body.Tags)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token", srv.Client())

	err := c.Retain(context.Background(), "san-myrepo", []RetainItem{
		{Content: "always run tests", Context: "convention"},
		{Content: "hashline edits are atomic"},
	}, []string{"project:myrepo"})
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}
}

func TestRecallDecodesAndLimitsResults(t *testing.T) {
	answer := `{"results":[
		{"text":"m1","type":"experience","mentioned_at":"2026-01-01"},
		{"text":"m2"},
		{"text":"m3"},
		{"text":"m4"}
	]}`
	_, c := newTestServer(t, http.MethodPost,
		"/v1/default/banks/san/memories/recall",
		map[string]any{"query": "how do edits work", "max_tokens": 1024},
		answer,
	)
	got, err := c.Recall(context.Background(), "san", "how do edits work", 2)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (maxResults cap)", len(got))
	}
	if got[0].Text != "m1" || got[0].Type != "experience" || got[0].MentionedAt != "2026-01-01" {
		t.Errorf("first memory = %+v", got[0])
	}

	// No cap: all results pass through (same query so the strict body
	// assertion still applies).
	all, err := c.Recall(context.Background(), "san", "how do edits work", 0)
	if err != nil {
		t.Fatalf("Recall uncapped: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("uncapped len = %d, want 4", len(all))
	}
}

func TestReflectReturnsServerTextAndFallback(t *testing.T) {
	_, c := newTestServer(t, http.MethodPost,
		"/v1/default/banks/san/reflect",
		map[string]any{"query": "why hashline", "context": "for the plan"},
		`{"text":"because anchors are stable"}`,
	)
	got, err := c.Reflect(context.Background(), "san", "why hashline", "for the plan")
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if got != "because anchors are stable" {
		t.Errorf("got %q", got)
	}

	// Blank synthesis → clear fallback message, not an empty string.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"   "}`))
	}))
	defer srv.Close()
	c = NewClient(srv.URL, "", srv.Client())
	got, err = c.Reflect(context.Background(), "san", "q", "")
	if err != nil {
		t.Fatalf("Reflect blank: %v", err)
	}
	if got != "No relevant information found to reflect on." {
		t.Errorf("blank fallback = %q", got)
	}
}

func TestEnsureBankHappensOnceAndSwallowsFailure(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		w.WriteHeader(http.StatusNotFound) // failure must be swallowed
		_, _ = w.Write([]byte(`{"detail":"nope"}`))
	}))
	defer srv.Close()

	// Fresh bank namespace so a previous test's marker can't hide the call.
	bank := "san-once-test"
	ensuredBanks.Delete(bank)

	c := NewClient(srv.URL, "", srv.Client())
	c.EnsureBank(context.Background(), bank)
	c.EnsureBank(context.Background(), bank) // second call is a no-op
	if calls != 1 {
		t.Errorf("EnsureBank hit the server %d times, want 1", calls)
	}
}

func TestDoSurfacesHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"detail":"down"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", srv.Client())

	_, err := c.Recall(context.Background(), "san", "q", 5)
	if err == nil {
		t.Fatal("want error on 503")
	}
	if !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "down") {
		t.Errorf("error = %v, want status + body excerpt", err)
	}
}

func TestCancelledContextPropagatesToHTTPRequest(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the handler until the client goes away — exercises request
		// cancellation rather than a fast-failing endpoint.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	c := NewClient(srv.URL, "", srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := c.Recall(ctx, "san", "q", 5)
	if err == nil {
		t.Fatal("want error after cancellation")
	}
	if !strings.Contains(err.Error(), "context canceled") &&
		!errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context cancellation", err)
	}
}

func TestUnreachableServerReturnsErrorNotPanic(t *testing.T) {
	// Port 1 on localhost: connection refused, the classic "server down" case.
	c := NewClient("http://127.0.0.1:1", "", nil)
	if _, err := c.Recall(context.Background(), "san", "q", 5); err == nil {
		t.Fatal("want error when Hindsight is unreachable")
	}
	if err := c.Retain(context.Background(), "san", []RetainItem{{Content: "x"}}, nil); err == nil {
		t.Fatal("want error when Hindsight is unreachable")
	}
}
