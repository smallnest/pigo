package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

// TestLoopDeliversDueScheduleReminder is the end-to-end delivery proof for the
// session-local scheduler (issue #565): a run whose only tool call creates an
// already-due reminder must settle once, get the reminder injected as a normal
// user turn through GetFollowUpMessages, and run one more turn that actually
// sees the reminder in its request context.
func TestLoopDeliversDueScheduleReminder(t *testing.T) {
	t0 := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	sched := agenttool.NewSchedule()
	sched.Now = func() time.Time { return t0 }

	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "schedule_create", `{"prompt":"check the deploy","at":"2026-09-12T12:00:00Z"}`),
			textTurn("reminder acknowledged"),
			textTurn("checked the deploy; all green"),
		},
	}
	cfg := newFauxRunCfg(p, agenttool.ScheduleTools(sched)...)
	cfg.GetFollowUpMessages = sched.FollowUpMessages

	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("start")},
	}}}

	_, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	// Three provider turns: create the reminder, acknowledge it, then act on the
	// injected follow-up.
	if p.callCount() != 3 {
		t.Fatalf("provider called %d times, want 3", p.callCount())
	}

	// Message shape: assistant(tool) + toolResult + assistant(ack) +
	// user(reminder follow-up) + assistant(final).
	if len(msgs) != 5 {
		t.Fatalf("expected 5 new messages, got %d: %+v", len(msgs), msgs)
	}
	follow, ok := msgs[3].(agentcore.UserMessage)
	if !ok || agentcore.ContentToText(follow.Content) != "check the deploy" {
		t.Fatalf("msgs[3] = %T %+v, want the reminder user turn", msgs[3], msgs[3])
	}
	if a, ok := msgs[4].(agentcore.AssistantMessage); !ok || agentcore.ContentToText(a.Content) != "checked the deploy; all green" {
		t.Fatalf("msgs[4] = %T %+v, want the post-reminder assistant turn", msgs[4], msgs[4])
	}

	// The third request's context must carry the reminder text.
	textOf := func(m agentcore.Message) string {
		switch v := m.(type) {
		case agentcore.UserMessage:
			return agentcore.ContentToText(v.Content)
		case agentcore.AssistantMessage:
			return agentcore.ContentToText(v.Content)
		case agentcore.ToolResultMessage:
			return agentcore.ContentToText(v.Content)
		}
		return ""
	}
	var sawReminder bool
	for _, m := range p.requestAt(2).Context.Messages {
		if strings.Contains(textOf(m), "check the deploy") {
			sawReminder = true
		}
	}
	if !sawReminder {
		t.Error("third request context does not contain the delivered reminder text")
	}

	// The one-shot reminder must be marked delivered, not re-fire later.
	listed := sched.List()
	if len(listed) != 1 || listed[0].Status(t0) != "delivered" {
		t.Fatalf("store after delivery = %+v, want one delivered reminder", listed)
	}
	if follow := sched.FollowUpMessages(context.Background(), nil); len(follow) != 0 {
		t.Fatalf("second consult delivered %+v, want none", follow)
	}
}
