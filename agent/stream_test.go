package agent

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func TestStructuredStreamHandlerNilCallbackLeavesEventHookNil(t *testing.T) {
	handler := structuredStreamHandler(nil)
	if handler.OnEvent != nil {
		t.Fatal("OnEvent is non-nil for nil structured callback")
	}
}

func TestStructuredStreamHandlerMapsInternalEvents(t *testing.T) {
	var got []Event
	handler := structuredStreamHandler(func(event Event) {
		got = append(got, event)
	})
	if handler.OnEvent == nil {
		t.Fatal("OnEvent is nil for non-nil structured callback")
	}

	handler.OnEvent(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("hello")},
	}})
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	if delta, ok := got[0].(MessageDeltaEvent); !ok || delta.Text != "hello" {
		t.Fatalf("event = %#v, want MessageDeltaEvent hello", got[0])
	}
}
