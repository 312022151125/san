package core

import (
	"fmt"
	"sync/atomic"
	"time"
)

// SessionMetrics tracks token and round-trip usage for a single agent session.
// All methods are goroutine-safe via atomic operations.
//
// Use these to measure whether optimizations (Task batch, Command batch,
// context pruning) actually reduce total trajectory cost — do not inject them
// into model prompts.
type SessionMetrics struct {
	modelCalls    atomic.Int64
	toolCalls     atomic.Int64
	commandSteps  atomic.Int64 // Batch tool command executions
	subagentCalls atomic.Int64 // Agent batch item executions
	inputTokens   atomic.Int64
	outputTokens  atomic.Int64
	cacheTokens   atomic.Int64 // cache-read tokens (prompt-caching backends)
	startTime     time.Time
}

// NewSessionMetrics creates a SessionMetrics with startTime set to now.
func NewSessionMetrics() *SessionMetrics {
	return &SessionMetrics{startTime: time.Now()}
}

func (m *SessionMetrics) RecordModelCall()    { m.modelCalls.Add(1) }
func (m *SessionMetrics) RecordToolCall()     { m.toolCalls.Add(1) }
func (m *SessionMetrics) RecordCommandStep()  { m.commandSteps.Add(1) }
func (m *SessionMetrics) RecordSubagentCall() { m.subagentCalls.Add(1) }

// RecordTokens adds token counts from a single inference response.
// input is the uncached input, cache is the cache-read portion, output is the
// model output.
func (m *SessionMetrics) RecordTokens(input, cache, output int) {
	m.inputTokens.Add(int64(input))
	m.cacheTokens.Add(int64(cache))
	m.outputTokens.Add(int64(output))
}

// Snapshot returns a point-in-time copy of the metrics values.
func (m *SessionMetrics) Snapshot() SessionMetricsSnapshot {
	return SessionMetricsSnapshot{
		ModelCalls:    int(m.modelCalls.Load()),
		ToolCalls:     int(m.toolCalls.Load()),
		CommandSteps:  int(m.commandSteps.Load()),
		SubagentCalls: int(m.subagentCalls.Load()),
		InputTokens:   int(m.inputTokens.Load()),
		OutputTokens:  int(m.outputTokens.Load()),
		CacheTokens:   int(m.cacheTokens.Load()),
		Duration:      time.Since(m.startTime),
	}
}

// SnapshotPtr returns a pointer to a snapshot, or nil when m is nil.
// Useful for callers that hold a nullable *SessionMetrics and need to
// return a nullable *SessionMetricsSnapshot without a separate nil guard.
func (m *SessionMetrics) SnapshotPtr() *SessionMetricsSnapshot {
	if m == nil {
		return nil
	}
	s := m.Snapshot()
	return &s
}

// SessionMetricsSnapshot is a value-type point-in-time copy of SessionMetrics.
type SessionMetricsSnapshot struct {
	ModelCalls    int
	ToolCalls     int
	CommandSteps  int
	SubagentCalls int
	InputTokens   int
	OutputTokens  int
	CacheTokens   int
	Duration      time.Duration
}

// String formats the snapshot for display in the /debug metrics command.
// Kept human-readable and concise — not injected into model prompts.
func (s SessionMetricsSnapshot) String() string {
	total := s.InputTokens + s.OutputTokens
	return fmt.Sprintf(
		"Session metrics\n"+
			"  model calls:    %d\n"+
			"  tool calls:     %d\n"+
			"  command steps:  %d\n"+
			"  subagent calls: %d\n"+
			"  input tokens:   %d\n"+
			"  cache tokens:   %d\n"+
			"  output tokens:  %d\n"+
			"  total tokens:   %d\n"+
			"  duration:       %s",
		s.ModelCalls,
		s.ToolCalls,
		s.CommandSteps,
		s.SubagentCalls,
		s.InputTokens,
		s.CacheTokens,
		s.OutputTokens,
		total,
		s.Duration.Round(time.Millisecond),
	)
}
