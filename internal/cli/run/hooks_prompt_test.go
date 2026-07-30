package run

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// promptDispatcher builds a Dispatcher for a single UserPromptSubmit matcher.
func promptDispatcher(t *testing.T, cmd string) *hooks.Dispatcher {
	t.Helper()
	set := hooks.HookSet{
		"UserPromptSubmit": {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: cmd}}}},
	}
	d := hooks.NewDispatcher(set, t.TempDir(), nil)
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	return d
}

// TestUserPromptSubmitBlocks: a hook exiting 2 with a stderr reason blocks the
// prompt; DispatchUserPromptSubmit reports (true, reason) and injects nothing.
func TestUserPromptSubmitBlocks(t *testing.T) {
	d := promptDispatcher(t, `echo "prompt rejected" 1>&2; exit 2`)
	var cfg runtime.RunConfig
	block, reason := DispatchUserPromptSubmit(context.Background(), d, &cfg, HookDeps{SessionID: "s1"}, "hello")

	if !block {
		t.Fatal("expected block")
	}
	if !strings.Contains(reason, "prompt rejected") {
		t.Fatalf("block reason not surfaced, got %q", reason)
	}
	if cfg.Reminders != nil && !cfg.Reminders.Empty() {
		t.Fatal("a blocking decision must not register a reminder")
	}
}

// TestUserPromptSubmitInjectsOnce: a hook printing additionalContext registers a
// one-shot reminder that fires exactly once, then goes silent.
func TestUserPromptSubmitInjectsOnce(t *testing.T) {
	d := promptDispatcher(t, `echo '{"additionalContext":"remember: run gofmt"}'`)
	var cfg runtime.RunConfig
	block, _ := DispatchUserPromptSubmit(context.Background(), d, &cfg, HookDeps{SessionID: "s1"}, "hello")

	if block {
		t.Fatal("additionalContext must not block")
	}
	if cfg.Reminders == nil || cfg.Reminders.Empty() {
		t.Fatal("additionalContext should register a reminder")
	}

	// First turn: the injected context appears.
	first := cfg.Reminders.Messages(context.Background(), nil)
	if len(first) != 1 {
		t.Fatalf("expected 1 reminder message on first turn, got %d", len(first))
	}
	if txt := textOfUserMsg(first[0]); !strings.Contains(txt, "remember: run gofmt") {
		t.Fatalf("injected context missing, got %q", txt)
	}

	// Second turn: the one-shot provider is silent.
	if second := cfg.Reminders.Messages(context.Background(), nil); len(second) != 0 {
		t.Fatalf("one-shot reminder must not fire twice, got %d", len(second))
	}
}

// TestUserPromptSubmitNilDispatcher: no hooks configured is a no-op.
func TestUserPromptSubmitNilDispatcher(t *testing.T) {
	var cfg runtime.RunConfig
	block, reason := DispatchUserPromptSubmit(context.Background(), nil, &cfg, HookDeps{}, "hello")
	if block || reason != "" {
		t.Fatalf("nil dispatcher should be a no-op, got (%v, %q)", block, reason)
	}
	if cfg.Reminders != nil {
		t.Fatal("nil dispatcher must not allocate a registry")
	}
}

func textOfUserMsg(m agentcore.AgentMessage) string {
	um, ok := m.(agentcore.UserMessage)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, c := range um.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
