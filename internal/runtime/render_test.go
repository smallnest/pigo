package runtime

// Tests for DrainStream (architecture deepening ①): the single event-stream
// consumer shared by the REPL, the headless driver, and the sub-agent tool.
// These drive it with a hand-built LoopEventStream so the delta accounting and
// the message_update-vs-turn_end dispatch are exercised directly, independent
// of any provider.

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// emitStream builds a LoopEventStream, runs emit on a producer goroutine to
// push events and set the result, and returns the stream ready to drain.
func emitStream(emit func(s *LoopEventStream)) *LoopEventStream {
	s := agentcore.NewEventStream[agentcore.AgentEvent, []agentcore.AgentMessage](0)
	go func() {
		emit(s)
		s.Close()
	}()
	return s
}

func assistantWith(text string) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	}
}

// TestDrainStreamTextDeltas verifies OnText receives only the new suffix of the
// streaming assistant message on each update, never re-emitting printed bytes.
func TestDrainStreamTextDeltas(t *testing.T) {
	ctx := context.Background()
	stream := emitStream(func(s *LoopEventStream) {
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: assistantWith("Hel")})
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: assistantWith("Hello")})
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: assistantWith("Hello world")})
		_ = s.Emit(ctx, agentcore.TurnEndEvent{Message: assistantWith("Hello world")})
		s.SetResult([]agentcore.AgentMessage{assistantWith("Hello world")})
	})

	var b strings.Builder
	deltas := 0
	final, err := DrainStream(ctx, stream, StreamHandler{
		OnText: func(delta string) { b.WriteString(delta); deltas++ },
	})
	if err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	if got := b.String(); got != "Hello world" {
		t.Errorf("concatenated deltas = %q, want %q", got, "Hello world")
	}
	// 3 update deltas ("Hel","lo","<space>world"); turn-end adds nothing new.
	if deltas != 3 {
		t.Errorf("OnText calls = %d, want 3 (turn-end must not re-emit)", deltas)
	}
	if final == nil || agentcore.ContentToText(final.Content) != "Hello world" {
		t.Errorf("final message = %v, want %q", final, "Hello world")
	}
}

// TestDrainStreamTurnEndFlush verifies that when a provider delivers only the
// complete message at turn end (no streaming updates), OnText still receives the
// full text via the turn-end flush.
func TestDrainStreamTurnEndFlush(t *testing.T) {
	ctx := context.Background()
	stream := emitStream(func(s *LoopEventStream) {
		_ = s.Emit(ctx, agentcore.TurnEndEvent{Message: assistantWith("complete only at end")})
		s.SetResult([]agentcore.AgentMessage{assistantWith("complete only at end")})
	})

	var b strings.Builder
	_, err := DrainStream(ctx, stream, StreamHandler{OnText: func(d string) { b.WriteString(d) }})
	if err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	if got := b.String(); got != "complete only at end" {
		t.Errorf("flushed text = %q, want full message", got)
	}
}

// TestDrainStreamResetsPerTurn verifies the delta accounting resets between
// turns, so a second turn's text is emitted from its own start rather than being
// masked by the first turn's printed offset.
func TestDrainStreamResetsPerTurn(t *testing.T) {
	ctx := context.Background()
	stream := emitStream(func(s *LoopEventStream) {
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: assistantWith("first")})
		_ = s.Emit(ctx, agentcore.TurnEndEvent{Message: assistantWith("first")})
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: assistantWith("two")})
		_ = s.Emit(ctx, agentcore.TurnEndEvent{Message: assistantWith("two")})
		s.SetResult([]agentcore.AgentMessage{assistantWith("two")})
	})

	var b strings.Builder
	turns := 0
	_, err := DrainStream(ctx, stream, StreamHandler{
		OnText:    func(d string) { b.WriteString(d) },
		OnTurnEnd: func(agentcore.AssistantMessage, []agentcore.ToolResultMessage) { turns++ },
	})
	if err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	if got := b.String(); got != "firsttwo" {
		t.Errorf("text across turns = %q, want %q", got, "firsttwo")
	}
	if turns != 2 {
		t.Errorf("OnTurnEnd calls = %d, want 2", turns)
	}
}

// TestDrainStreamOnTurnEndResults verifies tool results are handed to OnTurnEnd.
func TestDrainStreamOnTurnEndResults(t *testing.T) {
	ctx := context.Background()
	res := agentcore.ToolResultMessage{Content: agentcore.ContentList{agentcore.NewTextContent("42")}}
	stream := emitStream(func(s *LoopEventStream) {
		_ = s.Emit(ctx, agentcore.TurnEndEvent{
			Message:     assistantWith(""),
			ToolResults: []agentcore.ToolResultMessage{res},
		})
		s.SetResult(nil)
	})

	var gotResults int
	_, err := DrainStream(ctx, stream, StreamHandler{
		OnTurnEnd: func(_ agentcore.AssistantMessage, rs []agentcore.ToolResultMessage) { gotResults = len(rs) },
	})
	if err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	if gotResults != 1 {
		t.Errorf("OnTurnEnd tool results = %d, want 1", gotResults)
	}
}

// TestDrainStreamOnEvent verifies OnEvent sees every raw event in order (the
// stream-json driver's hook), and that a nil-callback handler still drains.
func TestDrainStreamOnEvent(t *testing.T) {
	ctx := context.Background()
	stream := emitStream(func(s *LoopEventStream) {
		_ = s.Emit(ctx, agentcore.AgentStartEvent{})
		_ = s.Emit(ctx, agentcore.MessageUpdateEvent{Message: assistantWith("x")})
		_ = s.Emit(ctx, agentcore.TurnEndEvent{Message: assistantWith("x")})
		_ = s.Emit(ctx, agentcore.AgentEndEvent{})
		s.SetResult(nil)
	})

	var types []string
	_, err := DrainStream(ctx, stream, StreamHandler{
		OnEvent: func(ev agentcore.AgentEvent) { types = append(types, ev.EventType()) },
	})
	if err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	want := []string{"agent_start", "message_update", "turn_end", "agent_end"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Errorf("OnEvent order = %v, want %v", types, want)
	}
}
