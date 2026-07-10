package agent

// AgentEvent is the sealed interface implemented by every event the loop emits.
// Consumers dispatch with a type switch, consistent with Content. pigo covers
// all 10 of pi's event types (PRD FR-24).
type AgentEvent interface {
	isAgentEvent()
	// EventType returns the discriminant string, useful for logging and for
	// serialising events to the stream-json/stdio protocol (US-020).
	EventType() string
}

// Event type discriminants.
const (
	EventAgentStart          = "agent_start"
	EventAgentEnd            = "agent_end"
	EventTurnStart           = "turn_start"
	EventTurnEnd             = "turn_end"
	EventMessageStart        = "message_start"
	EventMessageUpdate       = "message_update"
	EventMessageEnd          = "message_end"
	EventToolExecutionStart  = "tool_execution_start"
	EventToolExecutionUpdate = "tool_execution_update"
	EventToolExecutionEnd    = "tool_execution_end"
)

// AgentStartEvent is emitted once when a loop run begins.
type AgentStartEvent struct{}

// AgentEndEvent is emitted once when a loop run ends, carrying the messages
// newly produced during this run (the EventStream result).
type AgentEndEvent struct {
	Messages []AgentMessage
}

// TurnStartEvent marks the start of a turn (a single assistant response cycle).
type TurnStartEvent struct{}

// TurnEndEvent marks the end of a turn, with the assistant message and any tool
// results produced during it.
type TurnEndEvent struct {
	Message     AssistantMessage
	ToolResults []ToolResultMessage
}

// MessageStartEvent is emitted when a message begins streaming.
type MessageStartEvent struct {
	Message AgentMessage
}

// MessageUpdateEvent is emitted for each streaming delta, carrying the current
// partial message and the raw provider-level event that produced it.
type MessageUpdateEvent struct {
	Message               AgentMessage
	AssistantMessageEvent any
}

// MessageEndEvent is emitted when a message finishes streaming.
type MessageEndEvent struct {
	Message AgentMessage
}

// ToolExecutionStartEvent is emitted before a tool runs.
type ToolExecutionStartEvent struct {
	ToolCallID string
	ToolName   string
	Args       any
}

// ToolExecutionUpdateEvent carries a partial result during tool execution.
type ToolExecutionUpdateEvent struct {
	ToolCallID    string
	ToolName      string
	PartialResult AgentToolResult
}

// ToolExecutionEndEvent is emitted when a tool finishes.
type ToolExecutionEndEvent struct {
	ToolCallID string
	ToolName   string
	Result     AgentToolResult
	IsError    bool
}

func (AgentStartEvent) isAgentEvent()          {}
func (AgentEndEvent) isAgentEvent()            {}
func (TurnStartEvent) isAgentEvent()           {}
func (TurnEndEvent) isAgentEvent()             {}
func (MessageStartEvent) isAgentEvent()        {}
func (MessageUpdateEvent) isAgentEvent()       {}
func (MessageEndEvent) isAgentEvent()          {}
func (ToolExecutionStartEvent) isAgentEvent()  {}
func (ToolExecutionUpdateEvent) isAgentEvent() {}
func (ToolExecutionEndEvent) isAgentEvent()    {}

func (AgentStartEvent) EventType() string          { return EventAgentStart }
func (AgentEndEvent) EventType() string            { return EventAgentEnd }
func (TurnStartEvent) EventType() string           { return EventTurnStart }
func (TurnEndEvent) EventType() string             { return EventTurnEnd }
func (MessageStartEvent) EventType() string        { return EventMessageStart }
func (MessageUpdateEvent) EventType() string       { return EventMessageUpdate }
func (MessageEndEvent) EventType() string          { return EventMessageEnd }
func (ToolExecutionStartEvent) EventType() string  { return EventToolExecutionStart }
func (ToolExecutionUpdateEvent) EventType() string { return EventToolExecutionUpdate }
func (ToolExecutionEndEvent) EventType() string    { return EventToolExecutionEnd }
