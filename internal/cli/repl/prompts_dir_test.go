package repl

// Tests for global prompt-template discovery (US-005, #336): buildSlashRegistry
// loads both the legacy ~/.pigo/commands and the pi-aligned ~/.pigo/prompts
// (non-recursive, global tier), and a same-named template in prompts/ overrides
// the one in commands/ (last-write-wins within the global tier).

import (
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/testutil"
)

// TestBuildSlashRegistryLoadsLegacyCommandsDir verifies the legacy
// ~/.pigo/commands directory still loads templates (regression).
func TestBuildSlashRegistryLoadsLegacyCommandsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	testutil.WritePrompt(t, home, "commands", "legacy.md", "Legacy: $ARGUMENTS")

	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil, promptTemplateSources{})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	out, err := reg.ResolveOutcome("/legacy hi")
	if err != nil {
		t.Fatalf("ResolveOutcome: %v", err)
	}
	if !out.Handled || out.Prompt != "Legacy: hi" {
		t.Errorf("/legacy = handled=%v prompt=%q, want handled=true \"Legacy: hi\"", out.Handled, out.Prompt)
	}
}

// TestBuildSlashRegistryLoadsPromptsDir verifies the pi-aligned ~/.pigo/prompts
// directory loads templates.
func TestBuildSlashRegistryLoadsPromptsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	testutil.WritePrompt(t, home, "prompts", "review.md", "Review: $ARGUMENTS")

	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil, promptTemplateSources{})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	out, err := reg.ResolveOutcome("/review diff")
	if err != nil {
		t.Fatalf("ResolveOutcome: %v", err)
	}
	if !out.Handled || out.Prompt != "Review: diff" {
		t.Errorf("/review = handled=%v prompt=%q, want handled=true \"Review: diff\"", out.Handled, out.Prompt)
	}
}

// TestBuildSlashRegistryPromptsOverridesCommands verifies that a same-named
// template in prompts/ overrides one in commands/ (both global tier; prompts is
// loaded second so last-write-wins), with no shadow entry.
func TestBuildSlashRegistryPromptsOverridesCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	testutil.WritePrompt(t, home, "commands", "dup.md", "FROM COMMANDS")
	testutil.WritePrompt(t, home, "prompts", "dup.md", "FROM PROMPTS")

	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil, promptTemplateSources{})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	cmd, ok := reg.Lookup("dup")
	if !ok {
		t.Fatal("/dup not found")
	}
	if got := cmd.Expand(""); got != "FROM PROMPTS" {
		t.Errorf("prompts should override commands on same name, got %q", got)
	}
	if len(reg.Shadowed()) != 0 {
		t.Errorf("same-tier override must not shadow, got %v", reg.Shadowed())
	}
}

// TestBuildSlashRegistryMissingDirsNoError verifies that with neither commands/
// nor prompts/ present, buildSlashRegistry returns no error (built-ins only).
func TestBuildSlashRegistryMissingDirsNoError(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil, promptTemplateSources{})
	if err != nil {
		t.Fatalf("buildSlashRegistry with no prompt dirs: %v", err)
	}
	if reg == nil {
		t.Fatal("registry is nil")
	}
}
