package compaction

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// bigUser builds a user message whose estimate is ~tokens tokens (4 chars each).
func bigUser(tokens int) agentcore.UserMessage {
	return userMsg(strings.Repeat("x", tokens*4))
}

func assistantToolCall(id, name string) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewToolCallContent(id, name, nil)},
	}
}

func toolResult(id string) agentcore.ToolResultMessage {
	return agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: id,
		Content:    agentcore.ContentList{agentcore.NewTextContent("result")},
	}
}

func TestFindCutPointNoValidCutPoint(t *testing.T) {
	// Only toolResult messages -> no valid cut point.
	msgs := []agentcore.Message{toolResult("a"), toolResult("b")}
	got := FindCutPoint(msgs, 100)
	if got.FirstKeptIndex != 0 || got.TurnStartIndex != -1 || got.IsSplitTurn {
		t.Fatalf("no-cutpoint: got %+v, want {0 -1 false}", got)
	}
}

func TestFindCutPointOnTurnBoundary(t *testing.T) {
	// Three complete user/assistant turns, each user ~100 tokens.
	// keepRecentTokens small so the cut lands on a clean user boundary.
	msgs := []agentcore.Message{
		bigUser(100),                // 0
		assistantMsg("a1", nil, ""), // 1
		bigUser(100),                // 2
		assistantMsg("a2", nil, ""), // 3
		bigUser(100),                // 4
		assistantMsg("a3", nil, ""), // 5
	}
	// keepRecentTokens=50: walking back, msg[5] tiny, msg[4]=100 >= 50 at i=4.
	// nearest cut point >= 4 is index 4 (a user message) -> clean boundary.
	got := FindCutPoint(msgs, 50)
	if got.FirstKeptIndex != 4 {
		t.Fatalf("FirstKeptIndex: got %d, want 4", got.FirstKeptIndex)
	}
	if got.IsSplitTurn {
		t.Fatalf("IsSplitTurn: got true, want false (landed on user boundary)")
	}
	if got.TurnStartIndex != -1 {
		t.Fatalf("TurnStartIndex: got %d, want -1", got.TurnStartIndex)
	}
}

func TestFindCutPointSplitTurn(t *testing.T) {
	// A turn starting with a user message, a big assistant reply, a toolCall and
	// its toolResult. If the budget forces the cut onto the assistant message
	// mid-turn, it's a split turn.
	msgs := []agentcore.Message{
		userMsg("older turn"),                           // 0
		assistantMsg("older reply", nil, ""),            // 1
		userMsg("current turn start"),                   // 2 user
		assistantMsg(strings.Repeat("y", 400), nil, ""), // 3 assistant ~100 tokens
		assistantToolCall("t1", "read"),                 // 4 assistant (valid cut)
		toolResult("t1"),                                // 5 toolResult (not cuttable)
	}
	// keepRecentTokens=50: from end, msg[5]=~2, msg[4]~1, msg[3]=100 >= 50 at i=3.
	// nearest cut point >= 3 is index 3 (assistant) -> split turn, turn start=2.
	got := FindCutPoint(msgs, 50)
	if got.FirstKeptIndex != 3 {
		t.Fatalf("FirstKeptIndex: got %d, want 3", got.FirstKeptIndex)
	}
	if !got.IsSplitTurn {
		t.Fatalf("IsSplitTurn: got false, want true (cut on assistant mid-turn)")
	}
	if got.TurnStartIndex != 2 {
		t.Fatalf("TurnStartIndex: got %d, want 2", got.TurnStartIndex)
	}
}

func TestFindCutPointNeverCutsOnToolResult(t *testing.T) {
	// Ensure a toolResult is never chosen even when it's the message where the
	// budget is reached.
	msgs := []agentcore.Message{
		userMsg("u0"),                   // 0
		assistantToolCall("t1", "grep"), // 1
		toolResult("t1"),                // 2 (budget could land here)
		assistantMsg("done", nil, ""),   // 3
	}
	got := FindCutPoint(msgs, 1) // tiny budget: reached at msg[3]
	// cut must be a valid point (>=3 is index 3 assistant); never index 2.
	if got.FirstKeptIndex == 2 {
		t.Fatalf("cut landed on toolResult (index 2), which is illegal")
	}
	if msgs[got.FirstKeptIndex].Role() == agentcore.RoleToolResult {
		t.Fatalf("FirstKeptIndex points at a toolResult: %d", got.FirstKeptIndex)
	}
}

func TestFindCutPointBudgetNeverReachedKeepsEarliest(t *testing.T) {
	msgs := []agentcore.Message{
		userMsg("u0"),               // 0
		assistantMsg("a0", nil, ""), // 1
	}
	// Huge budget -> never reached -> keep from earliest valid cut point (0).
	got := FindCutPoint(msgs, 1_000_000)
	if got.FirstKeptIndex != 0 {
		t.Fatalf("FirstKeptIndex: got %d, want 0 (earliest cut point)", got.FirstKeptIndex)
	}
}
