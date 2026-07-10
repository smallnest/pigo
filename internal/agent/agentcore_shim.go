// This file re-exports the symbols moved to internal/agentcore (US-001 of the
// agent package split) via type aliases, const re-declarations, and thin
// function wrappers, so the remaining files in package agent compile unchanged
// during the transition. It will be removed as later steps migrate call sites
// to reference agentcore directly.
package agent

import "github.com/smallnest/pigo/internal/agentcore"

// --- message.go ---

type (
	Message           = agentcore.Message
	AgentMessage      = agentcore.AgentMessage
	Usage             = agentcore.Usage
	UserMessage       = agentcore.UserMessage
	AssistantMessage  = agentcore.AssistantMessage
	ToolResultMessage = agentcore.ToolResultMessage
	MessageList       = agentcore.MessageList
)

const (
	RoleUser       = agentcore.RoleUser
	RoleAssistant  = agentcore.RoleAssistant
	RoleToolResult = agentcore.RoleToolResult

	StopReasonEndTurn = agentcore.StopReasonEndTurn
	StopReasonToolUse = agentcore.StopReasonToolUse
	StopReasonLength  = agentcore.StopReasonLength
	StopReasonError   = agentcore.StopReasonError
	StopReasonAborted = agentcore.StopReasonAborted
)

// --- content.go ---

type (
	Content         = agentcore.Content
	TextContent     = agentcore.TextContent
	ThinkingContent = agentcore.ThinkingContent
	ToolCallContent = agentcore.ToolCallContent
	ImageContent    = agentcore.ImageContent
	ContentList     = agentcore.ContentList
)

const (
	ContentTypeText     = agentcore.ContentTypeText
	ContentTypeThinking = agentcore.ContentTypeThinking
	ContentTypeToolCall = agentcore.ContentTypeToolCall
	ContentTypeImage    = agentcore.ContentTypeImage
)

var (
	NewTextContent     = agentcore.NewTextContent
	NewThinkingContent = agentcore.NewThinkingContent
	NewToolCallContent = agentcore.NewToolCallContent
	NewImageContent    = agentcore.NewImageContent
)

// --- event.go ---

type (
	AgentEvent               = agentcore.AgentEvent
	AgentStartEvent          = agentcore.AgentStartEvent
	AgentEndEvent            = agentcore.AgentEndEvent
	TurnStartEvent           = agentcore.TurnStartEvent
	TurnEndEvent             = agentcore.TurnEndEvent
	MessageStartEvent        = agentcore.MessageStartEvent
	MessageUpdateEvent       = agentcore.MessageUpdateEvent
	MessageEndEvent          = agentcore.MessageEndEvent
	ToolExecutionStartEvent  = agentcore.ToolExecutionStartEvent
	ToolExecutionUpdateEvent = agentcore.ToolExecutionUpdateEvent
	ToolExecutionEndEvent    = agentcore.ToolExecutionEndEvent
)

const (
	EventAgentStart          = agentcore.EventAgentStart
	EventAgentEnd            = agentcore.EventAgentEnd
	EventTurnStart           = agentcore.EventTurnStart
	EventTurnEnd             = agentcore.EventTurnEnd
	EventMessageStart        = agentcore.EventMessageStart
	EventMessageUpdate       = agentcore.EventMessageUpdate
	EventMessageEnd          = agentcore.EventMessageEnd
	EventToolExecutionStart  = agentcore.EventToolExecutionStart
	EventToolExecutionUpdate = agentcore.EventToolExecutionUpdate
	EventToolExecutionEnd    = agentcore.EventToolExecutionEnd
)

// --- event_stream.go ---

type EventStream[T any, R any] = agentcore.EventStream[T, R]

var ErrStreamIncomplete = agentcore.ErrStreamIncomplete

// NewEventStream constructs an agentcore.EventStream with the given buffer size.
// It is a thin wrapper because generic functions cannot be aliased with a var.
func NewEventStream[T any, R any](buffer int) *agentcore.EventStream[T, R] {
	return agentcore.NewEventStream[T, R](buffer)
}

// --- tool.go ---

type (
	AgentContext      = agentcore.AgentContext
	ToolExecutionMode = agentcore.ToolExecutionMode
	ToolUpdateFunc    = agentcore.ToolUpdateFunc
	AgentTool         = agentcore.AgentTool
	AgentToolCall     = agentcore.AgentToolCall
	AgentToolResult   = agentcore.AgentToolResult
)

const (
	ToolExecutionParallel   = agentcore.ToolExecutionParallel
	ToolExecutionSequential = agentcore.ToolExecutionSequential
)

// --- hooks.go ---

type (
	ThinkingLevel       = agentcore.ThinkingLevel
	ThinkingLevelMap    = agentcore.ThinkingLevelMap
	AfterToolCallResult = agentcore.AfterToolCallResult
	AgentLoopTurnUpdate = agentcore.AgentLoopTurnUpdate
)

const (
	ThinkingOff     = agentcore.ThinkingOff
	ThinkingMinimal = agentcore.ThinkingMinimal
	ThinkingLow     = agentcore.ThinkingLow
	ThinkingMedium  = agentcore.ThinkingMedium
	ThinkingHigh    = agentcore.ThinkingHigh
	ThinkingXHigh   = agentcore.ThinkingXHigh
)

// --- helpers.go (relocated symbols) ---

type (
	emitFunc               = agentcore.EmitFunc
	PrepareArgumentsFunc   = agentcore.PrepareArgumentsFunc
	BeforeToolCallDecision = agentcore.BeforeToolCallDecision
	BeforeToolCallFunc     = agentcore.BeforeToolCallFunc
	AfterToolCallFunc      = agentcore.AfterToolCallFunc
)

var (
	contentToText   = agentcore.ContentToText
	lastAssistantOf = agentcore.LastAssistantOf
)
