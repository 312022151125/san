// Hindsight long-term memory integration on the orchestration side: the
// once-per-user-task auto-recall gate in the submission pipeline and the
// opt-in auto-retain at turn end. Both are no-ops unless
// settings.memory.backend == "hindsight"; memory off means no client, no
// HTTP, no reminder, zero overhead (the gate reads one settings struct).
//
// Design rules (see notes/active/hindsight-searchxng-plan.md):
//   - recall runs before the turn starts, bounded by a short deadline so a
//     hung server never stalls the session; failure logs and submits anyway
//   - results arrive as a background-context reminder (never instructions)
//   - retention only happens on the parent/orchestration side, from a
//     consolidated task digest — never raw transcripts, never subagents
package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/core"
	hindsight "github.com/genai-io/san/internal/memory"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/setting"
)

const (
	// autoRecallDeadline bounds the pre-submit recall. Deliberately much
	// shorter than the client's 30s tool-recall timeout: this one defers
	// the turn start, so a dead server must cost the user seconds, not half
	// a minute. A local Hindsight answers in milliseconds.
	autoRecallDeadline = 5 * time.Second

	// autoRecallQueryChars caps the recall query (OMP's recallMaxQueryChars
	// = 800) so a pasted novel never becomes the query body.
	autoRecallQueryChars = 800

	// Digest caps for auto-retain: enough to carry the task and its
	// outcome, nowhere near the full transcript.
	retainRequestChars  = 1000
	retainAnswerChars   = 2000
	minRetainAnswerChars = 50 // shorter than this is an ack/error, not durable work
)

// autoRecallSubmitMsg carries the deferred provider turn plus the recalled
// context (empty on failure/timeout/disabled). Delivered on the UI
// goroutine; the recall itself ran in the tea.Cmd goroutine with no access
// to the model.
type autoRecallSubmitMsg struct {
	providerMsg core.Message
	recalled    string
}

// autoRetainDoneMsg acknowledges a finished background retain so the
// single-flight flag clears on the UI goroutine (no cross-goroutine write).
type autoRetainDoneMsg struct {
	digest string // retained digest ("" when the retain was skipped/failed)
}

// memoryConfig returns the live memory settings (zero value when settings
// are not loaded — which reads as "backend off").
func (m *model) memoryConfig() setting.MemorySettings {
	if m.services.Setting == nil {
		return setting.MemorySettings{}
	}
	return m.services.Setting.Memory()
}

// shouldAutoRecall gates the once-per-task recall: enabled backend, auto
// recall on, no recall already in flight, and a human-originated task (an
// autopilot continuation is mid-task, not a new one).
func (m *model) shouldAutoRecall() bool {
	cfg := m.memoryConfig()
	return cfg.Enabled() && cfg.AutoRecallOn() &&
		!m.autoRecallInFlight && !m.autopilotContinuing
}

// maybeAutoRecallSubmit runs the auto-recall gate just before the provider
// turn. When a recall is due it defers the submit: returns a cmd that
// recalls in the background and delivers autoRecallSubmitMsg, whose handler
// enqueues the background-context reminder and submits. Any failure path
// (backend gone, timeout, empty results) submits the original turn with no
// injection — the session proceeds normally.
//
// While a recall is in flight, m.autoRecallInFlight parks further
// submissions in the input queue (handleSubmit treats it like an active
// stream), preserving submission order.
func (m *model) maybeAutoRecallSubmit(providerMsg core.Message) tea.Cmd {
	if !m.shouldAutoRecall() {
		return m.SubmitToAgent(providerMsg)
	}
	m.autoRecallInFlight = true

	cwd := m.env.CWD
	cfg := m.memoryConfig()
	query := hindsight.TruncateRunes(strings.TrimSpace(providerMsg.Text()), autoRecallQueryChars)
	deadline := autoRecallDeadline

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()

		var recalled string
		b := hindsight.Get(cwd)
		if b == nil {
			// Settings changed under us (backend switched off mid-flight).
			return autoRecallSubmitMsg{providerMsg: providerMsg}
		}
		results, err := b.Client.Recall(ctx, b.BankID, query, cfg.ResolvedMaxResults())
		if err != nil {
			log.QueueLog("auto-recall skipped: %v", err)
			return autoRecallSubmitMsg{providerMsg: providerMsg}
		}
		if len(results) == 0 {
			return autoRecallSubmitMsg{providerMsg: providerMsg}
		}
		recalled = hindsight.FormatBullets(results, cfg.ResolvedMaxResults())
		return autoRecallSubmitMsg{providerMsg: providerMsg, recalled: recalled}
	}
}

// handleAutoRecallSubmit clears the in-flight gate, attaches whatever was
// recalled as a background-context reminder (the "hindsight" preamble frames
// it as knowledge, not instructions), and submits the turn either way.
func (m *model) handleAutoRecallSubmit(msg autoRecallSubmitMsg) tea.Cmd {
	m.autoRecallInFlight = false
	if body := reminder.WrapMemory(reminder.ScopeHindsight, msg.recalled); body != "" {
		m.services.Reminder.EnqueueOnce(body)
	}
	return m.SubmitToAgent(msg.providerMsg)
}

// maybeAutoRetain fires the opt-in background retain at a completed turn.
// Parent-side only (OnTurnEnd), single-flight via a UI-goroutine flag, and
// deliberately conservative: canceled/error turns and turns whose final
// answer is a short ack produce nothing worth storing. The digest is the
// request plus the trimmed outcome — a consolidation, not a transcript.
func (m *model) maybeAutoRetain(result core.Result) tea.Cmd {
	cfg := m.memoryConfig()
	if !cfg.Enabled() || !cfg.AutoRetain || m.autoRetainInFlight {
		return nil
	}
	if result.StopReason == core.StopCanceled || result.StopReason == core.StopError {
		return nil
	}
	answer := strings.TrimSpace(result.Content)
	if len(answer) < minRetainAnswerChars {
		return nil
	}
	request := lastHumanRequest(m.conv.Messages)
	if request == "" {
		return nil
	}
	digest := retainDigest(request, answer)

	// Skip an unchanged digest (autopilot chains can end several turns on
	// the same request+answer pair).
	if digest == m.lastRetainedDigest {
		return nil
	}

	cwd := m.env.CWD
	m.autoRetainInFlight = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		b := hindsight.Get(cwd)
		if b == nil {
			return autoRetainDoneMsg{}
		}
		b.Client.EnsureBank(ctx, b.BankID)
		err := b.Client.Retain(ctx, b.BankID, []hindsight.RetainItem{
			{Content: digest, Context: "auto-retain: consolidated task digest"},
		}, nil)
		if err != nil {
			// Background retain never surfaces to the model or blocks the
			// session — log only (matches OMP's warning-notice behavior).
			log.QueueLog("auto-retain failed: %v", err)
			return autoRetainDoneMsg{}
		}
		return autoRetainDoneMsg{digest: digest}
	}
}

// retainDigest consolidates a task into the single durable fact auto-retain
// stores: the request plus the trimmed outcome, each capped. Shared with
// tests so the digest-dedupe assertion cannot drift from production.
func retainDigest(request, answer string) string {
	return fmt.Sprintf("[task] %s\n\n[outcome] %s",
		hindsight.TruncateRunes(request, retainRequestChars),
		hindsight.TruncateRunes(answer, retainAnswerChars))
}

// lastHumanRequest returns the most recent plain user request (tool results
// are ChatUser rows with a ToolResult and are excluded). DisplayContent —
// the human-readable form — wins, with Content as fallback for rows built
// without it.
func lastHumanRequest(msgs []core.ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role == core.ChatUser && msg.ToolResult == nil && strings.TrimSpace(msg.Content) != "" {
			if display := strings.TrimSpace(msg.DisplayContent); display != "" {
				return display
			}
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}
