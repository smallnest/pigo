// This file implements structured telemetry collection for a loop run (可观测性
// ——结构化遥测采集). A lightweight accumulator observes the AgentEvents the loop
// already emits and folds them into a compact summary — per-tool wall-clock
// durations, turn count, truncation count, compaction count, and the latest
// context-utilization ratio — that is surfaced once at run end as a
// TelemetryEvent (just before agent_end).
//
// The design is deliberately additive: telemetry rides the existing AgentEvent
// family and the existing stream-json output path, so no new dependency
// (Prometheus/OTLP) is introduced and existing stream-json consumers that do
// not know the "telemetry" event type keep working unchanged. Collection is
// passive — observing an event never changes loop behavior.
package runtime

import (
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// telemetry is the per-run accumulator. Most fields are folded from events on
// the single runLoop goroutine, but tool_execution_* events fire from the
// parallel tool-batch goroutines (ExecuteToolCalls), so all mutation goes
// through mu.
type telemetry struct {
	mu              sync.Mutex
	turns           int
	truncationCount int
	compactionCount int

	// toolStarts maps an in-flight tool call id to the wall-clock time its
	// execution began, so the matching end event can compute a duration. Keying
	// by call id (not tool name) keeps parallel tool batches correct.
	toolStarts map[string]time.Time
	// toolTimings aggregates finished tool durations by tool name.
	toolTimings map[string]agentcore.ToolTiming

	// contextTokens / contextWindow capture the most recent context accounting so
	// the summary can report the latest utilization ratio. contextWindow == 0
	// means the window is unknown (utilization is then reported as 0).
	contextTokens int
	contextWindow int

	// now is the clock, injectable for deterministic tests. Defaults to
	// time.Now.
	now func() time.Time
}

// newTelemetry constructs an empty accumulator using the wall clock.
func newTelemetry() *telemetry {
	return &telemetry{
		toolStarts:  make(map[string]time.Time),
		toolTimings: make(map[string]agentcore.ToolTiming),
		now:         time.Now,
	}
}

// observe folds a single emitted event into the accumulator. It is a no-op for
// event types that carry no telemetry signal, and it never mutates the event.
func (t *telemetry) observe(ev agentcore.AgentEvent) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch e := ev.(type) {
	case agentcore.TurnStartEvent:
		t.turns++
	case agentcore.ToolExecutionStartEvent:
		t.toolStarts[e.ToolCallID] = t.now()
	case agentcore.ToolExecutionEndEvent:
		start, ok := t.toolStarts[e.ToolCallID]
		if !ok {
			return
		}
		delete(t.toolStarts, e.ToolCallID)
		elapsed := t.now().Sub(start).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
		agg := t.toolTimings[e.ToolName]
		agg.Count++
		agg.TotalMs += elapsed
		t.toolTimings[e.ToolName] = agg
	case agentcore.TurnEndEvent:
		if e.Message.StopReason == agentcore.StopReasonLength {
			t.truncationCount++
		}
	case agentcore.CompactionEvent:
		// Count only successful compactions; a failed one (ErrorMessage set) left
		// the context unchanged.
		if e.ErrorMessage == "" {
			t.compactionCount++
		}
	}
}

// recordContext captures the latest context-token usage and window so the
// summary can report the current utilization ratio. A non-positive window is
// treated as unknown.
func (t *telemetry) recordContext(tokens, window int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.contextTokens = tokens
	if window > 0 {
		t.contextWindow = window
	}
}

// summary materializes the accumulated metrics into a TelemetryEvent. The
// per-tool map is copied so the emitted event does not alias the accumulator's
// live state.
func (t *telemetry) summary() agentcore.TelemetryEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	timings := make(map[string]agentcore.ToolTiming, len(t.toolTimings))
	for name, v := range t.toolTimings {
		timings[name] = v
	}
	var utilization float64
	if t.contextWindow > 0 {
		utilization = float64(t.contextTokens) / float64(t.contextWindow)
	}
	return agentcore.TelemetryEvent{
		Turns:              t.turns,
		ToolDurationsMs:    timings,
		TruncationCount:    t.truncationCount,
		CompactionCount:    t.compactionCount,
		ContextUtilization: utilization,
		ContextTokens:      t.contextTokens,
		ContextWindow:      t.contextWindow,
	}
}
