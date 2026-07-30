package run

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// sessionDispatcher builds a Dispatcher for a single SessionStart matcher.
func sessionDispatcher(t *testing.T, cmd string) *hooks.Dispatcher {
	t.Helper()
	set := hooks.HookSet{
		"SessionStart": {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: cmd}}}},
	}
	d := hooks.NewDispatcher(set, t.TempDir(), nil)
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	return d
}

// TestSessionStartInjectsOnce: a SessionStart hook printing additionalContext
// registers a one-shot reminder that fires on the first turn, then goes silent.
func TestSessionStartInjectsOnce(t *testing.T) {
	d := sessionDispatcher(t, `echo '{"additionalContext":"project context loaded"}'`)
	var cfg runtime.RunConfig
	DispatchSessionStart(context.Background(), d, &cfg, HookDeps{SessionID: "s1"}, "startup")

	if cfg.Reminders == nil || cfg.Reminders.Empty() {
		t.Fatal("SessionStart additionalContext should register a reminder")
	}

	first := cfg.Reminders.Messages(context.Background(), nil)
	if len(first) != 1 {
		t.Fatalf("expected 1 reminder message on the first turn, got %d", len(first))
	}
	if txt := textOfUserMsg(first[0]); !strings.Contains(txt, "project context loaded") {
		t.Fatalf("injected context missing, got %q", txt)
	}

	if second := cfg.Reminders.Messages(context.Background(), nil); len(second) != 0 {
		t.Fatalf("one-shot reminder must not fire twice, got %d", len(second))
	}
}

// TestSessionStartResumeSource: the source ("resume") is threaded into the hook
// input so the hook can differentiate startup from resume. The hook echoes its
// stdin JSON's source field into additionalContext, which we then observe.
func TestSessionStartResumeSource(t *testing.T) {
	d := sessionDispatcher(t, `in=$(cat); case "$in" in *'"source":"resume"'*) echo '{"additionalContext":"source=resume"}';; *) echo '{}';; esac`)
	var cfg runtime.RunConfig
	DispatchSessionStart(context.Background(), d, &cfg, HookDeps{SessionID: "s1"}, "resume")

	if cfg.Reminders == nil || cfg.Reminders.Empty() {
		t.Fatal("expected a reminder registered")
	}
	msgs := cfg.Reminders.Messages(context.Background(), nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 reminder message, got %d", len(msgs))
	}
	if txt := textOfUserMsg(msgs[0]); !strings.Contains(txt, "source=resume") {
		t.Fatalf("resume source not threaded into hook input, got %q", txt)
	}
}

// TestSessionStartNilDispatcher: no hooks configured is a no-op.
func TestSessionStartNilDispatcher(t *testing.T) {
	var cfg runtime.RunConfig
	DispatchSessionStart(context.Background(), nil, &cfg, HookDeps{}, "startup")
	if cfg.Reminders != nil {
		t.Fatal("nil dispatcher must not allocate a registry")
	}
}
