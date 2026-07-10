package agentcore

import (
	"encoding/json"
	"fmt"
)

// Message roles, matching pi's wire format.
const (
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleToolResult = "toolResult"
)

// Message is the sealed interface implemented by the three message roles.
// AgentMessage (the loop's message abstraction) is simply Message: custom
// message kinds implement the same interface and convertToLlm filters out any
// that are not LLM-bound. This deliberately replaces pi's declaration merging,
// which has no Go equivalent.
type Message interface {
	isMessage()
	// Role returns the discriminant ("user" | "assistant" | "toolResult").
	Role() string
}

// AgentMessage is the loop-level message type. It is the same as Message; the
// alias documents intent at call sites that deal with the loop rather than raw
// LLM messages.
type AgentMessage = Message

// Usage reports token accounting for an assistant response.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// UserMessage is input from the user. Content is restricted at construction to
// text/image blocks (runtime constraint, not a separate interface).
type UserMessage struct {
	RoleField string      `json:"role"`
	Content   ContentList `json:"content"`
	Timestamp int64       `json:"timestamp"`
}

func (UserMessage) isMessage()     {}
func (m UserMessage) Role() string { return RoleUser }

// AssistantMessage is a model response. Content may hold text/thinking/toolCall
// blocks. StopReason follows pi's set (end_turn/tool_use/length/error/aborted).
type AssistantMessage struct {
	RoleField    string      `json:"role"`
	Content      ContentList `json:"content"`
	API          string      `json:"api,omitempty"`
	Provider     string      `json:"provider,omitempty"`
	Model        string      `json:"model,omitempty"`
	Usage        *Usage      `json:"usage,omitempty"`
	StopReason   string      `json:"stopReason,omitempty"`
	ErrorMessage string      `json:"errorMessage,omitempty"`
	Timestamp    int64       `json:"timestamp"`

	// Optional diagnostics, kept for cross-provider replay/observability.
	ResponseModel string `json:"responseModel,omitempty"`
	ResponseID    string `json:"responseId,omitempty"`
}

func (AssistantMessage) isMessage()     {}
func (m AssistantMessage) Role() string { return RoleAssistant }

// ToolCalls returns the tool call blocks in this assistant message, in order.
func (m AssistantMessage) ToolCalls() []ToolCallContent {
	var calls []ToolCallContent
	for _, c := range m.Content {
		if tc, ok := c.(ToolCallContent); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// ToolResultMessage carries the outcome of executing a single tool call.
// Content is restricted to text/image blocks at construction.
type ToolResultMessage struct {
	RoleField  string      `json:"role"`
	ToolCallID string      `json:"toolCallId"`
	ToolName   string      `json:"toolName"`
	Content    ContentList `json:"content"`
	Details    any         `json:"details,omitempty"`
	IsError    bool        `json:"isError"`
	Timestamp  int64       `json:"timestamp"`
}

func (ToolResultMessage) isMessage()     {}
func (m ToolResultMessage) Role() string { return RoleToolResult }

// StopReason values, matching pi.
const (
	StopReasonEndTurn = "end_turn"
	StopReasonToolUse = "tool_use"
	StopReasonLength  = "length"
	StopReasonError   = "error"
	StopReasonAborted = "aborted"
)

// MessageList is a slice of Message with discriminated JSON (un)marshalling,
// dispatching on the "role" field. Used by AgentContext and session persistence.
type MessageList []Message

// UnmarshalJSON decodes a JSON array of messages, dispatching each element on
// its "role" discriminant.
func (ml *MessageList) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(MessageList, 0, len(raws))
	for i, raw := range raws {
		m, err := decodeMessage(raw)
		if err != nil {
			return fmt.Errorf("message[%d]: %w", i, err)
		}
		out = append(out, m)
	}
	*ml = out
	return nil
}

// decodeMessage peeks at the "role" field and decodes into the matching
// concrete message struct.
func decodeMessage(raw json.RawMessage) (Message, error) {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("peek role: %w", err)
	}
	switch probe.Role {
	case RoleUser:
		var m UserMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case RoleAssistant:
		var m AssistantMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case RoleToolResult:
		var m ToolResultMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	case "":
		return nil, fmt.Errorf("missing role discriminant")
	default:
		return nil, fmt.Errorf("unknown role %q", probe.Role)
	}
}
