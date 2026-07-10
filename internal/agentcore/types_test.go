package agentcore

import (
	"encoding/json"
	"testing"
)

func TestContentListRoundTrip(t *testing.T) {
	in := ContentList{
		NewTextContent("hello"),
		NewThinkingContent("pondering"),
		NewToolCallContent("call_1", "read", json.RawMessage(`{"path":"a.go"}`)),
		NewImageContent("YmFzZTY0", "image/png"),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ContentList
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 blocks, got %d", len(out))
	}
	if _, ok := out[0].(TextContent); !ok {
		t.Errorf("block 0: want TextContent, got %T", out[0])
	}
	if _, ok := out[1].(ThinkingContent); !ok {
		t.Errorf("block 1: want ThinkingContent, got %T", out[1])
	}
	tc, ok := out[2].(ToolCallContent)
	if !ok {
		t.Fatalf("block 2: want ToolCallContent, got %T", out[2])
	}
	if tc.ID != "call_1" || tc.Name != "read" {
		t.Errorf("toolCall fields lost: %+v", tc)
	}
	if string(tc.Arguments) != `{"path":"a.go"}` {
		t.Errorf("arguments lost: %s", tc.Arguments)
	}
	if _, ok := out[3].(ImageContent); !ok {
		t.Errorf("block 3: want ImageContent, got %T", out[3])
	}
}

func TestContentUnknownTypeRejected(t *testing.T) {
	var out ContentList
	err := json.Unmarshal([]byte(`[{"type":"bogus"}]`), &out)
	if err == nil {
		t.Fatal("expected error for unknown content type")
	}
}

func TestContentMissingTypeRejected(t *testing.T) {
	var out ContentList
	err := json.Unmarshal([]byte(`[{"text":"no type"}]`), &out)
	if err == nil {
		t.Fatal("expected error for missing type discriminant")
	}
}

func TestMessageListRoundTrip(t *testing.T) {
	term := true
	_ = term
	in := MessageList{
		UserMessage{RoleField: RoleUser, Content: ContentList{NewTextContent("hi")}, Timestamp: 1},
		AssistantMessage{
			RoleField:  RoleAssistant,
			Content:    ContentList{NewTextContent("ok"), NewToolCallContent("c1", "ls", json.RawMessage(`{}`))},
			StopReason: StopReasonToolUse,
			Timestamp:  2,
		},
		ToolResultMessage{RoleField: RoleToolResult, ToolCallID: "c1", ToolName: "ls", Content: ContentList{NewTextContent("file.go")}, Timestamp: 3},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out MessageList
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 messages, got %d", len(out))
	}
	if out[0].Role() != RoleUser {
		t.Errorf("msg 0: want user, got %s", out[0].Role())
	}
	am, ok := out[1].(AssistantMessage)
	if !ok {
		t.Fatalf("msg 1: want AssistantMessage, got %T", out[1])
	}
	if calls := am.ToolCalls(); len(calls) != 1 || calls[0].Name != "ls" {
		t.Errorf("assistant ToolCalls wrong: %+v", calls)
	}
	if out[2].Role() != RoleToolResult {
		t.Errorf("msg 2: want toolResult, got %s", out[2].Role())
	}
}

func TestMessageUnknownRoleRejected(t *testing.T) {
	var out MessageList
	if err := json.Unmarshal([]byte(`[{"role":"system"}]`), &out); err == nil {
		t.Fatal("expected error for unknown role")
	}
}

// TestAgentEventCoverage asserts all 10 event types report a distinct,
// non-empty discriminant (PRD FR-24).
func TestAgentEventCoverage(t *testing.T) {
	events := []AgentEvent{
		AgentStartEvent{}, AgentEndEvent{}, TurnStartEvent{}, TurnEndEvent{},
		MessageStartEvent{}, MessageUpdateEvent{}, MessageEndEvent{},
		ToolExecutionStartEvent{}, ToolExecutionUpdateEvent{}, ToolExecutionEndEvent{},
	}
	seen := map[string]bool{}
	for _, e := range events {
		et := e.EventType()
		if et == "" {
			t.Errorf("%T has empty EventType", e)
		}
		if seen[et] {
			t.Errorf("duplicate event type %q", et)
		}
		seen[et] = true
	}
	if len(seen) != 10 {
		t.Fatalf("want 10 distinct event types, got %d", len(seen))
	}
}
