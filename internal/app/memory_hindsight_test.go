package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
	hindsight "github.com/genai-io/san/internal/memory"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/todo"
)

// memoryModel builds a minimal model carrying the given memory settings plus
// the reminder service the recall handler enqueues into. Tracker/Subagent are
// populated because the submit path's CommitMessages render touches them.
//
// The global default settings are set too (and restored): the model reads
// memory config through its injected services.Setting, while the hindsight
// package reads setting.DefaultIfInit — in production setting.Initialize
// makes them the same object; here both are wired to the same cfg.
func memoryModel(t *testing.T, cfg setting.MemorySettings) *model {
	t.Helper()
	svc := setting.New(&setting.Data{Memory: cfg})
	prev := setting.DefaultIfInit()
	setting.SetDefaultSettings(svc)
	t.Cleanup(func() {
		setting.ResetDefaultSettings()
		if prev != nil {
			setting.SetDefaultSettings(prev)
		}
		hindsight.Reset()
	})
	return &model{
		services: services{
			Setting:  svc,
			Reminder: reminder.NewService(),
			Tracker:  todo.NewStore(),
			Subagent: subagent.NewRegistry(),
		},
	}
}

func TestShouldAutoRecallGates(t *testing.T) {
	cases := []struct {
		name       string
		cfg        setting.MemorySettings
		inFlight   bool
		autopilot  bool
		wantRecall bool
	}{
		{"backend off (default)", setting.MemorySettings{}, false, false, false},
		{"backend on, auto on", setting.MemorySettings{Backend: setting.MemoryBackendHindsight}, false, false, true},
		{"auto-recall explicitly off", setting.MemorySettings{
			Backend:    setting.MemoryBackendHindsight,
			AutoRecall: boolPtr(false),
		}, false, false, false},
		{"recall already in flight", setting.MemorySettings{Backend: setting.MemoryBackendHindsight}, true, false, false},
		{"autopilot continuation is mid-task", setting.MemorySettings{Backend: setting.MemoryBackendHindsight}, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := memoryModel(t, tc.cfg)
			m.autoRecallInFlight = tc.inFlight
			m.autopilotContinuing = tc.autopilot
			if got := m.shouldAutoRecall(); got != tc.wantRecall {
				t.Errorf("shouldAutoRecall() = %v, want %v", got, tc.wantRecall)
			}
		})
	}
}

func TestMaybeAutoRecallSubmitDirectWhenDisabled(t *testing.T) {
	m := memoryModel(t, setting.MemorySettings{}) // off
	msg := core.UserMessage("fix the bug", nil)
	cmd := m.maybeAutoRecallSubmit(msg)
	if cmd == nil {
		t.Fatal("disabled memory must still submit (direct cmd)")
	}
	if m.autoRecallInFlight {
		t.Error("disabled memory must not mark a recall in flight")
	}
	// The direct path is SubmitToAgent's cmd: running it with no provider
	// returns the no-provider notice cmd rather than an autoRecallSubmitMsg.
	if _, ok := cmd().(autoRecallSubmitMsg); ok {
		t.Error("disabled memory must not route through the recall message")
	}
}

func TestAutoRecallFailureStillSubmits(t *testing.T) {
	// Unreachable server: the recall goroutine must deliver an empty-context
	// submit message (never error, never hang past the deadline).
	m := memoryModel(t, setting.MemorySettings{
		Backend: setting.MemoryBackendHindsight,
		URL:     "http://127.0.0.1:1",
	})
	t.Setenv("HINDSIGHT_API_URL", "")

	msg := core.UserMessage("task", nil)
	cmd := m.maybeAutoRecallSubmit(msg)
	if !m.autoRecallInFlight {
		t.Fatal("recall must mark itself in flight")
	}
	got, ok := cmd().(autoRecallSubmitMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want autoRecallSubmitMsg", cmd())
	}
	if got.recalled != "" {
		t.Errorf("recalled = %q, want empty on failure", got.recalled)
	}

	// The handler clears the gate and submits either way.
	m.env.LLMProvider = nil // submission degrades to the no-provider notice
	m.handleAutoRecallSubmit(got)
	if m.autoRecallInFlight {
		t.Error("handler must clear the in-flight gate")
	}
}

func TestAutoRecallInjectsBackgroundContextOnce(t *testing.T) {
	// Server answers one recall with two memories.
	var recallCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recallCalls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if q, _ := body["query"].(string); q == "" {
			t.Error("recall query must not be empty")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"text": "hashline edits are atomic", "type": "experience", "mentioned_at": "2026-01-02T00:00:00Z"},
				{"text": "tests need GOCACHE"},
			},
		})
	}))
	defer srv.Close()

	m := memoryModel(t, setting.MemorySettings{
		Backend: setting.MemoryBackendHindsight,
		URL:     srv.URL,
	})
	t.Setenv("HINDSIGHT_API_URL", "")

	cmd := m.maybeAutoRecallSubmit(core.UserMessage("how are edits done", nil))
	got := cmd().(autoRecallSubmitMsg)
	if !strings.Contains(got.recalled, "hashline edits are atomic [experience] (2026-01-02)") {
		t.Errorf("recalled = %q", got.recalled)
	}

	// Handler enqueues exactly one pending reminder, framed as hindsight
	// background knowledge (not instructions).
	m.env.LLMProvider = nil
	m.handleAutoRecallSubmit(got)
	pending := m.services.Reminder.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending reminders = %d, want 1", len(pending))
	}
	if !strings.Contains(pending[0], reminder.ScopeHindsight) {
		t.Errorf("reminder must carry the hindsight scope: %s", pending[0])
	}
	if !strings.Contains(pending[0], "background knowledge") {
		t.Errorf("reminder preamble must frame content as background knowledge: %s", pending[0])
	}
	if recallCalls != 1 {
		t.Errorf("recall calls = %d, want exactly 1 per task", recallCalls)
	}

	// EnqueueOnce semantics: a second identical injection would dedupe —
	// but the in-flight gate already prevents a second auto-recall per task
	// (shouldAutoRecall tests cover that).
}

func TestAutoRetainGatesAndDigest(t *testing.T) {
	answer := strings.Repeat("implemented the hashline diff view and fixed three off-by-one cases in the renderer. ", 2)

	t.Run("off by default", func(t *testing.T) {
		m := memoryModel(t, setting.MemorySettings{})
		m.conv.Messages = []core.ChatMessage{
			{Role: core.ChatUser, Content: "build a diff view", DisplayContent: "build a diff view"},
		}
		if cmd := m.maybeAutoRetain(core.Result{Content: answer, StopReason: core.StopEndTurn}); cmd != nil {
			t.Error("auto-retain must produce no cmd when disabled")
		}
		if m.autoRetainInFlight {
			t.Error("disabled retain must not set the in-flight flag")
		}
	})

	t.Run("skips canceled and error turns", func(t *testing.T) {
		m := memoryModel(t, setting.MemorySettings{Backend: setting.MemoryBackendHindsight, AutoRetain: true})
		m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "task", DisplayContent: "task"}}
		if cmd := m.maybeAutoRetain(core.Result{Content: answer, StopReason: core.StopCanceled}); cmd != nil {
			t.Error("canceled turn must not retain")
		}
		if cmd := m.maybeAutoRetain(core.Result{Content: answer, StopReason: core.StopError}); cmd != nil {
			t.Error("error turn must not retain")
		}
	})

	t.Run("skips short acks", func(t *testing.T) {
		m := memoryModel(t, setting.MemorySettings{Backend: setting.MemoryBackendHindsight, AutoRetain: true})
		m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "task", DisplayContent: "task"}}
		if cmd := m.maybeAutoRetain(core.Result{Content: "Done.", StopReason: core.StopEndTurn}); cmd != nil {
			t.Error("a short ack must not be retained")
		}
	})

	t.Run("single-flight and digest dedupe", func(t *testing.T) {
		m := memoryModel(t, setting.MemorySettings{
			Backend:    setting.MemoryBackendHindsight,
			AutoRetain: true,
			URL:        "http://127.0.0.1:1", // retain will fail; that's fine, we test the gates
		})
		t.Setenv("HINDSIGHT_API_URL", "")
		m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "task", DisplayContent: "task"}}
		result := core.Result{Content: answer, StopReason: core.StopEndTurn}

		cmd := m.maybeAutoRetain(result)
		if cmd == nil {
			t.Fatal("enabled retain must fire on a completed turn")
		}
		if !m.autoRetainInFlight {
			t.Fatal("retain must mark itself in flight")
		}
		if cmd2 := m.maybeAutoRetain(result); cmd2 != nil {
			t.Error("a second retain while one is in flight must be skipped")
		}

		// Simulate completion with this digest recorded; the same digest
		// must then be deduped.
		m.autoRetainInFlight = false
		// Production trims the answer before digesting — match it.
		m.lastRetainedDigest = retainDigest("task", strings.TrimSpace(answer))
		if cmd3 := m.maybeAutoRetain(result); cmd3 != nil {
			t.Error("unchanged digest must be deduped")
		}
	})
}

func TestLastHumanRequestSkipsToolResults(t *testing.T) {
	msgs := []core.ChatMessage{
		{Role: core.ChatUser, Content: "the actual request", DisplayContent: "the actual request"},
		{Role: core.ChatUser, Content: "tool output", ToolResult: &core.ToolResult{ToolName: "Read"}},
		{Role: core.ChatAssistant, Content: "answer"},
	}
	got := lastHumanRequest(msgs)
	if got != "the actual request" {
		t.Errorf("lastHumanRequest = %q, want the human request (tool rows skipped)", got)
	}
	if lastHumanRequest(nil) != "" {
		t.Error("empty history must return empty")
	}
}

func boolPtr(b bool) *bool { return &b }
