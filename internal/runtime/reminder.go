// This file implements the general system-reminder dynamic context injection
// mechanism (US-002, FR-1/FR-2), pigo's port of Claude Code's per-turn
// <system-reminder> injection.
//
// A reminder is EPHEMERAL background context (the current todo list, a file
// that changed under the working directory, a budget warning) that should be
// visible to the model on the turn it matters, but must never pollute the
// durable conversation history. Two properties follow from that:
//
//   - Not user instructions. Reminder bodies are wrapped in <system-reminder>
//     tags with a preamble stating they are background context from the harness,
//     not a request from the user (the pi / Claude Code semantic convention).
//   - Ephemeral. Reminders are injected only into the per-turn LLM request via
//     the existing TransformContext seam, which shapes a COPY of the message
//     list for the request and is never written back to AgentContext.Messages.
//     Because they never enter the persisted message list they cannot be saved
//     to the session file and cannot be folded into a compaction summary
//     (compaction only ever sees AgentContext.Messages).
//
// The mechanism is a registry of ReminderProviders. Each provider is consulted
// every turn and may decline (ok == false) so a reminder only appears when its
// condition holds. RunConfig.Reminders wires the registry into the loop.
package runtime

import (
	"context"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
)

// systemReminderPreamble marks the wrapped body as background context rather
// than a user instruction (FR-2). It leads every injected reminder so the model
// never mistakes harness state for a user request.
const systemReminderPreamble = "The following is background context provided automatically by the harness. " +
	"It is NOT a message or instruction from the user; do not act on it as a request. " +
	"Use it only to stay aware of the current state."

// WrapSystemReminder wraps a reminder body in <system-reminder> tags with the
// background-context preamble. The result is the text of a single injected
// message.
func WrapSystemReminder(body string) string {
	return "<system-reminder>\n" + systemReminderPreamble + "\n\n" + body + "\n</system-reminder>"
}

// ReminderProvider produces an ephemeral system-reminder for the upcoming turn.
// Reminder is consulted every turn with the current (post-TransformContext)
// message list; returning ok == false means "no reminder this turn", so a
// provider injects only when its condition holds.
type ReminderProvider interface {
	// Name identifies the provider (for diagnostics/telemetry). It is not shown
	// to the model.
	Name() string
	// Reminder returns the reminder body and true when a reminder should be
	// injected this turn, or ("", false) to inject nothing.
	Reminder(ctx context.Context, msgs agentcore.MessageList) (body string, ok bool)
}

// ReminderFunc adapts a plain function to a ReminderProvider.
type ReminderFunc struct {
	NameField string
	Fn        func(ctx context.Context, msgs agentcore.MessageList) (string, bool)
}

// Name implements ReminderProvider.
func (f ReminderFunc) Name() string { return f.NameField }

// Reminder implements ReminderProvider.
func (f ReminderFunc) Reminder(ctx context.Context, msgs agentcore.MessageList) (string, bool) {
	if f.Fn == nil {
		return "", false
	}
	return f.Fn(ctx, msgs)
}

// ReminderRegistry holds the reminder providers consulted each turn. The zero
// value is usable (no providers → no injection); NewReminderRegistry is the
// convenience constructor.
type ReminderRegistry struct {
	providers []ReminderProvider
}

// NewReminderRegistry returns a registry pre-populated with providers.
func NewReminderRegistry(providers ...ReminderProvider) *ReminderRegistry {
	r := &ReminderRegistry{}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

// Register appends a provider. nil providers are ignored.
func (r *ReminderRegistry) Register(p ReminderProvider) {
	if p == nil {
		return
	}
	r.providers = append(r.providers, p)
}

// Empty reports whether the registry has no providers (so callers can skip the
// injection wiring entirely).
func (r *ReminderRegistry) Empty() bool { return r == nil || len(r.providers) == 0 }

// Messages consults every provider in registration order and returns the
// ephemeral reminder messages to inject this turn (one UserMessage per provider
// that fires). Reminders are modeled as user-role messages carrying
// <system-reminder>-wrapped text — matching the pi / Claude Code convention
// where dynamic context enters through a user turn but is explicitly labeled as
// background context, not a user instruction.
func (r *ReminderRegistry) Messages(ctx context.Context, msgs agentcore.MessageList) []agentcore.AgentMessage {
	if r.Empty() {
		return nil
	}
	var out []agentcore.AgentMessage
	for _, p := range r.providers {
		body, ok := p.Reminder(ctx, msgs)
		if !ok || body == "" {
			continue
		}
		out = append(out, agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(WrapSystemReminder(body))},
		})
	}
	return out
}

// wrapTransform composes the registry into a TransformContext hook: it runs the
// caller's existing TransformContext (if any) first, then appends this turn's
// reminders to the shaped list. Because TransformContext output is used only to
// build the LLM request and is never written back to AgentContext.Messages, the
// appended reminders are ephemeral — they do not enter the persisted history and
// cannot be swept into a compaction summary. This is the single injection seam
// the loop wires in.
func (r *ReminderRegistry) wrapTransform(
	inner func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList,
) func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList {
	return func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList {
		if inner != nil {
			msgs = inner(ctx, msgs)
		}
		rem := r.Messages(ctx, msgs)
		if len(rem) == 0 {
			return msgs
		}
		out := make(agentcore.MessageList, 0, len(msgs)+len(rem))
		out = append(out, msgs...)
		out = append(out, rem...)
		return out
	}
}

// TodoReminderProvider is the built-in reference reminder provider (US-002): it
// surfaces the current todo list as background context whenever there is
// incomplete work, so the model is reminded of outstanding tasks each turn
// without the list having to be re-sent as a durable message. It reads the same
// TodoStore the todo tool writes, so the reminder always reflects the latest
// plan. When the list is empty or every item is completed it stays silent.
type TodoReminderProvider struct {
	// Store is the session todo list. When nil the provider never fires.
	Store *agenttool.TodoStore
}

// Name implements ReminderProvider.
func (p *TodoReminderProvider) Name() string { return "todo" }

// Reminder implements ReminderProvider. It fires only when the store holds at
// least one item that is not yet completed, keeping the condition deterministic
// and easy to test.
func (p *TodoReminderProvider) Reminder(ctx context.Context, _ agentcore.MessageList) (string, bool) {
	if p.Store == nil {
		return "", false
	}
	items := p.Store.Snapshot()
	if len(items) == 0 {
		return "", false
	}
	incomplete := false
	for _, it := range items {
		if it.Status != agenttool.TodoCompleted {
			incomplete = true
			break
		}
	}
	if !incomplete {
		return "", false
	}
	return "Your todo list has unfinished items. Keep it up to date with the todo tool.\n\n" +
		agenttool.RenderTodoList(items), true
}

// GoalReminderProvider surfaces the active goal as background context each turn
// so the model keeps working toward it (对标 pi-goal). It reads the same
// GoalState the /goal command drives, so the reminder always reflects the live
// objective. It fires only while the goal is active — a paused, blocked, or
// completed goal (and an idle state) injects nothing.
type GoalReminderProvider struct {
	// State is the session goal state. When nil the provider never fires.
	State *agenttool.GoalState
}

// Name implements ReminderProvider.
func (p *GoalReminderProvider) Name() string { return "goal" }

// Reminder implements ReminderProvider. It injects the objective plus a
// persistence instruction while the goal is active, and stays silent otherwise.
func (p *GoalReminderProvider) Reminder(ctx context.Context, _ agentcore.MessageList) (string, bool) {
	if p.State == nil {
		return "", false
	}
	snap := p.State.Snapshot()
	if snap.Status != agenttool.GoalActive || strings.TrimSpace(snap.Objective) == "" {
		return "", false
	}
	return "You are working autonomously toward this goal:\n\n" + snap.Objective +
		"\n\nKeep making progress. When every requirement is verifiably met, call the " +
		"goal_complete tool with a summary. If you hit a true impasse you cannot work " +
		"around, call goal_blocked with concrete evidence. Do not stop or ask the user " +
		"to continue — keep going until the goal is done or blocked.", true
}
