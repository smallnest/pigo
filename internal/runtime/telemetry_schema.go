// This file freezes pigo's telemetry export contract (issue #569), aligning
// the export story with pi-telemetry's goal — a vendor-neutral, typed,
// conformance-tested schema — scoped to pigo's run-summary model:
//
//   - TelemetrySummary is the canonical run-level export document, JSON-tagged
//     with snake_case names and a schema_version discriminator. Consumers can
//     decode it without knowing pigo's Go types, and the golden conformance
//     test (telemetry_schema_test.go + testdata/telemetry) fails the build if
//     the shape or the stream-json envelope drifts.
//   - The stream-json "telemetry" event envelope (eventEnvelope in
//     headless.go) keeps its historical camelCase field names — existing
//     consumers depend on them — and is now pinned by the same golden
//     machinery, so it cannot change unnoticed either.
//
// Both documents derive from the single accumulator (telemetry) and its
// TelemetryEvent, so there is exactly one source of truth with two frozen
// projections.
package runtime

import (
	"encoding/json"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TelemetrySchemaVersion is the discriminant stamped on every
// TelemetrySummary document. A breaking shape change MUST bump it (v2) and
// regenerate the golden fixtures.
const TelemetrySchemaVersion = "pigo.telemetry/v1"

// ToolTimingSummary is one tool's aggregated timing in the export schema.
type ToolTimingSummary struct {
	Count   int   `json:"count"`
	TotalMs int64 `json:"total_ms"`
}

// TelemetrySummary is the stable, run-level telemetry export document.
type TelemetrySummary struct {
	SchemaVersion      string                       `json:"schema_version"`
	Turns              int                          `json:"turns"`
	TruncationCount    int                          `json:"truncation_count"`
	CompactionCount    int                          `json:"compaction_count"`
	ContextTokens      int                          `json:"context_tokens"`
	ContextWindow      int                          `json:"context_window"`
	ContextUtilization float64                      `json:"context_utilization"`
	ToolDurations      map[string]ToolTimingSummary `json:"tool_durations_ms"`
}

// TelemetrySummaryFromEvent maps the accumulator's TelemetryEvent onto the
// frozen export document. It is pure: the same event always yields the same
// document (and thus the same golden fixture).
func TelemetrySummaryFromEvent(e agentcore.TelemetryEvent) TelemetrySummary {
	tools := make(map[string]ToolTimingSummary, len(e.ToolDurationsMs))
	for name, t := range e.ToolDurationsMs {
		tools[name] = ToolTimingSummary{Count: t.Count, TotalMs: t.TotalMs}
	}
	return TelemetrySummary{
		SchemaVersion:      TelemetrySchemaVersion,
		Turns:              e.Turns,
		TruncationCount:    e.TruncationCount,
		CompactionCount:    e.CompactionCount,
		ContextTokens:      e.ContextTokens,
		ContextWindow:      e.ContextWindow,
		ContextUtilization: e.ContextUtilization,
		ToolDurations:      tools,
	}
}

// MarshalJSON guarantees the document always carries the schema_version
// discriminator even when constructed directly rather than through
// TelemetrySummaryFromEvent.
func (s TelemetrySummary) MarshalJSON() ([]byte, error) {
	if s.SchemaVersion == "" {
		s.SchemaVersion = TelemetrySchemaVersion
	}
	type alias TelemetrySummary
	return json.Marshal(alias(s))
}
