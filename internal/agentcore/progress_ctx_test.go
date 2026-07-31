package agentcore

import (
	"context"
	"errors"
	"testing"
)

func TestSubAgentProgressEventImplementsAgentEvent(t *testing.T) {
	var ev AgentEvent = SubAgentProgressEvent{
		ToolCallID:  "call-1",
		Description: "do a thing",
		Activity:    "Editing",
		Tokens:      42,
	}
	if got := ev.EventType(); got != EventSubAgentProgress {
		t.Fatalf("EventType() = %q, want %q", got, EventSubAgentProgress)
	}
	if EventSubAgentProgress != "subagent_progress" {
		t.Fatalf("EventSubAgentProgress = %q, want %q", EventSubAgentProgress, "subagent_progress")
	}
}

func TestProgressEmitterRoundTrip(t *testing.T) {
	var seen AgentEvent
	sentinel := errors.New("sentinel")
	emit := func(ctx context.Context, ev AgentEvent) error {
		seen = ev
		return sentinel
	}

	ctx := WithProgressEmitter(context.Background(), emit)
	got := ProgressEmitterFromContext(ctx)
	if got == nil {
		t.Fatal("ProgressEmitterFromContext returned nil after WithProgressEmitter")
	}

	want := SubAgentProgressEvent{ToolCallID: "call-2", Activity: "Thinking"}
	if err := got(ctx, want); !errors.Is(err, sentinel) {
		t.Fatalf("emitter returned err = %v, want sentinel", err)
	}
	if seen != want {
		t.Fatalf("emitter received %#v, want %#v", seen, want)
	}
}

func TestProgressEmitterFromBareContextIsNil(t *testing.T) {
	if got := ProgressEmitterFromContext(context.Background()); got != nil {
		t.Fatalf("ProgressEmitterFromContext on bare ctx = %v, want nil", got)
	}
}
