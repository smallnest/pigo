package runtime

// Tests for structured telemetry collection (可观测性——结构化遥测采集, node-251):
// the loop accumulates per-tool durations, turn count, truncation count,
// compaction count, and the latest context-utilization ratio, then surfaces
// them as a TelemetryEvent emitted just before agent_end. Both the unit-level
// accumulator and the end-to-end loop wiring (including the stream-json
// headless surface a script reads) are covered.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
)

// fakeClock returns a now() closure that advances by step on every call, so a
// tool_execution_start/end pair yields a deterministic non-zero duration.
func fakeClock(start time.Time, step time.Duration) func() time.Time {
	cur := start
	return func() time.Time {
		t := cur
		cur = cur.Add(step)
		return t
	}
}

// findTelemetry returns the first TelemetryEvent emitted, or nil.
func findTelemetry(events []agentcore.AgentEvent) *agentcore.TelemetryEvent {
	for _, ev := range events {
		if te, ok := ev.(agentcore.TelemetryEvent); ok {
			return &te
		}
	}
	return nil
}

// TestTelemetryObserveToolTiming verifies a start/end pair records an aggregated
// per-tool duration, and repeated calls accumulate count and total.
func TestTelemetryObserveToolTiming(t *testing.T) {
	tel := newTelemetry()
	tel.now = fakeClock(time.Unix(0, 0), 5*time.Millisecond)

	// Two invocations of "echo": each start advances the clock 5ms, each end
	// advances another 5ms, so each invocation measures 5ms.
	for _, id := range []string{"c1", "c2"} {
		tel.observe(agentcore.ToolExecutionStartEvent{ToolCallID: id, ToolName: "echo"})
		tel.observe(agentcore.ToolExecutionEndEvent{ToolCallID: id, ToolName: "echo"})
	}

	sum := tel.summary()
	got, ok := sum.ToolDurationsMs["echo"]
	if !ok {
		t.Fatalf("expected timing for tool echo, got %+v", sum.ToolDurationsMs)
	}
	if got.Count != 2 {
		t.Errorf("echo count = %d, want 2", got.Count)
	}
	if got.TotalMs != 10 {
		t.Errorf("echo totalMs = %d, want 10", got.TotalMs)
	}
}

// TestTelemetryObserveCounters verifies turn/truncation/compaction counters and
// that a failed compaction is not counted.
func TestTelemetryObserveCounters(t *testing.T) {
	tel := newTelemetry()
	tel.observe(agentcore.TurnStartEvent{})
	tel.observe(agentcore.TurnStartEvent{})
	tel.observe(agentcore.TurnEndEvent{Message: agentcore.AssistantMessage{StopReason: agentcore.StopReasonLength}})
	tel.observe(agentcore.TurnEndEvent{Message: agentcore.AssistantMessage{StopReason: agentcore.StopReasonEndTurn}})
	tel.observe(agentcore.CompactionEvent{}) // success
	tel.observe(agentcore.CompactionEvent{ErrorMessage: "boom"}) // failure, not counted

	sum := tel.summary()
	if sum.Turns != 2 {
		t.Errorf("turns = %d, want 2", sum.Turns)
	}
	if sum.TruncationCount != 1 {
		t.Errorf("truncationCount = %d, want 1", sum.TruncationCount)
	}
	if sum.CompactionCount != 1 {
		t.Errorf("compactionCount = %d, want 1 (failed compaction must not count)", sum.CompactionCount)
	}
}

// TestTelemetryContextUtilization verifies the ratio is used/window and is 0
// when the window is unknown.
func TestTelemetryContextUtilization(t *testing.T) {
	tel := newTelemetry()
	tel.recordContext(500, 2000)
	if got := tel.summary().ContextUtilization; got != 0.25 {
		t.Errorf("utilization = %v, want 0.25", got)
	}

	unknown := newTelemetry()
	unknown.recordContext(500, 0)
	if got := unknown.summary().ContextUtilization; got != 0 {
		t.Errorf("utilization with unknown window = %v, want 0", got)
	}
}

// TestLoopEmitsTelemetryBeforeAgentEnd verifies the loop emits exactly one
// telemetry event immediately before agent_end and that it captures a tool
// execution.
func TestLoopEmitsTelemetryBeforeAgentEnd(t *testing.T) {
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "echo"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("done")}},
	}), echoTool("echo", agentcore.ToolExecutionParallel, false))
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	events := collectEvents(t, agentLoop(context.Background(), agentCtx, cfg))

	te := findTelemetry(events)
	if te == nil {
		t.Fatalf("expected a TelemetryEvent, got %+v", eventKinds(events))
	}
	// Telemetry must be the penultimate event, immediately before agent_end.
	if n := len(events); n < 2 || events[n-1].EventType() != agentcore.EventAgentEnd || events[n-2].EventType() != agentcore.EventTelemetry {
		t.Errorf("telemetry must be emitted just before agent_end, got %v", eventKinds(events))
	}
	if te.Turns != 2 {
		t.Errorf("telemetry turns = %d, want 2", te.Turns)
	}
	if _, ok := te.ToolDurationsMs["echo"]; !ok {
		t.Errorf("telemetry should record the echo tool timing, got %+v", te.ToolDurationsMs)
	}
	if te.ToolDurationsMs["echo"].Count != 1 {
		t.Errorf("echo count = %d, want 1", te.ToolDurationsMs["echo"].Count)
	}
}

// TestLoopTelemetryCountsCompaction verifies a compaction that fires during the
// run increments the telemetry compaction counter and records a non-zero
// context-utilization ratio.
func TestLoopTelemetryCountsCompaction(t *testing.T) {
	main := scriptedStream([]agentcore.AssistantMessage{
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("ok")}},
	})
	cfg := newRunCfg(main)
	cfg.SummaryStream = summaryStream("## Goal\ncompacted")
	cfg.ContextWindow = 2000
	cfg.Compaction = compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100}
	agentCtx := &agentcore.AgentContext{Messages: bigUserMessages(12, 800)}

	events := collectEvents(t, agentLoop(context.Background(), agentCtx, cfg))
	te := findTelemetry(events)
	if te == nil {
		t.Fatal("expected a TelemetryEvent")
	}
	if te.CompactionCount != 1 {
		t.Errorf("telemetry compactionCount = %d, want 1", te.CompactionCount)
	}
	if te.ContextWindow != 2000 {
		t.Errorf("telemetry contextWindow = %d, want 2000", te.ContextWindow)
	}
	if te.ContextUtilization <= 0 {
		t.Errorf("telemetry contextUtilization = %v, want > 0", te.ContextUtilization)
	}
}

// TestLoopTelemetryCountsTruncation verifies a length-truncated assistant
// response increments the telemetry truncation counter.
func TestLoopTelemetryCountsTruncation(t *testing.T) {
	truncated := oneToolAssistant("c1", "echo")
	truncated.StopReason = agentcore.StopReasonLength
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		truncated,
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn},
	}), echoTool("echo", agentcore.ToolExecutionParallel, false))
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	events := collectEvents(t, agentLoop(context.Background(), agentCtx, cfg))
	te := findTelemetry(events)
	if te == nil {
		t.Fatal("expected a TelemetryEvent")
	}
	if te.TruncationCount != 1 {
		t.Errorf("telemetry truncationCount = %d, want 1", te.TruncationCount)
	}
}

// TestTelemetryEventEnvelope verifies the stream-json envelope carries every
// telemetry field, including a name-keyed per-tool timings object, so a script
// can read the metrics directly (headless surface, acceptance criterion 3).
func TestTelemetryEventEnvelope(t *testing.T) {
	env := eventEnvelope(agentcore.TelemetryEvent{
		Turns:              3,
		TruncationCount:    1,
		CompactionCount:    2,
		ContextUtilization: 0.5,
		ContextTokens:      1000,
		ContextWindow:      2000,
		ToolDurationsMs:    map[string]agentcore.ToolTiming{"echo": {Count: 2, TotalMs: 40}},
	})
	if env["type"] != agentcore.EventTelemetry {
		t.Errorf("type = %v, want %q", env["type"], agentcore.EventTelemetry)
	}
	if env["turns"] != 3 || env["truncationCount"] != 1 || env["compactionCount"] != 2 {
		t.Errorf("counter fields wrong: %+v", env)
	}
	if env["contextUtilization"] != 0.5 || env["contextTokens"] != 1000 || env["contextWindow"] != 2000 {
		t.Errorf("context fields wrong: %+v", env)
	}
	tools, ok := env["toolDurationsMs"].(map[string]map[string]any)
	if !ok {
		t.Fatalf("toolDurationsMs type = %T, want map[string]map[string]any", env["toolDurationsMs"])
	}
	if tools["echo"]["count"] != 2 || tools["echo"]["totalMs"] != int64(40) {
		t.Errorf("echo timing wrong: %+v", tools["echo"])
	}
}

// TestHeadlessStreamJSONSurfacesTelemetry verifies the run-end telemetry summary
// reaches the stream-json headless output as a parseable JSON line — the
// script-readable metric surface required by acceptance criterion 3.
func TestHeadlessStreamJSONSurfacesTelemetry(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "echo", `{"msg":"hi"}`),
			textTurn("done"),
		},
	}
	cfg := newFauxRunCfg(p, echoTool("echo", agentcore.ToolExecutionParallel, false))
	var out bytes.Buffer
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}}}}

	if err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Mode: StreamJSONMode, Out: &out}); err != nil {
		t.Fatalf("RunHeadless stream-json: %v", err)
	}

	var telemetryLine map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("stream-json line not valid JSON: %q (%v)", line, err)
		}
		if env["type"] == agentcore.EventTelemetry {
			telemetryLine = env
		}
	}
	if telemetryLine == nil {
		t.Fatal("stream-json output must contain a telemetry event a script can read")
	}
	// turns is a JSON number; a script would read it as float64.
	if turns, ok := telemetryLine["turns"].(float64); !ok || turns < 2 {
		t.Errorf("telemetry turns = %v, want >= 2", telemetryLine["turns"])
	}
	tools, ok := telemetryLine["toolDurationsMs"].(map[string]any)
	if !ok || tools["echo"] == nil {
		t.Errorf("telemetry must report the echo tool timing, got %v", telemetryLine["toolDurationsMs"])
	}
}

// eventKinds maps events to their type strings for readable failure output.
func eventKinds(events []agentcore.AgentEvent) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.EventType()
	}
	return out
}
