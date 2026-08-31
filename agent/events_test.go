package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func TestEventMapperEmitsTextAndThinkingSuffixes(t *testing.T) {
	var got []Event
	m := eventMapper{emit: func(ev Event) { got = append(got, ev) }}

	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewThinkingContent("abc"),
			agentcore.NewTextContent("hello"),
		},
	}})
	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewThinkingContent("abcdef"),
			agentcore.NewTextContent("hello world"),
		},
	}})

	want := []Event{
		ThinkingDeltaEvent{Text: "abc"},
		MessageDeltaEvent{Text: "hello"},
		ThinkingDeltaEvent{Text: "def"},
		MessageDeltaEvent{Text: " world"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestEventMapperPreservesContentBlockOrderAcrossSuffixes(t *testing.T) {
	var got []Event
	m := eventMapper{emit: func(ev Event) { got = append(got, ev) }}

	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewTextContent("answer"),
			agentcore.NewThinkingContent("reason"),
		},
	}})

	want := []Event{
		MessageDeltaEvent{Text: "answer"},
		ThinkingDeltaEvent{Text: "reason"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestEventMapperRestartsDeltaAfterShrink(t *testing.T) {
	var got []Event
	m := eventMapper{emit: func(ev Event) { got = append(got, ev) }}

	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("hello")},
	}})
	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("hi")},
	}})

	want := []Event{
		MessageDeltaEvent{Text: "hello"},
		MessageDeltaEvent{Text: "hi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestEventMapperRestartsDeltaAfterReplacement(t *testing.T) {
	var got []Event
	m := eventMapper{emit: func(ev Event) { got = append(got, ev) }}

	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("hello")},
	}})
	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("jello")},
	}})

	want := []Event{
		MessageDeltaEvent{Text: "hello"},
		MessageDeltaEvent{Text: "jello"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestEventMapperMessageEndEmitsUsageAndResetsDeltas(t *testing.T) {
	var got []Event
	m := eventMapper{emit: func(ev Event) { got = append(got, ev) }}

	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("first")},
	}})
	got = nil
	m.handle(agentcore.MessageEndEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("first")},
		Usage:     &agentcore.Usage{InputTokens: 7, OutputTokens: 3},
	}})
	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("next")},
	}})

	want := []Event{
		MessageEndEvent{Text: "first"},
		UsageEvent{Usage: Usage{InputTokens: 7, OutputTokens: 3}},
		MessageDeltaEvent{Text: "next"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestEventMapperMapsToolLifecycle(t *testing.T) {
	var got []Event
	m := eventMapper{emit: func(ev Event) { got = append(got, ev) }}

	m.handle(agentcore.ToolExecutionStartEvent{
		ToolCallID: "call-1",
		ToolName:   "lookup",
		Args:       json.RawMessage(`{"q":"PiFlow"}`),
	})
	m.handle(agentcore.ToolExecutionUpdateEvent{
		ToolCallID: "call-1",
		ToolName:   "lookup",
		PartialResult: agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent("partial")},
			Details: map[string]any{"phase": "running"},
		},
	})
	m.handle(agentcore.ToolExecutionEndEvent{
		ToolCallID: "call-1",
		ToolName:   "lookup",
		Result: agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent("done")},
			Details: map[string]any{"phase": "done"},
		},
		IsError: true,
	})

	want := []Event{
		ToolExecutionStartEvent{
			ToolCallID: "call-1",
			ToolName:   "lookup",
			Arguments:  json.RawMessage(`{"q":"PiFlow"}`),
		},
		ToolExecutionUpdateEvent{
			ToolCallID: "call-1",
			ToolName:   "lookup",
			Result: ToolResult{
				Content: []ToolResultContent{{Type: ToolResultContentText, Text: "partial"}},
				Details: map[string]any{"phase": "running"},
			},
		},
		ToolExecutionEndEvent{
			ToolCallID: "call-1",
			ToolName:   "lookup",
			Result: ToolResult{
				Content: []ToolResultContent{{Type: ToolResultContentText, Text: "done"}},
				Details: map[string]any{"phase": "done"},
			},
			IsError: true,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestEventMapperIgnoresNonAssistantMessages(t *testing.T) {
	var got []Event
	m := eventMapper{emit: func(ev Event) { got = append(got, ev) }}
	m.handle(agentcore.MessageUpdateEvent{Message: agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("user")},
	}})
	m.handle(agentcore.MessageEndEvent{Message: agentcore.ToolResultMessage{
		RoleField: agentcore.RoleToolResult,
		Content:   agentcore.ContentList{agentcore.NewTextContent("tool")},
	}})
	if len(got) != 0 {
		t.Fatalf("events = %#v, want none", got)
	}
}
