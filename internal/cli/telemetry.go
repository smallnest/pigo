// This file implements the telemetry holder that retains per-run telemetry
// events (US-001, #291) and folds them into a cumulative accumulator for the
// /status command to display. It was moved verbatim from cmd/pigo
// (telemetry_state.go, US-013) into the shared cli layer so the status and repl
// subpackages share one holder type.
package cli

import (
	"github.com/smallnest/pigo/internal/agentcore"
)

// TelemetryHolder holds the most recent run's telemetry event and a cumulative
// accumulator that sums metrics across all runs in the session.
type TelemetryHolder struct {
	// last is the telemetry event from the most recent completed run, or nil if
	// no run has completed yet.
	last *agentcore.TelemetryEvent
	// cumulative is the accumulator that sums metrics across all runs.
	cumulative cumulativeTelemetry
}

// cumulativeTelemetry holds the accumulated metrics from all runs in the session.
type cumulativeTelemetry struct {
	// turns is the total number of turns across all runs.
	turns int
	// toolDurationsMs maps tool name to aggregated count and total milliseconds
	// across all runs.
	toolDurationsMs map[string]agentcore.ToolTiming
	// truncationCount is the total number of truncations across all runs.
	truncationCount int
	// compactionCount is the total number of compactions across all runs.
	compactionCount int
	// contextTokens is the most recent observed context token count.
	contextTokens int
	// contextWindow is the most recent observed context window size.
	contextWindow int
}

// NewTelemetryHolder creates a new, empty TelemetryHolder with no telemetry yet.
func NewTelemetryHolder() *TelemetryHolder {
	return &TelemetryHolder{
		last: nil,
		cumulative: cumulativeTelemetry{
			toolDurationsMs: make(map[string]agentcore.ToolTiming),
		},
	}
}

// Reset clears the holder to "no telemetry yet", as if the session had just
// started. It is called when switching sessions (/fork, /clone, /import).
func (h *TelemetryHolder) Reset() {
	h.last = nil
	h.cumulative = cumulativeTelemetry{
		toolDurationsMs: make(map[string]agentcore.ToolTiming),
	}
}

// Fold incorporates a new TelemetryEvent into the holder: it becomes the new
// "last run" event, and its metrics are added to the cumulative accumulator.
func (h *TelemetryHolder) Fold(ev agentcore.TelemetryEvent) {
	// Store the new event as the last run.
	h.last = &ev

	// Add its metrics to the cumulative accumulator.
	h.cumulative.turns += ev.Turns
	h.cumulative.truncationCount += ev.TruncationCount
	h.cumulative.compactionCount += ev.CompactionCount

	// Update the latest context tokens and window (always use the most recent).
	h.cumulative.contextTokens = ev.ContextTokens
	h.cumulative.contextWindow = ev.ContextWindow

	// Sum the per-tool timings.
	for name, timing := range ev.ToolDurationsMs {
		agg := h.cumulative.toolDurationsMs[name]
		agg.Count += timing.Count
		agg.TotalMs += timing.TotalMs
		h.cumulative.toolDurationsMs[name] = agg
	}
}

// HasTelemetry returns true if at least one run has completed and telemetry is
// available.
func (h *TelemetryHolder) HasTelemetry() bool {
	return h.last != nil
}

// Last returns the most recent TelemetryEvent, or nil if no run has completed.
func (h *TelemetryHolder) Last() *agentcore.TelemetryEvent {
	return h.last
}

// CumulativeTurns returns the total number of turns across all runs.
func (h *TelemetryHolder) CumulativeTurns() int {
	return h.cumulative.turns
}

// CumulativeTruncationCount returns the total number of truncations across all runs.
func (h *TelemetryHolder) CumulativeTruncationCount() int {
	return h.cumulative.truncationCount
}

// CumulativeCompactionCount returns the total number of compactions across all runs.
func (h *TelemetryHolder) CumulativeCompactionCount() int {
	return h.cumulative.compactionCount
}

// CumulativeToolDurations returns the aggregated tool timings across all runs.
func (h *TelemetryHolder) CumulativeToolDurations() map[string]agentcore.ToolTiming {
	// Return a copy to prevent mutation of the internal state.
	result := make(map[string]agentcore.ToolTiming, len(h.cumulative.toolDurationsMs))
	for name, timing := range h.cumulative.toolDurationsMs {
		result[name] = timing
	}
	return result
}

// LatestContextTokens returns the most recent observed context token count.
func (h *TelemetryHolder) LatestContextTokens() int {
	return h.cumulative.contextTokens
}

// LatestContextWindow returns the most recent observed context window size.
func (h *TelemetryHolder) LatestContextWindow() int {
	return h.cumulative.contextWindow
}

// CumulativeContextUtilization returns the latest utilization ratio, or 0 if no
// window is known.
func (h *TelemetryHolder) CumulativeContextUtilization() float64 {
	if h.cumulative.contextWindow <= 0 {
		return 0
	}
	return float64(h.cumulative.contextTokens) / float64(h.cumulative.contextWindow)
}
