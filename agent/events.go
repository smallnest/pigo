package agent

import (
	"encoding/json"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// EventType is the stable discriminant for structured Session events.
type EventType string

const (
	EventMessageDelta        EventType = "message_delta"
	EventMessageEnd          EventType = "message_end"
	EventThinkingDelta       EventType = "thinking_delta"
	EventToolExecutionStart  EventType = "tool_execution_start"
	EventToolExecutionUpdate EventType = "tool_execution_update"
	EventToolExecutionEnd    EventType = "tool_execution_end"
	EventUsage               EventType = "usage"
)

// Event is the sealed interface implemented by the public structured event
// types emitted by StreamEvents.
type Event interface {
	EventType() EventType
	isEvent()
}

// MessageDeltaEvent carries only newly appended assistant text.
type MessageDeltaEvent struct {
	Text string `json:"text"`
}

// MessageEndEvent carries the complete text of one finished assistant message.
type MessageEndEvent struct {
	Text string `json:"text"`
}

// ThinkingDeltaEvent carries only newly appended assistant reasoning text.
type ThinkingDeltaEvent struct {
	Text string `json:"text"`
}

// ToolExecutionStartEvent is emitted immediately before a tool invocation.
type ToolExecutionStartEvent struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
}

// ToolExecutionUpdateEvent carries a partial custom-tool result.
type ToolExecutionUpdateEvent struct {
	ToolCallID string     `json:"toolCallId"`
	ToolName   string     `json:"toolName"`
	Result     ToolResult `json:"result"`
}

// ToolExecutionEndEvent carries the final tool result and its error status.
type ToolExecutionEndEvent struct {
	ToolCallID string     `json:"toolCallId"`
	ToolName   string     `json:"toolName"`
	Result     ToolResult `json:"result"`
	IsError    bool       `json:"isError"`
}

// Usage is provider-reported token accounting for one assistant message.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// UsageEvent carries token accounting for one completed assistant message.
type UsageEvent struct {
	Usage Usage `json:"usage"`
}

func (MessageDeltaEvent) isEvent()        {}
func (MessageEndEvent) isEvent()          {}
func (ThinkingDeltaEvent) isEvent()       {}
func (ToolExecutionStartEvent) isEvent()  {}
func (ToolExecutionUpdateEvent) isEvent() {}
func (ToolExecutionEndEvent) isEvent()    {}
func (UsageEvent) isEvent()               {}

func (MessageDeltaEvent) EventType() EventType        { return EventMessageDelta }
func (MessageEndEvent) EventType() EventType          { return EventMessageEnd }
func (ThinkingDeltaEvent) EventType() EventType       { return EventThinkingDelta }
func (ToolExecutionStartEvent) EventType() EventType  { return EventToolExecutionStart }
func (ToolExecutionUpdateEvent) EventType() EventType { return EventToolExecutionUpdate }
func (ToolExecutionEndEvent) EventType() EventType    { return EventToolExecutionEnd }
func (UsageEvent) EventType() EventType               { return EventUsage }

type eventMapper struct {
	previousText     string
	previousThinking string
	emit             func(Event)
}

func (m *eventMapper) handle(event agentcore.AgentEvent) {
	switch value := event.(type) {
	case agentcore.MessageUpdateEvent:
		message, ok := value.Message.(agentcore.AssistantMessage)
		if !ok {
			return
		}
		m.handleMessageUpdate(message)
	case agentcore.MessageEndEvent:
		message, ok := value.Message.(agentcore.AssistantMessage)
		if !ok {
			return
		}
		m.emitEvent(MessageEndEvent{Text: agentcore.ContentToText(message.Content)})
		if message.Usage != nil {
			m.emitEvent(UsageEvent{Usage: Usage{
				InputTokens:  message.Usage.InputTokens,
				OutputTokens: message.Usage.OutputTokens,
			}})
		}
		m.previousText = ""
		m.previousThinking = ""
	case agentcore.ToolExecutionStartEvent:
		arguments, err := json.Marshal(value.Args)
		if err != nil {
			arguments = []byte("null")
		}
		m.emitEvent(ToolExecutionStartEvent{
			ToolCallID: value.ToolCallID,
			ToolName:   value.ToolName,
			Arguments:  append(json.RawMessage(nil), arguments...),
		})
	case agentcore.ToolExecutionUpdateEvent:
		m.emitEvent(ToolExecutionUpdateEvent{
			ToolCallID: value.ToolCallID,
			ToolName:   value.ToolName,
			Result:     toolResultFromInternal(value.PartialResult),
		})
	case agentcore.ToolExecutionEndEvent:
		m.emitEvent(ToolExecutionEndEvent{
			ToolCallID: value.ToolCallID,
			ToolName:   value.ToolName,
			Result:     toolResultFromInternal(value.Result),
			IsError:    value.IsError,
		})
	}
}

func (m *eventMapper) handleMessageUpdate(message agentcore.AssistantMessage) {
	currentText, currentThinking := assistantStreams(message.Content)
	textSkip := sharedPrefixLength(m.previousText, currentText)
	thinkingSkip := sharedPrefixLength(m.previousThinking, currentThinking)

	for _, block := range message.Content {
		switch value := block.(type) {
		case agentcore.TextContent:
			delta, remaining := contentBlockDelta(value.Text, textSkip)
			textSkip = remaining
			if delta != "" {
				m.emitEvent(MessageDeltaEvent{Text: delta})
			}
		case agentcore.ThinkingContent:
			delta, remaining := contentBlockDelta(value.Thinking, thinkingSkip)
			thinkingSkip = remaining
			if delta != "" {
				m.emitEvent(ThinkingDeltaEvent{Text: delta})
			}
		}
	}

	m.previousText = currentText
	m.previousThinking = currentThinking
}

func assistantStreams(content agentcore.ContentList) (string, string) {
	var text strings.Builder
	var thinking strings.Builder
	for _, block := range content {
		switch value := block.(type) {
		case agentcore.TextContent:
			text.WriteString(value.Text)
		case agentcore.ThinkingContent:
			thinking.WriteString(value.Thinking)
		}
	}
	return text.String(), thinking.String()
}

func sharedPrefixLength(previous, current string) int {
	if previous != "" && strings.HasPrefix(current, previous) {
		return len(previous)
	}
	return 0
}

func contentBlockDelta(content string, skip int) (string, int) {
	if skip >= len(content) {
		return "", skip - len(content)
	}
	return content[skip:], 0
}

func (m *eventMapper) emitEvent(event Event) {
	if m.emit != nil {
		m.emit(event)
	}
}
