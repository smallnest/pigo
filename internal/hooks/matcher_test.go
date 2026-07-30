package hooks

import (
	"bytes"
	"strings"
	"testing"
)

func TestMatchHooks(t *testing.T) {
	set := HookSet{
		"PreToolUse": {
			{Matcher: "", Hooks: []HookConfig{{Command: "all"}}},
			{Matcher: "*", Hooks: []HookConfig{{Command: "star"}}},
			{Matcher: "bash", Hooks: []HookConfig{{Command: "bash-only"}}},
			{Matcher: "write|edit", Hooks: []HookConfig{{Command: "write-or-edit"}}},
			{Matcher: "Edit.*", Hooks: []HookConfig{{Command: "regex-edit"}}},
		},
		"SessionStart": {
			{Matcher: "ignored", Hooks: []HookConfig{{Command: "session"}}},
		},
	}

	commands := func(hs []HookConfig) []string {
		out := make([]string, len(hs))
		for i, h := range hs {
			out[i] = h.Command
		}
		return out
	}

	t.Run("bash matches empty, star, exact", func(t *testing.T) {
		got := commands(set.MatchHooks("PreToolUse", "bash", nil))
		want := []string{"all", "star", "bash-only"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("write matches multi-value", func(t *testing.T) {
		got := commands(set.MatchHooks("PreToolUse", "write", nil))
		if !contains(got, "write-or-edit") {
			t.Fatalf("expected write-or-edit in %v", got)
		}
	})

	t.Run("EditFile matches regex not exact", func(t *testing.T) {
		got := commands(set.MatchHooks("PreToolUse", "EditFile", nil))
		if !contains(got, "regex-edit") {
			t.Fatalf("expected regex-edit in %v", got)
		}
		if contains(got, "bash-only") {
			t.Fatalf("did not expect bash-only in %v", got)
		}
	})

	t.Run("no-tool-name event ignores matcher", func(t *testing.T) {
		got := commands(set.MatchHooks("SessionStart", "", nil))
		want := []string{"session"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("unknown event returns nil", func(t *testing.T) {
		if got := set.MatchHooks("Nope", "bash", nil); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})
}

func TestMatchHooksInvalidRegexSkipped(t *testing.T) {
	set := HookSet{
		"PreToolUse": {
			{Matcher: "(unterminated", Hooks: []HookConfig{{Command: "bad"}}},
			{Matcher: "bash", Hooks: []HookConfig{{Command: "good"}}},
		},
	}
	var warn bytes.Buffer
	got := set.MatchHooks("PreToolUse", "bash", &warn)
	if len(got) != 1 || got[0].Command != "good" {
		t.Fatalf("expected only good hook, got %v", got)
	}
	if !strings.Contains(warn.String(), "invalid regexp") {
		t.Fatalf("expected regexp warning, got %q", warn.String())
	}
}

func TestMatchHooksSkipsInvalidHook(t *testing.T) {
	set := HookSet{
		"PreToolUse": {
			{Matcher: "*", Hooks: []HookConfig{{Command: ""}, {Command: "ok"}}},
		},
	}
	var warn bytes.Buffer
	got := set.MatchHooks("PreToolUse", "bash", &warn)
	if len(got) != 1 || got[0].Command != "ok" {
		t.Fatalf("expected only ok hook, got %v", got)
	}
	if !strings.Contains(warn.String(), "invalid hook") {
		t.Fatalf("expected invalid-hook warning, got %q", warn.String())
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
