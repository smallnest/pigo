package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/runtime"
)

// This file bridges the agent run seam (runtime.StartRun + runtime.DrainStream)
// to Bubble Tea (US-004, SPEC 5.1 bridge / 3.2). The agent loop runs on its own
// goroutine and emits AgentEvents; a Bubble Tea program consumes tea.Msg values
// one at a time from its Update loop. The bridge is a pump: a goroutine drains
// the run and converts every event into the matching tea.Msg (see msgs.go),
// sending it into a buffered channel; a tea.Cmd (waitForEvent) receives one msg
// per Update tick. The channel is the only synchronization point, so the
// producer never touches the model and the model never touches the run — all
// state transitions happen on the tea goroutine.
//
// Back-pressure is intentional: the channel blocks the draining goroutine when
// the buffer is full, so no event is ever dropped (the tea loop always catches
// up). Node #388 wires startRun into Model.Init/Update; this file only provides
// the reusable, unit-testable primitives.

// eventChanCap is the buffer size of the bridge channel. A modest buffer lets a
// burst of tool events queue without blocking the run's goroutine on every send,
// while still bounding memory (blocking, never dropping, past the cap).
const eventChanCap = 64

// newEventChan allocates the buffered channel the bridge pumps run events
// through.
func newEventChan() chan tea.Msg {
	return make(chan tea.Msg, eventChanCap)
}

// newStreamHandler builds the runtime.StreamHandler that converts each run event
// into a tea.Msg and sends it into ch. Sends block when ch is full, applying
// back-pressure to the draining goroutine so no event is lost. It is factored
// out of pump so the callback→msg conversion can be unit-tested without a real
// provider run (see bridge_test.go).
func newStreamHandler(ch chan tea.Msg, extra func(agentcore.AgentEvent)) runtime.StreamHandler {
	return runtime.StreamHandler{
		OnText: func(delta string) {
			ch <- textDeltaMsg{delta: delta}
		},
		OnTurnEnd: func(msg agentcore.AssistantMessage, results []agentcore.ToolResultMessage) {
			ch <- turnEndMsg{msg: msg, results: results}
		},
		OnEvent: func(ev agentcore.AgentEvent) {
			// Deliver observer events (plugin notifier, SessionEnd/PreCompact hook)
			// first, then translate into TUI messages.
			if extra != nil {
				extra(ev)
			}
			switch e := ev.(type) {
			case agentcore.ToolExecutionStartEvent:
				ch <- toolStartMsg{id: e.ToolCallID, name: e.ToolName, input: argsToMap(e.Args)}
			case agentcore.ToolExecutionUpdateEvent:
				ch <- toolUpdateMsg{id: e.ToolCallID, partial: agentcore.ContentToText(e.PartialResult.Content)}
			case agentcore.ToolExecutionEndEvent:
				ch <- toolEndMsg{id: e.ToolCallID, ok: !e.IsError, result: agentcore.ContentToText(e.Result.Content)}
			case agentcore.TelemetryEvent:
				ch <- telemetryMsg{ev: e}
			case agentcore.CompactionEvent:
				ch <- compactionMsg{}
			}
		},
	}
}

// argsToMap coerces a tool call's untyped Args into a map[string]any when it is a
// JSON object, returning nil otherwise. The event layer carries Args as an
// untyped any (the decoded JSON arguments), so a consumer that wants structured
// input gets the object form when available and nil for non-object args.
func argsToMap(args any) map[string]any {
	if m, ok := args.(map[string]any); ok {
		return m
	}
	return nil
}

// pump runs the agent loop to completion on the calling goroutine, converting
// every event to a tea.Msg on ch, and finally sends a runEndMsg carrying the
// run's result error. It is meant to be launched as a goroutine by startRun.
func pump(ctx context.Context, ch chan tea.Msg, agentCtx *agentcore.AgentContext, cfg runtime.RunConfig, onEvent func(agentcore.AgentEvent)) {
	stream := runtime.StartRun(ctx, agentCtx, cfg)
	_, err := runtime.DrainStream(ctx, stream, newStreamHandler(ch, onEvent))
	ch <- runEndMsg{err: err}
}

// waitForEvent returns a tea.Cmd that blocks until the next bridge msg arrives.
// The Update loop re-issues it after handling each msg (except runEndMsg) to
// keep pulling events one at a time, so ordering is preserved and the tea
// goroutine never spins.
func waitForEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// startRun launches the run pump on a new goroutine and returns the channel it
// feeds together with the first waitForEvent Cmd. The caller (node #388's model)
// stores the channel and, on every subsequent event, issues waitForEvent(ch)
// again to pull the next msg. Returning the channel keeps the bridge
// self-contained: the model owns the handle and decides when to stop pulling
// (after runEndMsg).
func startRun(ctx context.Context, agentCtx *agentcore.AgentContext, cfg runtime.RunConfig, onEvent func(agentcore.AgentEvent)) (chan tea.Msg, tea.Cmd) {
	ch := newEventChan()
	go pump(ctx, ch, agentCtx, cfg, onEvent)
	return ch, waitForEvent(ch)
}
