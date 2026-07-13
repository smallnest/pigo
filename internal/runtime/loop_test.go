package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

// collectStream drains a LoopEventStream, returning the event types in order
// and the run result messages.
func collectStream(t *testing.T, s *LoopEventStream) ([]string, []agentcore.AgentMessage) {
	t.Helper()
	var kinds []string
	for ev := range s.Events() {
		kinds = append(kinds, ev.EventType())
	}
	msgs, err := s.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}
	return kinds, msgs
}

// oneToolAssistant builds an assistant message with a single tool call.
func oneToolAssistant(id, name string) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		StopReason: agentcore.StopReasonToolUse,
		Content:    agentcore.ContentList{agentcore.NewToolCallContent(id, name, json.RawMessage(`{}`))},
	}
}

// scriptedStream returns a StreamFn that emits one StreamDoneEvent per call,
// consuming msgs in order. Extra calls beyond msgs emit a plain end_turn.
func scriptedStream(msgs []agentcore.AssistantMessage) provider.StreamFn {
	i := 0
	return func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		var msg agentcore.AssistantMessage
		if i < len(msgs) {
			msg = msgs[i]
		} else {
			msg = agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn}
		}
		i++
		s := provider.NewAssistantMessageEventStream(0)
		go func() {
			_ = s.Emit(ctx, provider.StreamDoneEvent{Message: msg})
			s.Close()
		}()
		return s, nil
	}
}

func newRunCfg(stream provider.StreamFn, tools ...agentcore.AgentTool) RunConfig {
	reg := agenttool.NewToolRegistry()
	for _, tl := range tools {
		_ = reg.Register(tl)
	}
	return RunConfig{
		LoopConfig: LoopConfig{Model: "fake", Stream: stream},
		Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
	}
}

func TestAgentLoopNoToolCallsSingleTurn(t *testing.T) {
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
	}))
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	want := []string{agentcore.EventAgentStart, agentcore.EventTurnStart, agentcore.EventMessageEnd, agentcore.EventTurnEnd, agentcore.EventAgentEnd}
	assertEventKinds(t, kinds, want)
	if len(msgs) != 1 {
		t.Fatalf("run produced %d messages, want 1: %+v", len(msgs), msgs)
	}
}

func TestAgentLoopInnerLoopFeedsToolResults(t *testing.T) {
	// Turn 1: tool call. Turn 2: no tool call → inner loop ends.
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "echo"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("done")}},
	}), echoTool("echo", agentcore.ToolExecutionParallel, false))
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	// Two turns; a tool executed in the first.
	if countKind(kinds, agentcore.EventTurnStart) != 2 {
		t.Errorf("expected 2 turns, got kinds %v", kinds)
	}
	if countKind(kinds, agentcore.EventToolExecutionEnd) != 1 {
		t.Errorf("expected 1 tool execution, got kinds %v", kinds)
	}
	// Messages produced: assistant(tool) + toolResult + assistant(done) = 3.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 new messages, got %d: %+v", len(msgs), msgs)
	}
	if _, ok := msgs[1].(agentcore.ToolResultMessage); !ok {
		t.Errorf("expected message[1] to be a tool result, got %T", msgs[1])
	}
}

func TestAgentLoopFollowUpMessagesContinue(t *testing.T) {
	served := false
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("first")}},
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("second")}},
	}))
	cfg.GetFollowUpMessages = func(ctx context.Context, agentCtx *agentcore.AgentContext) []agentcore.AgentMessage {
		if served {
			return nil
		}
		served = true
		return []agentcore.AgentMessage{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("more")}}}
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, _ := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	if countKind(kinds, agentcore.EventTurnStart) != 2 {
		t.Errorf("follow-up should drive a second turn, got kinds %v", kinds)
	}
}

func TestAgentLoopShouldStopAfterTurn(t *testing.T) {
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "echo"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn},
	}), echoTool("echo", agentcore.ToolExecutionParallel, false))
	cfg.ShouldStopAfterTurn = func(ctx context.Context, agentCtx *agentcore.AgentContext) bool { return true }
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, _ := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	// Stops after the first turn_end, so only one turn.
	if countKind(kinds, agentcore.EventTurnStart) != 1 {
		t.Errorf("shouldStopAfterTurn=true must stop after one turn, got %v", kinds)
	}
	if kinds[len(kinds)-1] != agentcore.EventAgentEnd {
		t.Errorf("run must end with agent_end, got %v", kinds)
	}
}

func TestAgentLoopSteeringInjected(t *testing.T) {
	var injectedSeen bool
	steer := agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("steer")}}
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "echo"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn},
	}), echoTool("echo", agentcore.ToolExecutionParallel, false))
	pulled := false
	cfg.GetSteeringMessages = func(ctx context.Context) []agentcore.AgentMessage {
		if pulled {
			return nil
		}
		pulled = true
		return []agentcore.AgentMessage{steer}
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	for _, m := range agentCtx.Messages {
		if um, ok := m.(agentcore.UserMessage); ok && len(um.Content) == 1 {
			if tc, ok := um.Content[0].(agentcore.TextContent); ok && tc.Text == "steer" {
				injectedSeen = true
			}
		}
	}
	if !injectedSeen {
		t.Errorf("steering message was not injected into the context")
	}
}

func TestAgentLoopPrepareNextTurnSwapsModel(t *testing.T) {
	var seenModels []string
	streamFn := func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		seenModels = append(seenModels, model)
		var msg agentcore.AssistantMessage
		if len(seenModels) == 1 {
			msg = oneToolAssistant("c1", "echo")
		} else {
			msg = agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn}
		}
		s := provider.NewAssistantMessageEventStream(0)
		go func() { _ = s.Emit(ctx, provider.StreamDoneEvent{Message: msg}); s.Close() }()
		return s, nil
	}
	cfg := newRunCfg(streamFn, echoTool("echo", agentcore.ToolExecutionParallel, false))
	newModel := "swapped-model"
	cfg.PrepareNextTurn = func(ctx context.Context, agentCtx *agentcore.AgentContext) *TurnUpdate {
		return &TurnUpdate{Model: &newModel}
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	if len(seenModels) != 2 || seenModels[1] != newModel {
		t.Errorf("prepareNextTurn should swap model to %q, saw %v", newModel, seenModels)
	}
}

func TestAgentLoopLengthFailsToolCalls(t *testing.T) {
	// Turn 1: tool call but truncated (length). Turn 2: end.
	truncated := oneToolAssistant("c1", "echo")
	truncated.StopReason = agentcore.StopReasonLength
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		truncated,
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn},
	}), echoTool("echo", agentcore.ToolExecutionParallel, false))
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	// The tool must NOT have executed (truncated → failed instead).
	if countKind(kinds, agentcore.EventToolExecutionEnd) != 0 {
		t.Errorf("truncated message must not execute tools, got %v", kinds)
	}
	// A failed tool result must have been synthesized.
	var foundFail bool
	for _, m := range msgs {
		if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.IsError && tr.ToolCallID == "c1" {
			foundFail = true
		}
	}
	if !foundFail {
		t.Errorf("expected a synthesized failed tool result for the truncated call")
	}
}

func TestAgentLoopErrorStopEndsRun(t *testing.T) {
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonError, ErrorMessage: "boom"},
	}))
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, _ := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	if countKind(kinds, agentcore.EventTurnStart) != 1 {
		t.Errorf("error stop must end after one turn, got %v", kinds)
	}
	if kinds[len(kinds)-1] != agentcore.EventAgentEnd {
		t.Errorf("run must end with agent_end, got %v", kinds)
	}
}

func TestAgentLoopAllTerminateStopsRun(t *testing.T) {
	term := true
	termTool := execTool{
		name: "quit",
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("bye")}, Terminate: &term}, nil
		},
	}
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "quit"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn}, // should never be reached
	}), termTool)
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	kinds, _ := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	if countKind(kinds, agentcore.EventTurnStart) != 1 {
		t.Errorf("terminate must end the run after one turn, got %v", kinds)
	}
}

func assertEventKinds(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func countKind(kinds []string, want string) int {
	n := 0
	for _, k := range kinds {
		if k == want {
			n++
		}
	}
	return n
}
