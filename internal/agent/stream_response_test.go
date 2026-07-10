package agent

import (
	"context"
	"testing"
)

// fakeStream builds a StreamFn that replays a fixed sequence of events, pushing
// each onto an AssistantMessageEventStream from a producer goroutine.
func fakeStream(events []AssistantMessageEvent) StreamFn {
	return func(ctx context.Context, model string, llm LlmContext, cfg StreamConfig) (*AssistantMessageEventStream, error) {
		s := NewAssistantMessageEventStream(0)
		go func() {
			for _, ev := range events {
				if err := s.Emit(ctx, ev); err != nil {
					s.SetError(err)
					s.Close()
					return
				}
			}
			s.Close()
		}()
		return s, nil
	}
}

// drives streamAssistantResponse with a synchronous emit that records events.
func runStream(t *testing.T, agentCtx *AgentContext, cfg LoopConfig) (AssistantMessage, []AgentEvent) {
	t.Helper()
	var got []AgentEvent
	emit := func(ctx context.Context, ev AgentEvent) error {
		got = append(got, ev)
		return nil
	}
	msg, err := streamAssistantResponse(context.Background(), agentCtx, cfg, emit)
	if err != nil {
		t.Fatalf("streamAssistantResponse: %v", err)
	}
	return msg, got
}

func TestStreamResponseBackfillAndEvents(t *testing.T) {
	partial0 := AssistantMessage{RoleField: RoleAssistant}
	partial1 := AssistantMessage{RoleField: RoleAssistant, Content: ContentList{NewTextContent("hel")}}
	final := AssistantMessage{RoleField: RoleAssistant, Content: ContentList{NewTextContent("hello")}, StopReason: StopReasonEndTurn}

	cfg := LoopConfig{
		Model: "fake",
		Stream: fakeStream([]AssistantMessageEvent{
			StreamStartEvent{Partial: partial0},
			StreamTextEvent{Partial: partial1},
			StreamDoneEvent{Message: final},
		}),
	}
	agentCtx := &AgentContext{Messages: MessageList{UserMessage{RoleField: RoleUser}}}

	msg, events := runStream(t, agentCtx, cfg)

	if msg.StopReason != StopReasonEndTurn {
		t.Errorf("final stopReason = %q, want end_turn", msg.StopReason)
	}
	// Context should hold the user message + the final assistant message (the
	// placeholder was replaced, not appended twice).
	if len(agentCtx.Messages) != 2 {
		t.Fatalf("context messages = %d, want 2: %+v", len(agentCtx.Messages), agentCtx.Messages)
	}
	last, ok := agentCtx.Messages[1].(AssistantMessage)
	if !ok || len(last.Content) != 1 {
		t.Fatalf("last message not final assistant: %+v", agentCtx.Messages[1])
	}
	// Event order: message_start, message_update, message_end.
	wantKinds := []string{EventMessageStart, EventMessageUpdate, EventMessageEnd}
	if len(events) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d: %+v", len(events), len(wantKinds), events)
	}
	for i, w := range wantKinds {
		if events[i].EventType() != w {
			t.Errorf("event[%d] = %q, want %q", i, events[i].EventType(), w)
		}
	}
}

func TestStreamResponseErrorEvent(t *testing.T) {
	errMsg := AssistantMessage{RoleField: RoleAssistant, StopReason: StopReasonError, ErrorMessage: "boom"}
	cfg := LoopConfig{
		Model:  "fake",
		Stream: fakeStream([]AssistantMessageEvent{StreamErrorEvent{Message: errMsg}}),
	}
	agentCtx := &AgentContext{}
	msg, events := runStream(t, agentCtx, cfg)
	if msg.StopReason != StopReasonError || msg.ErrorMessage != "boom" {
		t.Errorf("want error terminal message, got %+v", msg)
	}
	// No start event was sent; error should still append the terminal message.
	if len(agentCtx.Messages) != 1 {
		t.Fatalf("context messages = %d, want 1", len(agentCtx.Messages))
	}
	if events[len(events)-1].EventType() != EventMessageEnd {
		t.Errorf("last event = %q, want message_end", events[len(events)-1].EventType())
	}
}

func TestStreamResponseDynamicAPIKey(t *testing.T) {
	var seenKey string
	streamFn := func(ctx context.Context, model string, llm LlmContext, cfg StreamConfig) (*AssistantMessageEventStream, error) {
		seenKey = cfg.APIKey
		s := NewAssistantMessageEventStream(0)
		go func() {
			_ = s.Emit(ctx, StreamDoneEvent{Message: AssistantMessage{RoleField: RoleAssistant, StopReason: StopReasonEndTurn}})
			s.Close()
		}()
		return s, nil
	}
	cfg := LoopConfig{
		Model:     "fake",
		APIKey:    "static-key",
		Provider:  "test",
		Stream:    streamFn,
		GetAPIKey: func(ctx context.Context, provider string) string { return "dynamic-key" },
	}
	runStream(t, &AgentContext{}, cfg)
	if seenKey != "dynamic-key" {
		t.Errorf("dynamic key not used: got %q", seenKey)
	}

	// Empty dynamic key falls back to static.
	cfg.GetAPIKey = func(ctx context.Context, provider string) string { return "" }
	runStream(t, &AgentContext{}, cfg)
	if seenKey != "static-key" {
		t.Errorf("fallback to static key failed: got %q", seenKey)
	}
}

func TestStreamResponseTransformAndConvertOrder(t *testing.T) {
	var order []string
	cfg := LoopConfig{
		Model: "fake",
		TransformContext: func(ctx context.Context, msgs MessageList) MessageList {
			order = append(order, "transform")
			return msgs
		},
		ConvertToLlm: func(msgs MessageList) MessageList {
			order = append(order, "convert")
			return msgs
		},
		Stream: func(ctx context.Context, model string, llm LlmContext, cfg StreamConfig) (*AssistantMessageEventStream, error) {
			order = append(order, "stream")
			s := NewAssistantMessageEventStream(0)
			go func() {
				_ = s.Emit(ctx, StreamDoneEvent{Message: AssistantMessage{RoleField: RoleAssistant}})
				s.Close()
			}()
			return s, nil
		},
	}
	runStream(t, &AgentContext{}, cfg)
	if len(order) != 3 || order[0] != "transform" || order[1] != "convert" || order[2] != "stream" {
		t.Errorf("call order wrong: %v", order)
	}
}

func TestStreamResponseEarlyBuildFailure(t *testing.T) {
	cfg := LoopConfig{
		Model: "fake",
		Stream: func(ctx context.Context, model string, llm LlmContext, cfg StreamConfig) (*AssistantMessageEventStream, error) {
			return nil, context.DeadlineExceeded
		},
	}
	msg, _ := runStream(t, &AgentContext{}, cfg)
	if msg.StopReason != StopReasonError {
		t.Errorf("early build failure should yield error message, got %+v", msg)
	}
}
