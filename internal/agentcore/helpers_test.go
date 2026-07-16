package agentcore

import (
	"encoding/json"
	"testing"
)

func TestContentToText(t *testing.T) {
	cases := []struct {
		name string
		list ContentList
		want string
	}{
		{"empty", nil, ""},
		{"single text", ContentList{NewTextContent("hello")}, "hello"},
		{
			"skips non-text blocks",
			ContentList{
				NewTextContent("a"),
				NewThinkingContent("ignored"),
				NewToolCallContent("c1", "ls", json.RawMessage(`{}`)),
				NewTextContent("b"),
				NewImageContent("data", "image/png"),
			},
			"ab",
		},
		{
			"only non-text",
			ContentList{NewThinkingContent("x"), NewImageContent("d", "image/png")},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContentToText(tc.list); got != tc.want {
				t.Errorf("ContentToText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLastAssistantOf(t *testing.T) {
	t.Run("nil when absent", func(t *testing.T) {
		msgs := []AgentMessage{
			UserMessage{RoleField: RoleUser, Content: ContentList{NewTextContent("hi")}},
			ToolResultMessage{RoleField: RoleToolResult, ToolCallID: "c1"},
		}
		if got := LastAssistantOf(msgs); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})
	t.Run("nil for empty slice", func(t *testing.T) {
		if got := LastAssistantOf(nil); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})
	t.Run("returns last assistant", func(t *testing.T) {
		msgs := []AgentMessage{
			AssistantMessage{RoleField: RoleAssistant, Model: "first"},
			UserMessage{RoleField: RoleUser},
			AssistantMessage{RoleField: RoleAssistant, Model: "last"},
			ToolResultMessage{RoleField: RoleToolResult},
		}
		got := LastAssistantOf(msgs)
		if got == nil {
			t.Fatal("want an assistant message, got nil")
		}
		if got.Model != "last" {
			t.Errorf("want the last assistant (model %q), got %q", "last", got.Model)
		}
	})
}

func TestToolCallsEmpty(t *testing.T) {
	m := AssistantMessage{
		RoleField: RoleAssistant,
		Content:   ContentList{NewTextContent("no tools here"), NewThinkingContent("hmm")},
	}
	if calls := m.ToolCalls(); calls != nil {
		t.Errorf("want nil for a message with no tool calls, got %+v", calls)
	}
}

func TestToolCallsPreservesOrder(t *testing.T) {
	m := AssistantMessage{
		RoleField: RoleAssistant,
		Content: ContentList{
			NewToolCallContent("c1", "read", json.RawMessage(`{}`)),
			NewTextContent("between"),
			NewToolCallContent("c2", "write", json.RawMessage(`{}`)),
		},
	}
	calls := m.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("want 2 tool calls, got %d", len(calls))
	}
	if calls[0].ID != "c1" || calls[1].ID != "c2" {
		t.Errorf("tool call order lost: %+v", calls)
	}
}

// TestNewContentConstructorsSetType guards the invariant that every constructor
// sets its type discriminant, so a marshalled block always carries a "type".
func TestNewContentConstructorsSetType(t *testing.T) {
	cases := []struct {
		got  Content
		want string
	}{
		{NewTextContent("t"), ContentTypeText},
		{NewThinkingContent("th"), ContentTypeThinking},
		{NewToolCallContent("id", "n", json.RawMessage(`{}`)), ContentTypeToolCall},
		{NewImageContent("d", "image/png"), ContentTypeImage},
	}
	for _, tc := range cases {
		data, err := json.Marshal(tc.got)
		if err != nil {
			t.Fatalf("marshal %T: %v", tc.got, err)
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			t.Fatalf("unmarshal probe %T: %v", tc.got, err)
		}
		if probe.Type != tc.want {
			t.Errorf("%T type = %q, want %q", tc.got, probe.Type, tc.want)
		}
	}
}

func TestRoleAccessors(t *testing.T) {
	if got := (UserMessage{}).Role(); got != RoleUser {
		t.Errorf("UserMessage.Role = %q, want %q", got, RoleUser)
	}
	if got := (AssistantMessage{}).Role(); got != RoleAssistant {
		t.Errorf("AssistantMessage.Role = %q, want %q", got, RoleAssistant)
	}
	if got := (ToolResultMessage{}).Role(); got != RoleToolResult {
		t.Errorf("ToolResultMessage.Role = %q, want %q", got, RoleToolResult)
	}
}
