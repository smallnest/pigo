package prompts

// Tests for --prompt-template (CLI tier) and --no-prompt-templates (US-008,
// #339): CLI paths load at the CLI tier, --no-prompt-templates suppresses all
// prompt-template discovery while leaving built-ins, and a global template
// overrides a same-named CLI one (global tier wins, CLI shadowed).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/testutil"
	"github.com/smallnest/pigo/internal/runtime"
)

func TestBuildSlashRegistryLoadsCLIPrompts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	// file entry
	filePath := filepath.Join(home, "single.md")
	if err := os.WriteFile(filePath, []byte("Single: $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dir entry
	dirPath := filepath.Join(home, "clidir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, "a.md"), []byte("A: $1"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := BuildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		PromptTemplateSources{CLI: []string{filePath, dirPath}})
	if err != nil {
		t.Fatalf("BuildSlashRegistry: %v", err)
	}
	out, err := reg.ResolveOutcome("/single hi")
	if err != nil {
		t.Fatalf("ResolveOutcome /single: %v", err)
	}
	if !out.Handled || out.Prompt != "Single: hi" {
		t.Errorf("/single = handled=%v prompt=%q, want \"Single: hi\"", out.Handled, out.Prompt)
	}
	out2, err := reg.ResolveOutcome("/a x")
	if err != nil {
		t.Fatalf("ResolveOutcome /a: %v", err)
	}
	if !out2.Handled || out2.Prompt != "A: x" {
		t.Errorf("/a = handled=%v prompt=%q, want \"A: x\"", out2.Handled, out2.Prompt)
	}
}

func TestBuildSlashRegistryNoPromptTemplatesDisables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	// A global prompt that should NOT load under --no-prompt-templates.
	testutil.WritePrompt(t, home, "prompts", "review.md", "Review: $ARGUMENTS")
	// A CLI path that should also be ignored under --no-prompt-templates.
	cliFile := filepath.Join(home, "cli.md")
	if err := os.WriteFile(cliFile, []byte("CLI: $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := BuildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		PromptTemplateSources{Disable: true, CLI: []string{cliFile}})
	if err != nil {
		t.Fatalf("BuildSlashRegistry: %v", err)
	}
	if _, ok := reg.Lookup("review"); ok {
		t.Error("/review should NOT be registered under --no-prompt-templates")
	}
	if _, ok := reg.Lookup("cli"); ok {
		t.Error("/cli should NOT be registered under --no-prompt-templates")
	}
	// Built-in slash commands are unaffected: the registry is non-empty.
	if len(reg.List()) == 0 {
		t.Error("built-in slash commands should still be registered under --no-prompt-templates")
	}
}

func TestBuildSlashRegistryGlobalOverridesCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	// global prompt (TierGlobal) under ~/.pigo/prompts.
	testutil.WritePrompt(t, home, "prompts", "dup.md", "FROM GLOBAL")
	// CLI-tier file of the same name at the home root (not under prompts/).
	cliFile := filepath.Join(home, "dup.md")
	if err := os.WriteFile(cliFile, []byte("FROM CLI"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := BuildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		PromptTemplateSources{CLI: []string{cliFile}})
	if err != nil {
		t.Fatalf("BuildSlashRegistry: %v", err)
	}
	cmd, ok := reg.Lookup("dup")
	if !ok {
		t.Fatal("/dup not found")
	}
	if got := cmd.Expand(""); got != "FROM GLOBAL" {
		t.Errorf("global should override CLI, got %q", got)
	}
	found := false
	for _, e := range reg.Shadowed() {
		if e.Name == "dup" && e.Tier == runtime.TierCLI {
			found = true
		}
	}
	if !found {
		t.Errorf("CLI dup should be shadowed with TierCLI, got %v", reg.Shadowed())
	}
}
