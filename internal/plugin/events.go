// This file bridges the agent's event stream to subscribed plugins (US-017,
// #133). The agent loop emits agentcore.AgentEvent values; a plugin declares
// which event types it wants in its manifest. EventNotifier maps each observed
// event to a small, wire-safe payload and hands it to the Manager for
// fire-and-forget delivery — the same "never secrets, only observable fields"
// discipline the stream-json envelope uses.
package plugin

import (
	"encoding/json"
	"io"

	"github.com/smallnest/pigo/internal/agentcore"
)

// EventNotifier forwards agent lifecycle events to the plugins that subscribed
// to them. It is created once per run and its Handle method is wired as the
// event-stream OnEvent callback. A nil *Manager (no plugins) makes NewNotifier
// return nil, and calling Handle on a nil notifier is a safe no-op — callers can
// wire it unconditionally.
type EventNotifier struct {
	mgr     *Manager
	warnLog io.Writer
}

// NewEventNotifier returns a notifier over mgr, or nil when mgr is nil or has no
// plugins — so the caller can skip the OnEvent wiring entirely in the common
// no-plugin case. warnLog (when non-nil) receives per-plugin delivery failures.
func NewEventNotifier(mgr *Manager, warnLog io.Writer) *EventNotifier {
	if mgr == nil || len(mgr.plugins) == 0 {
		return nil
	}
	return &EventNotifier{mgr: mgr, warnLog: warnLog}
}

// Handle maps ev to a wire payload and dispatches it to subscribed plugins. It
// is a no-op on a nil notifier and when no plugin subscribes to ev's type, so it
// builds a payload only when someone is listening. Delivery is bounded and
// isolated by the Manager, so Handle never blocks the loop beyond the
// per-plugin event timeout.
func (n *EventNotifier) Handle(ev agentcore.AgentEvent) {
	if n == nil {
		return
	}
	t := ev.EventType()
	if !n.mgr.Subscribers(t) {
		return
	}
	data, err := json.Marshal(eventPayload(ev))
	if err != nil {
		data = nil // deliver the bare type rather than dropping the event
	}
	n.mgr.DispatchEvent(EventParams{Type: t, Data: data}, n.warnLog)
}

// eventPayload derives the wire-safe payload for an event: only observable,
// non-secret fields (ids, names, counts, stop reasons, streamed text). It
// mirrors the stream-json envelope's field selection so plugin authors and
// stream consumers see the same shape.
func eventPayload(ev agentcore.AgentEvent) map[string]any {
	switch e := ev.(type) {
	case agentcore.AgentEndEvent:
		return map[string]any{"messageCount": len(e.Messages)}
	case agentcore.TurnEndEvent:
		p := map[string]any{"stopReason": e.Message.StopReason}
		if text := agentcore.ContentToText(e.Message.Content); text != "" {
			p["text"] = text
		}
		if calls := e.Message.ToolCalls(); len(calls) > 0 {
			names := make([]string, len(calls))
			for i, c := range calls {
				names[i] = c.Name
			}
			p["toolCalls"] = names
		}
		return p
	case agentcore.ToolExecutionStartEvent:
		return map[string]any{"toolCallId": e.ToolCallID, "toolName": e.ToolName}
	case agentcore.ToolExecutionEndEvent:
		return map[string]any{"toolCallId": e.ToolCallID, "toolName": e.ToolName, "isError": e.IsError}
	case agentcore.CompactionEvent:
		return map[string]any{
			"reason":          e.Reason,
			"tokensBefore":    e.TokensBefore,
			"tokensAfter":     e.TokensAfter,
			"summarizedCount": e.SummarizedCount,
			"keptCount":       e.KeptCount,
		}
	default:
		return map[string]any{}
	}
}
