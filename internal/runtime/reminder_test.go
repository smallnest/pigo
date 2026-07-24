package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

// wrapStreamCapture wraps a StreamFn to record every text-block message the
// request carried into seen, so a test can assert what the model was sent.
func wrapStreamCapture(inner provider.StreamFn, seen *[]string) provider.StreamFn {
	return func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		for _, m := range llm.Messages {
			if um, ok := m.(agentcore.UserMessage); ok {
				*seen = append(*seen, agentcore.ContentToText(um.Content))
			}
		}
		return inner(ctx, model, llm, cfg)
	}
}

// reminderTextsInRequest drives one turn and returns the <system-reminder>
// message texts the provider stream actually received (the request-shaped list),
// so a test can assert what the model saw without touching persisted state.
func reminderTextsInRequest(t *testing.T, reg *ReminderRegistry, agentCtx *agentcore.AgentContext) []string {
	t.Helper()
	var seen []string
	streamFn := scriptedStream([]agentcore.AssistantMessage{
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("ok")}},
	})
	// Wrap the stream so we can inspect the LlmContext it is handed.
	cfg := newRunCfg(nil)
	cfg.Reminders = reg
	cfg.LoopConfig.Stream = wrapStreamCapture(streamFn, &seen)
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))
	return seen
}

func TestWrapSystemReminderLabelsBackgroundContext(t *testing.T) {
	out := WrapSystemReminder("body here")
	if !strings.Contains(out, "<system-reminder>") || !strings.Contains(out, "</system-reminder>") {
		t.Errorf("reminder must be wrapped in <system-reminder> tags, got %q", out)
	}
	if !strings.Contains(out, "NOT a message or instruction from the user") {
		t.Errorf("reminder must be labeled as background context, not a user instruction, got %q", out)
	}
	if !strings.Contains(out, "body here") {
		t.Errorf("reminder must contain the body, got %q", out)
	}
}

func TestReminderInjectedWhenConditionHolds(t *testing.T) {
	fired := ReminderFunc{NameField: "always", Fn: func(ctx context.Context, msgs agentcore.MessageList) (string, bool) {
		return "budget is low", true
	}}
	reg := NewReminderRegistry(fired)
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	seen := reminderTextsInRequest(t, reg, agentCtx)
	found := false
	for _, s := range seen {
		if strings.Contains(s, "budget is low") && strings.Contains(s, "<system-reminder>") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a system-reminder in the request, saw %v", seen)
	}
	// Ephemeral: the reminder must NOT be written back into the persisted history.
	for _, m := range agentCtx.Messages {
		if um, ok := m.(agentcore.UserMessage); ok {
			if strings.Contains(agentcore.ContentToText(um.Content), "system-reminder") {
				t.Errorf("reminder leaked into persisted message history: %+v", um)
			}
		}
	}
}

func TestReminderNotInjectedWhenConditionFails(t *testing.T) {
	silent := ReminderFunc{NameField: "never", Fn: func(ctx context.Context, msgs agentcore.MessageList) (string, bool) {
		return "", false
	}}
	reg := NewReminderRegistry(silent)
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	seen := reminderTextsInRequest(t, reg, agentCtx)
	for _, s := range seen {
		if strings.Contains(s, "system-reminder") {
			t.Errorf("no reminder should be injected when the provider declines, saw %q", s)
		}
	}
}

func TestReminderPreservesInnerTransform(t *testing.T) {
	var innerRan bool
	reg := NewReminderRegistry(ReminderFunc{NameField: "always", Fn: func(ctx context.Context, msgs agentcore.MessageList) (string, bool) {
		return "note", true
	}})
	inner := func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList {
		innerRan = true
		return msgs
	}
	wrapped := reg.wrapTransform(inner)
	out := wrapped(context.Background(), agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}})
	if !innerRan {
		t.Errorf("wrapTransform must call the inner TransformContext")
	}
	if len(out) != 2 {
		t.Fatalf("expected original + 1 reminder message, got %d", len(out))
	}
}

func TestTodoReminderProvider(t *testing.T) {
	store := agenttool.NewTodoStore()
	p := &TodoReminderProvider{Store: store}

	// Empty store: silent.
	if _, ok := p.Reminder(context.Background(), nil); ok {
		t.Errorf("empty todo store must not fire a reminder")
	}

	// All completed: silent.
	store.Set([]agenttool.TodoItem{{Content: "done", Status: agenttool.TodoCompleted}})
	if _, ok := p.Reminder(context.Background(), nil); ok {
		t.Errorf("fully-completed todo list must not fire a reminder")
	}

	// Incomplete work: fires with the rendered list.
	store.Set([]agenttool.TodoItem{
		{Content: "write code", Status: agenttool.TodoInProgress},
		{Content: "review", Status: agenttool.TodoPending},
	})
	body, ok := p.Reminder(context.Background(), nil)
	if !ok {
		t.Fatalf("incomplete todo list must fire a reminder")
	}
	if !strings.Contains(body, "write code") || !strings.Contains(body, "review") {
		t.Errorf("reminder body should render the todo items, got %q", body)
	}

	// nil store: never fires.
	nilP := &TodoReminderProvider{}
	if _, ok := nilP.Reminder(context.Background(), nil); ok {
		t.Errorf("nil todo store must not fire a reminder")
	}
}

func TestTodoReminderEndToEndInjection(t *testing.T) {
	store := agenttool.NewTodoStore()
	store.Set([]agenttool.TodoItem{{Content: "unfinished task", Status: agenttool.TodoInProgress}})
	reg := NewReminderRegistry(&TodoReminderProvider{Store: store})
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	seen := reminderTextsInRequest(t, reg, agentCtx)
	found := false
	for _, s := range seen {
		if strings.Contains(s, "unfinished task") && strings.Contains(s, "<system-reminder>") {
			found = true
		}
	}
	if !found {
		t.Fatalf("todo reminder should be injected into the request, saw %v", seen)
	}
}

func TestGoalReminderOnlyWhenActive(t *testing.T) {
	st := agenttool.NewGoalState()
	p := &GoalReminderProvider{State: st}

	// Idle: no reminder.
	if _, ok := p.Reminder(context.Background(), nil); ok {
		t.Fatal("idle goal should not inject a reminder")
	}

	// Active: injects the objective.
	st.Start("g1", "build the feature", 0)
	body, ok := p.Reminder(context.Background(), nil)
	if !ok {
		t.Fatal("active goal should inject a reminder")
	}
	if !strings.Contains(body, "build the feature") {
		t.Errorf("reminder body missing objective: %q", body)
	}

	// Completed: silent again.
	st.MarkComplete("done")
	if _, ok := p.Reminder(context.Background(), nil); ok {
		t.Fatal("completed goal should not inject a reminder")
	}
}
