package agentcore

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
	EventCompaction          = "compaction"
	EventTelemetry           = "telemetry"
)

// AgentStartEvent is emitted once when a loop run begins. SessionID, when set,
// is the id of the session backing this run; it is carried in the first
// stream-json event so a caller can associate output with a session and resume
// it later (对标 pi/Claude Code, which put a session id in the first event).
type AgentStartEvent struct {
	SessionID string
}

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

// CompactionEvent is emitted when the loop compacts the context window, either
// automatically (threshold/overflow) or on an explicit /compact request. It
// carries before/after token counts and how many messages were summarized vs.
// retained. When compaction fails it is still emitted with ErrorMessage set and
// the token/count fields describing the unchanged context, so consumers can
// surface the failure without the session aborting (US-004).
type CompactionEvent struct {
	// Reason is why compaction ran: "manual", "threshold", or "overflow".
	Reason string
	// TokensBefore is the estimated context tokens prior to compaction.
	TokensBefore int
	// TokensAfter is the estimated context tokens after compaction (equals
	// TokensBefore when compaction failed or was a no-op).
	TokensAfter int
	// SummarizedCount is the number of messages folded into the summary.
	SummarizedCount int
	// KeptCount is the number of recent messages retained verbatim.
	KeptCount int
	// ErrorMessage is non-empty when compaction failed; the original context is
	// preserved in that case.
	ErrorMessage string
}

// ToolTiming records how long one tool invocation took, keyed by tool name in
// TelemetryEvent.ToolDurationsMs. It aggregates repeated calls of the same tool
// so a summary stays compact regardless of turn count.
type ToolTiming struct {
	// Count is how many times the tool was invoked over the run.
	Count int
	// TotalMs is the summed wall-clock duration of every invocation, in
	// milliseconds.
	TotalMs int64
}

// TelemetryEvent is a lightweight, additive observability summary emitted once
// at run end (just before agent_end) so scripts consuming the stream-json
// output can read structured metrics without a new dependency (no
// Prometheus/OTLP). It is purely observational: consumers that ignore it behave
// exactly as before. Metrics covered (可观测性——结构化遥测采集):
//   - per-tool wall-clock durations (ToolDurationsMs, aggregated by tool name),
//   - how many turns ran (Turns),
//   - how many assistant responses were truncated by the output cap
//     (TruncationCount),
//   - how many times the context was compacted (CompactionCount),
//   - the latest context-utilization ratio (ContextUtilization = used tokens /
//     ContextWindow) and the raw numbers behind it.
type TelemetryEvent struct {
	// Turns is the number of turns (turn_start events) the run executed.
	Turns int
	// ToolDurationsMs maps a tool name to its aggregated timing over the run.
	ToolDurationsMs map[string]ToolTiming
	// TruncationCount is how many assistant responses stopped with reason
	// "length" (truncated by the output token cap), each triggering a resend.
	TruncationCount int
	// CompactionCount is how many successful context compactions occurred.
	CompactionCount int
	// ContextUtilization is the latest used/window ratio in [0,1], or 0 when the
	// context window is unknown. Computed as ContextTokens / ContextWindow.
	ContextUtilization float64
	// ContextTokens is the most recently observed estimated context-token usage.
	ContextTokens int
	// ContextWindow is the model's total context-token budget (0 when unknown).
	ContextWindow int
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
func (CompactionEvent) isAgentEvent()          {}
func (TelemetryEvent) isAgentEvent()           {}

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
func (CompactionEvent) EventType() string          { return EventCompaction }
func (TelemetryEvent) EventType() string           { return EventTelemetry }
