// This file bridges the agent's event stream to observer-only hooks (US-011/012/
// 013, FR-1). Where PreToolUse/UserPromptSubmit/Stop are decision hooks wired at
// dedicated seams, SessionEnd/PreCompact/Notification only observe: the agent
// emits lifecycle events, HookNotifier maps each to a HookInput and fires the
// matching hooks via the Dispatcher, discarding the decision (an observer hook
// cannot block).
//
// It mirrors plugin.EventNotifier: created once per run, its Handle method is
// wired as an event-stream OnEvent callback and coexists with the plugin
// notifier (RunConfig.OnEvent already chains multiple observers). A nil
// *Dispatcher makes NewHookNotifier return nil, and every method is a no-op on a
// nil receiver, so callers can wire it unconditionally.
//
// Session id and project dir are fixed for a run, so they are captured at
// construction rather than read from each event (AgentEndEvent/CompactionEvent
// do not carry them).
package hooks

import (
	"context"

	"github.com/smallnest/pigo/internal/agentcore"
)

// HookNotifier forwards agent lifecycle events to observer-only hooks. Handle
// maps AgentEndEvent→SessionEnd and CompactionEvent→PreCompact; Notify emits a
// Notification event for out-of-band prompts (e.g. a trust confirmation). The
// merged decision is intentionally discarded — these events have no in-flight
// action to veto.
type HookNotifier struct {
	d          *Dispatcher
	sessionID  string
	projectDir string
}

// NewHookNotifier returns a notifier over d, or nil when d is nil (no hooks) so
// the caller can skip the OnEvent wiring entirely. sessionID and projectDir
// populate every emitted HookInput.
func NewHookNotifier(d *Dispatcher, sessionID, projectDir string) *HookNotifier {
	if d == nil {
		return nil
	}
	return &HookNotifier{d: d, sessionID: sessionID, projectDir: projectDir}
}

// Handle maps an observed event to its observer hook and fires it. Events with
// no observer mapping (turn/message/tool events) are ignored. It is a no-op on a
// nil notifier, so it can be chained onto OnEvent unconditionally.
func (n *HookNotifier) Handle(ev agentcore.AgentEvent) {
	if n == nil {
		return
	}
	switch e := ev.(type) {
	case agentcore.AgentEndEvent:
		n.dispatch("SessionEnd", HookInput{
			EventType:  "SessionEnd",
			SessionID:  n.sessionID,
			ProjectDir: n.projectDir,
			StopReason: sessionEndReason(e.Messages),
		})
	case agentcore.CompactionEvent:
		n.dispatch("PreCompact", HookInput{
			EventType:  "PreCompact",
			SessionID:  n.sessionID,
			ProjectDir: n.projectDir,
			Trigger:    compactionTrigger(e.Reason),
		})
	}
}

// Notify fires the Notification event with a human-readable message. It is used
// for out-of-band prompts that are not part of the event stream, such as a trust
// confirmation for an untrusted-directory bash/write. A no-op on a nil notifier
// or an empty message.
func (n *HookNotifier) Notify(message string) {
	if n == nil || message == "" {
		return
	}
	n.dispatch("Notification", HookInput{
		EventType:  "Notification",
		SessionID:  n.sessionID,
		ProjectDir: n.projectDir,
		Message:    message,
	})
}

// dispatch fires the hooks for an observer event and discards the decision.
func (n *HookNotifier) dispatch(event string, input HookInput) {
	n.d.Dispatch(context.Background(), event, "", input)
}

// sessionEndReason derives the SessionEnd reason from the run's terminal
// assistant message: "error"/"aborted" pass through, everything else (end_turn/
// tool_use/length or no assistant message) is a "natural" end.
func sessionEndReason(msgs []agentcore.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if am, ok := msgs[i].(agentcore.AssistantMessage); ok {
			switch am.StopReason {
			case agentcore.StopReasonError:
				return "error"
			case agentcore.StopReasonAborted:
				return "aborted"
			default:
				return "natural"
			}
		}
	}
	return "natural"
}

// compactionTrigger maps a CompactionEvent.Reason to the PreCompact trigger:
// "manual" stays manual; "threshold"/"overflow" (and anything else) are "auto".
func compactionTrigger(reason string) string {
	if reason == "manual" {
		return "manual"
	}
	return "auto"
}
