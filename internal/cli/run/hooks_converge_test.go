package run

// Regression coverage for the #425 driver convergence (FR-16): the SAME resolved
// hook set must fire in every driver mode. Rather than stand up a provider-backed
// run for each of the six drivers, this pins the two DISTINCT wiring paths they
// route through — the one-shot path (headless / subagent_rpc via InstallDriverHooks
// / InstallHooks) and the multi-turn path (repl / tui / goal / btw via
// BuildDispatcher once + InstallSeams per turn) — and asserts a PreToolUse hook
// installed through either path reaches the BeforeToolCall seam and blocks. It
// also pins FR-18: an empty hook set wires no seam in either path, so a run with
// no hooks configured behaves exactly as before.

import (
	"context"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// blockingPreToolUse is a hook set whose PreToolUse hook exits 2 (Claude Code
// block semantics), so any wired BeforeToolCall seam must return a blocking
// decision when it fires.
func blockingPreToolUse() hooks.HookSet {
	return hooks.HookSet{
		"PreToolUse": {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: "exit 2"}}}},
	}
}

// fireBeforeToolCall drives the wired BeforeToolCall seam once and reports
// whether it produced a blocking decision. A nil seam (no hook wired) reports
// false.
func fireBeforeToolCall(cfg *runtime.RunConfig) bool {
	seam := cfg.Batch.ToolExecutorConfig.BeforeToolCall
	if seam == nil {
		return false
	}
	dec := seam(context.Background(), agentcore.AgentToolCall{Name: "Bash"})
	return dec != nil && dec.Block
}

// TestHookConvergenceBothPaths asserts the same PreToolUse hook set fires in both
// driver wiring paths: the one-shot headless path and the multi-turn REPL path.
func TestHookConvergenceBothPaths(t *testing.T) {
	deps := HookDeps{SessionID: "s1", ProjectDir: t.TempDir()}
	set := blockingPreToolUse()

	// Headless / subagent_rpc path: InstallDriverHooks wires the seams all-in-one.
	t.Run("headless", func(t *testing.T) {
		var cfg runtime.RunConfig
		d, _ := InstallDriverHooks(context.Background(), &cfg, set, deps, "startup", nil)
		if d == nil {
			t.Fatal("expected dispatcher for non-empty hook set")
		}
		if !fireBeforeToolCall(&cfg) {
			t.Fatal("headless path: PreToolUse hook did not block")
		}
	})

	// REPL / TUI / goal / btw path: BuildDispatcher once, then InstallSeams per turn.
	t.Run("repl", func(t *testing.T) {
		d := BuildDispatcher(set, deps)
		if d == nil {
			t.Fatal("expected dispatcher for non-empty hook set")
		}
		var cfg runtime.RunConfig
		InstallSeams(&cfg, d, deps)
		if !fireBeforeToolCall(&cfg) {
			t.Fatal("repl path: PreToolUse hook did not block")
		}
	})
}

// TestHookConvergenceNoHooksUnchanged pins FR-18: with no hooks configured neither
// wiring path installs a BeforeToolCall seam, so both drivers behave exactly as
// they did before hooks existed.
func TestHookConvergenceNoHooksUnchanged(t *testing.T) {
	deps := HookDeps{ProjectDir: t.TempDir()}

	t.Run("headless", func(t *testing.T) {
		var cfg runtime.RunConfig
		d, ev := InstallDriverHooks(context.Background(), &cfg, nil, deps, "startup", nil)
		if d != nil {
			t.Fatalf("expected nil dispatcher for empty hook set, got %v", d)
		}
		if ev != nil {
			t.Fatal("expected event handler unchanged (nil) for empty hook set")
		}
		if cfg.Batch.ToolExecutorConfig.BeforeToolCall != nil {
			t.Fatal("headless path: seam wired despite no hooks")
		}
	})

	t.Run("repl", func(t *testing.T) {
		d := BuildDispatcher(nil, deps)
		if d != nil {
			t.Fatalf("expected nil dispatcher for empty hook set, got %v", d)
		}
		var cfg runtime.RunConfig
		InstallSeams(&cfg, d, deps) // nil dispatcher must be a no-op
		if cfg.Batch.ToolExecutorConfig.BeforeToolCall != nil {
			t.Fatal("repl path: seam wired despite no hooks")
		}
	})
}
