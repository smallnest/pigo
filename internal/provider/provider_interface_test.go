package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// fakeProvider is a minimal Provider for interface tests.
type fakeProvider struct {
	name     string
	models   []Model
	buildErr error
	events   []AssistantMessageEvent
}

func (p fakeProvider) Name() string    { return p.name }
func (p fakeProvider) Models() []Model { return p.models }
func (p fakeProvider) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if p.buildErr != nil {
		return nil, p.buildErr
	}
	s := NewAssistantMessageEventStream(0)
	go func() {
		for _, ev := range p.events {
			if err := s.Emit(ctx, ev); err != nil {
				s.SetError(err)
				break
			}
		}
		s.Close()
	}()
	return s, nil
}

func TestProviderEarlyBuildFailureReturnsError(t *testing.T) {
	p := fakeProvider{name: "test", buildErr: errors.New("no such model")}
	_, err := p.StreamCompletion(context.Background(), CompletionRequest{Model: "ghost"})
	if err == nil {
		t.Fatal("early build failure must return an error")
	}
}

func TestProviderRuntimeFailureRidesStream(t *testing.T) {
	errMsg := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonError, ErrorMessage: "upstream 500"}
	p := fakeProvider{
		name:   "test",
		events: []AssistantMessageEvent{StreamErrorEvent{Message: errMsg}},
	}
	stream, err := p.StreamCompletion(context.Background(), CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("runtime failure must NOT be a returned error: %v", err)
	}
	final, resErr := stream.Result(context.Background())
	if resErr != nil {
		t.Fatalf("stream result error: %v", resErr)
	}
	if final.StopReason != agentcore.StopReasonError || final.ErrorMessage != "upstream 500" {
		t.Errorf("terminal error message wrong: %+v", final)
	}
}

func TestStreamFnFromProviderDelegates(t *testing.T) {
	done := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn}
	p := fakeProvider{name: "test", events: []AssistantMessageEvent{StreamDoneEvent{Message: done}}}
	fn := StreamFnFromProvider(p)
	stream, err := fn(context.Background(), "m", LlmContext{}, StreamConfig{})
	if err != nil {
		t.Fatalf("delegation error: %v", err)
	}
	final, _ := stream.Result(context.Background())
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("delegated stream result wrong: %+v", final)
	}
}

func TestModelMetadata(t *testing.T) {
	m := Model{Provider: "anthropic", ID: "claude-opus-4-8", SupportsThinking: true, ContextWindow: 200000}
	if m.Provider != "anthropic" || m.ID != "claude-opus-4-8" {
		t.Errorf("model identity wrong: %+v", m)
	}
	if !m.SupportsThinking || m.ContextWindow != 200000 {
		t.Errorf("model capability wrong: %+v", m)
	}
}
