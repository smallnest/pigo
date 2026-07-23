package runtime

// End-to-end robustness verification scenarios (US-007 / FR-10): the harness's
// two self-protection mechanisms must fire correctly when driven through the
// real provider seam with NO external LLM.
//
//   - LONG SESSION: a run whose accumulated context exceeds the usable window
//     (ContextWindow − ReserveTokens) must auto-compact in place, emitting a
//     successful CompactionEvent, and still finish normally.
//   - LARGE OUTPUT: a tool that returns far more than the executor-layer byte
//     budget (toolResultMaxBytes = 100_000) must have its result truncated with
//     the shared "[truncated" annotation, so a single fat tool result cannot
//     overflow the model context, and the run still finishes.
//
// Both scenarios reuse the existing in-repo test infrastructure only: the faux
// provider seam (StreamFnFromProvider via newFauxRunCfg / toolCallTurn / textTurn),
// the scripted StreamFn (scriptedStream / newRunCfg / summaryStream), and the
// event collectors (collectEvents / collectStream / findCompaction). No new
// mocking is invented and no production (non-_test) code is touched.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
)

// TestE2E_LongSession_TriggersCompaction drives a long session whose seeded
// history already exceeds the usable window, forcing the loop to auto-compact
// after the first turn settles. It asserts a *successful* CompactionEvent is
// emitted (empty ErrorMessage), tokens shrink, a compaction checkpoint replaces
// the head of the context, and the run ends normally via agent_end.
func TestE2E_LongSession_TriggersCompaction(t *testing.T) {
	// Main stream just ends the turn; a separate summary stream stands in for the
	// summarization model so compaction does not consume main-stream turns.
	main := scriptedStream([]agentcore.AssistantMessage{
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("ack")}},
	})
	cfg := newRunCfg(main)
	cfg.SummaryStream = summaryStream("## Goal\nlong session compacted")
	// Deliberately tiny window so a handful of fat seeded messages exceed the
	// threshold (ContextWindow − ReserveTokens) the moment the first turn settles.
	cfg.ContextWindow = 2000
	cfg.Compaction = compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100}

	// Seed a long history: 16 messages × 800 chars each blows past the usable window.
	agentCtx := &agentcore.AgentContext{Messages: bigUserMessages(16, 800)}

	events := collectEvents(t, agentLoop(context.Background(), agentCtx, cfg))

	ce := findCompaction(events)
	if ce == nil {
		t.Fatalf("long session must trigger a CompactionEvent, got %v", eventKinds(events))
	}
	if ce.ErrorMessage != "" {
		t.Fatalf("compaction must succeed, got error %q", ce.ErrorMessage)
	}
	if ce.TokensAfter >= ce.TokensBefore {
		t.Errorf("compaction must reduce estimated tokens: before=%d after=%d", ce.TokensBefore, ce.TokensAfter)
	}
	if ce.SummarizedCount <= 0 {
		t.Errorf("compaction must fold at least one message into the summary, got %d", ce.SummarizedCount)
	}
	// The compacted context must begin with a compaction checkpoint.
	if len(agentCtx.Messages) == 0 || agentCtx.Messages[0].Role() != agentcore.RoleCompaction {
		t.Errorf("context must start with a compaction checkpoint after compaction, got %+v", agentCtx.Messages)
	}
	// The run must still terminate cleanly.
	if n := len(events); n == 0 || events[n-1].EventType() != agentcore.EventAgentEnd {
		t.Errorf("run must end with agent_end, got %v", eventKinds(events))
	}
}

// TestE2E_LargeOutput_TriggersTruncation drives a tool call whose tool returns
// output far larger than the executor-layer byte budget. It asserts the
// resulting tool-result content carries the shared "[truncated" annotation and
// is clipped well below the raw size (so the context cannot overflow from a
// single fat result), and the run still completes normally.
func TestE2E_LargeOutput_TriggersTruncation(t *testing.T) {
	// A payload well over toolResultMaxBytes (100_000). 250_000 bytes guarantees
	// the executor-layer budget bites regardless of any looser inner cap.
	const rawSize = 250_000
	huge := strings.Repeat("A", rawSize)

	// A tool that emits the oversized payload as a single text block.
	bigOutputTool := execTool{
		name: "flood",
		mode: agentcore.ToolExecutionParallel,
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(huge)}}, nil
		},
	}

	// Turn 1: call the flooding tool. Turn 2: end the turn with text.
	p := &fauxProvider{
		name: "faux",
		turns: []fauxTurn{
			toolCallTurn("call-flood", "flood", `{}`),
			textTurn("handled large output"),
		},
	}
	cfg := newFauxRunCfg(p, bigOutputTool)
	// A generous window: the point is that truncation keeps the result small
	// enough that the context does NOT overflow, so no compaction is needed.
	cfg.ContextWindow = 200_000
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("run flood")}}}}

	kinds, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	// Locate the flood tool result and assert it was truncated.
	var floodResult *agentcore.ToolResultMessage
	for i := range msgs {
		if tr, ok := msgs[i].(agentcore.ToolResultMessage); ok && tr.ToolCallID == "call-flood" {
			trCopy := tr
			floodResult = &trCopy
		}
	}
	if floodResult == nil {
		t.Fatalf("expected a tool result for the flood call, got %+v", msgs)
	}
	got := textContentOf(floodResult.Content)
	if !strings.Contains(got, "[truncated") {
		t.Errorf("large tool output must carry the \"[truncated\" annotation, got %d bytes without it", len(got))
	}
	// The clipped result must be far smaller than the raw payload — the context
	// protection actually reduced the size (it must not blow the 100_000 budget
	// wildly; allow generous headroom for head+tail+marker).
	if len(got) >= rawSize {
		t.Errorf("truncation must shrink the result: got %d bytes, raw was %d", len(got), rawSize)
	}
	if len(got) > 120_000 {
		t.Errorf("truncated result should be near the byte budget, got %d bytes", len(got))
	}

	// Context must not have overflowed: with truncation in place the tiny clipped
	// result never crosses the window, so no compaction should have fired.
	for _, ev := range kinds {
		if ev == agentcore.EventCompaction {
			t.Errorf("truncation should keep context under the window; no compaction expected, got kinds %v", kinds)
		}
	}
	// And the run finished cleanly.
	if len(kinds) == 0 || kinds[len(kinds)-1] != agentcore.EventAgentEnd {
		t.Errorf("run must end with agent_end, got %v", kinds)
	}
}
