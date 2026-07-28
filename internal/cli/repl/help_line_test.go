package repl

// Tests for /help's template listing (US-011, #341): formatHelpLine renders
// "/name <argument-hint> - description (source: <tier>)" (hint omitted when
// absent), and the /help Action includes prompt templates with their hint and
// source tier alongside built-ins.

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/runtime"
)

func TestFormatHelpLine(t *testing.T) {
	cases := []struct {
		name string
		cmd  runtime.SlashCommand
		want string
	}{
		{"hint+desc global", runtime.SlashCommand{Name: "review", ArgumentHint: "<PR-URL>", Description: "Review PRs", Tier: runtime.TierGlobal}, "/review <PR-URL> - Review PRs (source: global)"},
		{"desc only builtin", runtime.SlashCommand{Name: "help", Description: "list commands", Tier: runtime.TierBuiltin}, "/help - list commands (source: builtin)"},
		{"hint only cli", runtime.SlashCommand{Name: "wr", ArgumentHint: "[instructions]", Tier: runtime.TierCLI}, "/wr [instructions] (source: cli)"},
		{"neither project", runtime.SlashCommand{Name: "deploy", Tier: runtime.TierProject}, "/deploy (source: project)"},
		{"settings tier", runtime.SlashCommand{Name: "audit", Description: "audit changelog", Tier: runtime.TierSettings}, "/audit - audit changelog (source: settings)"},
	}
	for _, c := range cases {
		if got := formatHelpLine(c.cmd); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestHelpActionIncludesTemplateLabelAndTier verifies the /help Action lists a
// prompt template with its argument-hint, description, and source tier.
func TestHelpActionIncludesTemplateLabelAndTier(t *testing.T) {
	reg := runtime.NewSlashRegistry()
	registerLiveCommands(reg, &cli.LiveConfig{Model: "test", ProviderName: "test"})
	reg.AddUser(runtime.SlashCommand{
		Name:         "review",
		ArgumentHint: "<PR-URL>",
		Description:  "Review PRs",
		Tier:         runtime.TierGlobal,
		Expand:       func(string) string { return "" },
	})

	out, err := reg.ResolveOutcome("/help")
	if err != nil {
		t.Fatalf("ResolveOutcome /help: %v", err)
	}
	if !out.Handled || out.Kind != runtime.SlashAction {
		t.Fatalf("/help should be a handled action, got handled=%v kind=%v", out.Handled, out.Kind)
	}
	for _, want := range []string{"/review", "<PR-URL>", "Review PRs", "(source: global)"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("/help output missing %q:\n%s", want, out.Message)
		}
	}
}
