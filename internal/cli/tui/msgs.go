package tui

import "github.com/smallnest/pigo/internal/agentcore"

// This file defines the tea.Msg types the event bridge (bridge.go) produces from
// a run's AgentEvents (US-004, SPEC 5.1). Each raw runtime signal is converted to
// exactly one of these value types so the Bubble Tea Update loop can dispatch on
// them with a plain type switch, keeping all run-time state changes on the tea
// goroutine (node #388 wires them into Model.Update). Every type is a value (not
// a pointer) so it flows through the tea.Msg (any) channel without aliasing the
// producer goroutine's state.

// textDeltaMsg carries the newest suffix of streaming assistant text — the bytes
// produced since the previous delta for the current turn (see DrainStream's
// OnText contract).
type textDeltaMsg struct{ delta string }

// turnEndMsg fires once per completed turn with the final assistant message and
// the tool results produced during it.
type turnEndMsg struct {
	msg     agentcore.AssistantMessage
	results []agentcore.ToolResultMessage
}

// toolStartMsg is emitted before a tool runs. input holds the decoded call
// arguments when they are a JSON object; it is nil otherwise (the raw Args are
// an untyped any at the event layer).
type toolStartMsg struct {
	id    string
	name  string
	input map[string]any
}

// toolUpdateMsg carries a partial result streamed during a tool's execution.
type toolUpdateMsg struct {
	id      string
	partial string
}

// toolEndMsg is emitted when a tool finishes. ok is false when the tool reported
// an error; result is the tool's textual output.
type toolEndMsg struct {
	id     string
	ok     bool
	result string
}

// telemetryMsg carries the run's end-of-run telemetry summary.
type telemetryMsg struct{ ev agentcore.TelemetryEvent }

// compactionMsg signals that the loop compacted the context window. The event's
// details are not needed by the transcript, so it is a bare signal.
type compactionMsg struct{}

// runEndMsg is the final message: the run has fully drained. err is non-nil when
// the run ended in error (or was interrupted).
type runEndMsg struct{ err error }
