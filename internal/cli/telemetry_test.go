package cli

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestTelemetryHolderReset verifies that Reset clears the holder to "no telemetry yet".
func TestTelemetryHolderReset(t *testing.T) {
	h := NewTelemetryHolder()
	if h.HasTelemetry() {
		t.Fatal("expected HasTelemetry() = false on new holder")
	}

	// Fold an event and verify it's present.
	h.Fold(agentcore.TelemetryEvent{
		Turns:           1,
		TruncationCount: 1,
		CompactionCount: 1,
		ContextTokens:   100,
		ContextWindow:   1000,
	})
	if !h.HasTelemetry() {
		t.Fatal("expected HasTelemetry() = true after Fold")
	}
	if h.Last() == nil {
		t.Fatal("expected Last() != nil after Fold")
	}
	if h.CumulativeTurns() != 1 {
		t.Fatalf("expected CumulativeTurns() = 1, got %d", h.CumulativeTurns())
	}

	// Reset and verify it's cleared.
	h.Reset()
	if h.HasTelemetry() {
		t.Fatal("expected HasTelemetry() = false after Reset")
	}
	if h.Last() != nil {
		t.Fatal("expected Last() = nil after Reset")
	}
	if h.CumulativeTurns() != 0 {
		t.Fatalf("expected CumulativeTurns() = 0 after Reset, got %d", h.CumulativeTurns())
	}
	if h.CumulativeTruncationCount() != 0 {
		t.Fatalf("expected CumulativeTruncationCount() = 0 after Reset, got %d", h.CumulativeTruncationCount())
	}
	if h.CumulativeCompactionCount() != 0 {
		t.Fatalf("expected CumulativeCompactionCount() = 0 after Reset, got %d", h.CumulativeCompactionCount())
	}
	if h.LatestContextTokens() != 0 {
		t.Fatalf("expected LatestContextTokens() = 0 after Reset, got %d", h.LatestContextTokens())
	}
	if h.LatestContextWindow() != 0 {
		t.Fatalf("expected LatestContextWindow() = 0 after Reset, got %d", h.LatestContextWindow())
	}
}

// TestTelemetryHolderFoldTwoEvents verifies that folding two synthetic TelemetryEvents
// sums the metrics correctly.
func TestTelemetryHolderFoldTwoEvents(t *testing.T) {
	h := NewTelemetryHolder()

	// Fold first event.
	h.Fold(agentcore.TelemetryEvent{
		Turns:           2,
		TruncationCount: 1,
		CompactionCount: 3,
		ContextTokens:   100,
		ContextWindow:   1000,
		ToolDurationsMs: map[string]agentcore.ToolTiming{
			"foo": {Count: 1, TotalMs: 100},
			"bar": {Count: 2, TotalMs: 200},
		},
	})

	// Verify the first event is the last one.
	if !h.HasTelemetry() {
		t.Fatal("expected HasTelemetry() = true after first Fold")
	}
	last1 := h.Last()
	if last1 == nil {
		t.Fatal("expected Last() != nil after first Fold")
	}
	if last1.Turns != 2 {
		t.Fatalf("expected Last().Turns = 2, got %d", last1.Turns)
	}

	// Verify cumulative after first event.
	if h.CumulativeTurns() != 2 {
		t.Fatalf("expected CumulativeTurns() = 2 after first Fold, got %d", h.CumulativeTurns())
	}
	if h.CumulativeTruncationCount() != 1 {
		t.Fatalf("expected CumulativeTruncationCount() = 1 after first Fold, got %d", h.CumulativeTruncationCount())
	}
	if h.CumulativeCompactionCount() != 3 {
		t.Fatalf("expected CumulativeCompactionCount() = 3 after first Fold, got %d", h.CumulativeCompactionCount())
	}
	if h.LatestContextTokens() != 100 {
		t.Fatalf("expected LatestContextTokens() = 100 after first Fold, got %d", h.LatestContextTokens())
	}
	if h.LatestContextWindow() != 1000 {
		t.Fatalf("expected LatestContextWindow() = 1000 after first Fold, got %d", h.LatestContextWindow())
	}
	tools1 := h.CumulativeToolDurations()
	if tools1["foo"].Count != 1 || tools1["foo"].TotalMs != 100 {
		t.Fatalf("expected foo tool timing {1, 100}, got %+v", tools1["foo"])
	}
	if tools1["bar"].Count != 2 || tools1["bar"].TotalMs != 200 {
		t.Fatalf("expected bar tool timing {2, 200}, got %+v", tools1["bar"])
	}

	// Fold second event.
	h.Fold(agentcore.TelemetryEvent{
		Turns:           3,
		TruncationCount: 2,
		CompactionCount: 1,
		ContextTokens:   150,
		ContextWindow:   1000,
		ToolDurationsMs: map[string]agentcore.ToolTiming{
			"foo": {Count: 2, TotalMs: 250},
			"baz": {Count: 1, TotalMs: 50},
		},
	})

	// Verify the second event is now the last one.
	last2 := h.Last()
	if last2 == nil {
		t.Fatal("expected Last() != nil after second Fold")
	}
	if last2.Turns != 3 {
		t.Fatalf("expected Last().Turns = 3, got %d", last2.Turns)
	}

	// Verify cumulative after second event (sums of both).
	if h.CumulativeTurns() != 5 {
		t.Fatalf("expected CumulativeTurns() = 5 after second Fold, got %d", h.CumulativeTurns())
	}
	if h.CumulativeTruncationCount() != 3 {
		t.Fatalf("expected CumulativeTruncationCount() = 3 after second Fold, got %d", h.CumulativeTruncationCount())
	}
	if h.CumulativeCompactionCount() != 4 {
		t.Fatalf("expected CumulativeCompactionCount() = 4 after second Fold, got %d", h.CumulativeCompactionCount())
	}
	if h.LatestContextTokens() != 150 {
		t.Fatalf("expected LatestContextTokens() = 150 after second Fold, got %d", h.LatestContextTokens())
	}
	if h.LatestContextWindow() != 1000 {
		t.Fatalf("expected LatestContextWindow() = 1000 after second Fold, got %d", h.LatestContextWindow())
	}
	tools2 := h.CumulativeToolDurations()
	if tools2["foo"].Count != 3 || tools2["foo"].TotalMs != 350 {
		t.Fatalf("expected foo tool timing {3, 350} after sum, got %+v", tools2["foo"])
	}
	if tools2["bar"].Count != 2 || tools2["bar"].TotalMs != 200 {
		t.Fatalf("expected bar tool timing {2, 200} unchanged, got %+v", tools2["bar"])
	}
	if tools2["baz"].Count != 1 || tools2["baz"].TotalMs != 50 {
		t.Fatalf("expected baz tool timing {1, 50} after sum, got %+v", tools2["baz"])
	}
}

// TestTelemetryHolderCumulativeContextUtilization verifies context utilization calculation.
func TestTelemetryHolderCumulativeContextUtilization(t *testing.T) {
	h := NewTelemetryHolder()

	// With unknown window (0), utilization should be 0.
	h.Fold(agentcore.TelemetryEvent{
		ContextTokens: 100,
		ContextWindow: 0,
	})
	if h.CumulativeContextUtilization() != 0 {
		t.Fatalf("expected utilization = 0 with unknown window, got %f", h.CumulativeContextUtilization())
	}

	// With known window, utilization should be tokens/window.
	h.Fold(agentcore.TelemetryEvent{
		ContextTokens: 500,
		ContextWindow: 1000,
	})
	if h.CumulativeContextUtilization() != 0.5 {
		t.Fatalf("expected utilization = 0.5, got %f", h.CumulativeContextUtilization())
	}
}

// TestTelemetryHolderToolDurationsImmutable verifies that CumulativeToolDurations returns
// a copy to prevent external mutation.
func TestTelemetryHolderToolDurationsImmutable(t *testing.T) {
	h := NewTelemetryHolder()
	h.Fold(agentcore.TelemetryEvent{
		ToolDurationsMs: map[string]agentcore.ToolTiming{
			"test": {Count: 1, TotalMs: 100},
		},
	})

	// Get the tool durations and mutate the returned map.
	tools := h.CumulativeToolDurations()
	tools["test"] = agentcore.ToolTiming{Count: 999, TotalMs: 9999}
	tools["new"] = agentcore.ToolTiming{Count: 1, TotalMs: 1}

	// Verify the internal state was not modified.
	tools2 := h.CumulativeToolDurations()
	if tools2["test"].Count != 1 || tools2["test"].TotalMs != 100 {
		t.Fatalf("expected internal tool timing to remain {1, 100}, got %+v", tools2["test"])
	}
	if _, ok := tools2["new"]; ok {
		t.Fatal("expected 'new' tool not to be present in internal state")
	}
}
