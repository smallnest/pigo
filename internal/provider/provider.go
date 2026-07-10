// This file defines the provider streaming abstraction (US-003/US-007 base):
// the StreamFn contract, the per-delta AssistantMessageEvent set, and the
// AssistantMessageEventStream (a specialization of EventStream) that a provider
// pushes deltas onto while yielding a final AssistantMessage.
//
// Contract (FR-13): a StreamFn never expresses a request failure by returning
// an error. Runtime failures are encoded as an error event plus a terminal
// assistant message (stopReason=error/aborted + errorMessage). The returned
// error is reserved for the earliest "could not even build the stream" case.
package provider

import (
	"context"

	"github.com/smallnest/pigo/internal/agentcore"
)

// AssistantMessageEvent is the sealed interface for provider stream deltas. The
// loop dispatches on EventKind; the raw event is also surfaced to consumers via
// MessageUpdateEvent.AssistantMessageEvent.
type AssistantMessageEvent interface {
	isAssistantMessageEvent()
	// EventKind returns the delta discriminant.
	EventKind() string
}

// AssistantMessageEvent kinds.
const (
	StreamEventStart    = "start"
	StreamEventText     = "text"
	StreamEventThinking = "thinking"
	StreamEventToolCall = "toolcall"
	StreamEventDone     = "done"
	StreamEventError    = "error"
)

// StreamStartEvent carries the initial (usually empty) partial message.
type StreamStartEvent struct{ Partial agentcore.AssistantMessage }

// StreamTextEvent carries the partial message after a text delta.
type StreamTextEvent struct{ Partial agentcore.AssistantMessage }

// StreamThinkingEvent carries the partial after a thinking delta.
type StreamThinkingEvent struct{ Partial agentcore.AssistantMessage }

// StreamToolCallEvent carries the partial after a tool-call delta.
type StreamToolCallEvent struct{ Partial agentcore.AssistantMessage }

// StreamDoneEvent is the terminal success event; Message is the final response.
type StreamDoneEvent struct{ Message agentcore.AssistantMessage }

// StreamErrorEvent is the terminal failure event; Message carries the terminal
// assistant message (stopReason=error/aborted + errorMessage).
type StreamErrorEvent struct {
	Message agentcore.AssistantMessage
	Err     error
}

func (StreamStartEvent) isAssistantMessageEvent()    {}
func (StreamTextEvent) isAssistantMessageEvent()     {}
func (StreamThinkingEvent) isAssistantMessageEvent() {}
func (StreamToolCallEvent) isAssistantMessageEvent() {}
func (StreamDoneEvent) isAssistantMessageEvent()     {}
func (StreamErrorEvent) isAssistantMessageEvent()    {}

func (StreamStartEvent) EventKind() string    { return StreamEventStart }
func (StreamTextEvent) EventKind() string     { return StreamEventText }
func (StreamThinkingEvent) EventKind() string { return StreamEventThinking }
func (StreamToolCallEvent) EventKind() string { return StreamEventToolCall }
func (StreamDoneEvent) EventKind() string     { return StreamEventDone }
func (StreamErrorEvent) EventKind() string    { return StreamEventError }

// AssistantMessageEventStream is the provider-level stream: deltas of type
// AssistantMessageEvent with a final AssistantMessage result. isComplete fires
// on done/error; extractResult takes the terminal event's message.
type AssistantMessageEventStream = agentcore.EventStream[AssistantMessageEvent, agentcore.AssistantMessage]

// NewAssistantMessageEventStream builds a provider stream wired with the
// done/error completion callbacks.
func NewAssistantMessageEventStream(buffer int) *AssistantMessageEventStream {
	s := agentcore.NewEventStream[AssistantMessageEvent, agentcore.AssistantMessage](buffer)
	s.IsComplete = func(e AssistantMessageEvent) bool {
		k := e.EventKind()
		return k == StreamEventDone || k == StreamEventError
	}
	s.ExtractResult = func(e AssistantMessageEvent) agentcore.AssistantMessage {
		switch ev := e.(type) {
		case StreamDoneEvent:
			return ev.Message
		case StreamErrorEvent:
			return ev.Message
		default:
			return agentcore.AssistantMessage{}
		}
	}
	return s
}

// LlmContext is the shaped request handed to a StreamFn: the system prompt, the
// LLM-bound messages (UI-only messages already filtered), and the tools.
type LlmContext struct {
	SystemPrompt string
	Messages     agentcore.MessageList
	Tools        []agentcore.AgentTool
}

// StreamConfig carries per-request settings for a StreamFn.
type StreamConfig struct {
	APIKey        string
	ThinkingLevel agentcore.ThinkingLevel
	// Extra holds provider-specific options; opaque to the loop.
	Extra map[string]any
}

// StreamFn produces a provider stream for a model + shaped context. Per the
// contract it returns an error only for early "cannot build the stream"
// failures; all runtime failures ride the returned stream as error events.
type StreamFn func(ctx context.Context, model string, llm LlmContext, cfg StreamConfig) (*AssistantMessageEventStream, error)
