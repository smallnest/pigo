package runtime

import (
	"context"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestAgentLoopOnStopBlocksThenAllows: OnStop blocks the natural end twice
// (injecting guidance each time) then allows it, so the run takes three turns
// and the guidance messages land in the context.
func TestAgentLoopOnStopBlocksThenAllows(t *testing.T) {
	cfg := newRunCfg(scriptedStream(nil)) // always a natural end_turn
	blocks := 0
	cfg.OnStop = func(ctx context.Context, agentCtx *agentcore.AgentContext) *StopDecision {
		if blocks < 2 {
			blocks++
			return &StopDecision{Block: true, Guidance: "keep going"}
		}
		return nil
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, _ := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	if got := countKind(kinds, agentcore.EventTurnStart); got != 3 {
		t.Fatalf("expected 3 turns (2 forced continuations + final), got %d (%v)", got, kinds)
	}
	if kinds[len(kinds)-1] != agentcore.EventAgentEnd {
		t.Fatalf("run must end with agent_end, got %v", kinds)
	}
	guidance := 0
	for _, m := range agentCtx.Messages {
		if um, ok := m.(agentcore.UserMessage); ok && len(um.Content) == 1 {
			if tc, ok := um.Content[0].(agentcore.TextContent); ok && tc.Text == "keep going" {
				guidance++
			}
		}
	}
	if guidance != 2 {
		t.Fatalf("expected 2 injected guidance messages, got %d", guidance)
	}
}

// TestAgentLoopOnStopNilEndsRun: a nil OnStop decision lets the run end after a
// single natural turn.
func TestAgentLoopOnStopNilEndsRun(t *testing.T) {
	cfg := newRunCfg(scriptedStream(nil))
	consulted := false
	cfg.OnStop = func(ctx context.Context, agentCtx *agentcore.AgentContext) *StopDecision {
		consulted = true
		return nil
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, _ := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	if !consulted {
		t.Fatal("OnStop was never consulted")
	}
	if got := countKind(kinds, agentcore.EventTurnStart); got != 1 {
		t.Fatalf("nil decision must end after one turn, got %d turns", got)
	}
}
