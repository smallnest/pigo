package agent

import (
	"context"
	"testing"
	"time"
)

// TestEventStreamNormalCompletion drives a producer that emits events and sets
// a result, then verifies the consumer sees every event and Result yields the
// captured value.
func TestEventStreamNormalCompletion(t *testing.T) {
	s := NewEventStream[AgentEvent, []AgentMessage](0)
	want := []AgentMessage{
		UserMessage{RoleField: RoleUser, Content: ContentList{NewTextContent("hi")}},
	}

	go func() {
		ctx := context.Background()
		_ = s.Emit(ctx, TurnStartEvent{})
		_ = s.Emit(ctx, AgentEndEvent{Messages: want})
		s.SetResult(want)
		s.Close()
	}()

	var got int
	for range s.Events() {
		got++
	}
	if got != 2 {
		t.Fatalf("want 2 events, got %d", got)
	}
	res, err := s.Result(context.Background())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(res) != 1 || res[0].Role() != RoleUser {
		t.Fatalf("result payload wrong: %+v", res)
	}
}

// TestEventStreamIsCompleteCallback verifies the isComplete/extractResult
// callbacks auto-capture the result on the terminal event.
func TestEventStreamIsCompleteCallback(t *testing.T) {
	s := NewEventStream[AgentEvent, []AgentMessage](4)
	s.IsComplete = func(e AgentEvent) bool { return e.EventType() == EventAgentEnd }
	s.ExtractResult = func(e AgentEvent) []AgentMessage { return e.(AgentEndEvent).Messages }

	msgs := []AgentMessage{AssistantMessage{RoleField: RoleAssistant}}
	go func() {
		ctx := context.Background()
		_ = s.Emit(ctx, MessageStartEvent{})
		_ = s.Emit(ctx, AgentEndEvent{Messages: msgs})
		s.Close()
	}()

	for range s.Events() {
	}
	res, err := s.Result(context.Background())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 msg from extractResult, got %d", len(res))
	}
}

// TestEventStreamCancellation verifies that a cancelled context unblocks a
// producer stuck on Emit (consumer stopped reading) and that Result returns the
// context error.
func TestEventStreamCancellation(t *testing.T) {
	s := NewEventStream[AgentEvent, []AgentMessage](0)
	ctx, cancel := context.WithCancel(context.Background())

	emitErr := make(chan error, 1)
	go func() {
		// First emit has no consumer; it blocks until cancel.
		emitErr <- s.Emit(ctx, TurnStartEvent{})
	}()

	// Give the producer a moment to block on the send, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-emitErr:
		if err == nil {
			t.Fatal("expected Emit to return ctx error on cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("Emit did not unblock after cancel (goroutine leak)")
	}

	// Result with a cancelled context returns promptly with the ctx error.
	if _, err := s.Result(ctx); err == nil {
		t.Fatal("expected Result to return ctx error")
	}
}

// TestEventStreamIncompleteClose verifies Close without a result yields
// ErrStreamIncomplete.
func TestEventStreamIncompleteClose(t *testing.T) {
	s := NewEventStream[AgentEvent, []AgentMessage](1)
	s.Close()
	if _, err := s.Result(context.Background()); err != ErrStreamIncomplete {
		t.Fatalf("want ErrStreamIncomplete, got %v", err)
	}
}

// TestEventStreamSetErrorWins verifies SetError is reported and later SetResult
// is ignored (first outcome wins).
func TestEventStreamSetErrorWins(t *testing.T) {
	s := NewEventStream[AgentEvent, []AgentMessage](1)
	sentinel := context.Canceled
	s.SetError(sentinel)
	s.SetResult(nil)
	s.Close()
	if _, err := s.Result(context.Background()); err != sentinel {
		t.Fatalf("want sentinel error, got %v", err)
	}
}
