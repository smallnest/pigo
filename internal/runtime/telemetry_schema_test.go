// Conformance tests for the telemetry export contract (issue #569): the
// canonical TelemetrySummary document and the stream-json telemetry envelope
// are both pinned against golden fixtures, and every field's JSON type is
// asserted explicitly, so neither surface can drift unnoticed.
package runtime

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

var updateGoldens = flag.Bool("update-telemetry-goldens", false, "rewrite the telemetry golden fixtures")

// goldenSummary is the fixed fixture event: every field populated, including
// multiple tools so the map projection is exercised.
var goldenEvent = agentcore.TelemetryEvent{
	Turns:              7,
	TruncationCount:    1,
	CompactionCount:    2,
	ContextTokens:      96_000,
	ContextWindow:      128_000,
	ContextUtilization: 0.75,
	ToolDurationsMs: map[string]agentcore.ToolTiming{
		"bash": {Count: 4, TotalMs: 1230},
		"read": {Count: 12, TotalMs: 87},
	},
}

func goldenPath(name string) string {
	return filepath.Join("testdata", "telemetry", name)
}

func writeOrCompare(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden fixture %s unreadable (run go test -update-telemetry-goldens): %v", path, err)
	}
	normalized := strings.TrimSpace(string(want))
	if normalized != string(got) {
		t.Fatalf("golden drift in %s:\n--- want ---\n%s\n--- got ---\n%s", path, normalized, got)
	}
}

// TestTelemetrySummaryGoldenConformance pins the canonical TelemetrySummary
// JSON document byte-for-byte.
func TestTelemetrySummaryGoldenConformance(t *testing.T) {
	doc := TelemetrySummaryFromEvent(goldenEvent)
	got, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeOrCompare(t, goldenPath("summary-v1.golden"), got)
}

// TestTelemetrySummaryFieldTypes decodes the document as a generic map and
// asserts each field's JSON type explicitly, so a Go type change that silently
// alters the wire shape (e.g. int → string) fails even if key names match.
func TestTelemetrySummaryFieldTypes(t *testing.T) {
	raw, err := json.Marshal(TelemetrySummaryFromEvent(goldenEvent))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	stringFields := []string{"schema_version"}
	for _, f := range stringFields {
		if _, ok := doc[f].(string); !ok {
			t.Errorf("field %q = %T, want string", f, doc[f])
		}
	}
	if doc["schema_version"] != TelemetrySchemaVersion {
		t.Errorf("schema_version = %v, want %q", doc["schema_version"], TelemetrySchemaVersion)
	}
	intFields := []string{"turns", "truncation_count", "compaction_count", "context_tokens", "context_window"}
	for _, f := range intFields {
		if _, ok := doc[f].(float64); !ok {
			t.Errorf("field %q = %T, want number", f, doc[f])
		}
	}
	if _, ok := doc["context_utilization"].(float64); !ok {
		t.Errorf("context_utilization = %T, want number", doc["context_utilization"])
	}
	tools, ok := doc["tool_durations_ms"].(map[string]any)
	if !ok {
		t.Fatalf("tool_durations_ms = %T, want object", doc["tool_durations_ms"])
	}
	for name, entry := range tools {
		obj, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("tool %q entry = %T, want object", name, entry)
		}
		if _, ok := obj["count"].(float64); !ok {
			t.Errorf("tool %q count = %T, want number", name, obj["count"])
		}
		if _, ok := obj["total_ms"].(float64); !ok {
			t.Errorf("tool %q total_ms = %T, want number", name, obj["total_ms"])
		}
	}
}

// TestStreamJSONTelemetryEnvelopeGolden pins the existing stream-json
// telemetry event (camelCase, consumer-facing) byte-for-byte: the historical
// shape is now part of the frozen contract.
func TestStreamJSONTelemetryEnvelopeGolden(t *testing.T) {
	env := eventEnvelope(goldenEvent)
	got, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeOrCompare(t, goldenPath("stream-json-telemetry-v1.golden"), got)
}
