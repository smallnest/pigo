package main

// Tests for project-level .pigo/prompts (US-006, #337): loaded at the project
// tier only when the project is trusted, overrides global same-name templates,
// and is suppressed by --no-prompt-templates.

import (
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/testutil"
	"github.com/smallnest/pigo/internal/runtime"
)

// TestBuildSlashRegistryLoadsProjectPromptsTrusted: with the project trusted,
// .pigo/prompts/*.md loads at the project tier.
func TestBuildSlashRegistryLoadsProjectPromptsTrusted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home) // empty global
	cwdTmp := t.TempDir()
	testutil.WritePrompt(t, cwdTmp, filepath.Join(".pigo", "prompts"), "review.md", "Review: $ARGUMENTS")

	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		promptTemplateSources{
			projectDir:     filepath.Join(cwdTmp, ".pigo", "prompts"),
			projectTrusted: true,
		})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	out, err := reg.ResolveOutcome("/review diff")
	if err != nil {
		t.Fatalf("ResolveOutcome: %v", err)
	}
	if !out.Handled || out.Prompt != "Review: diff" {
		t.Errorf("/review = handled=%v prompt=%q, want \"Review: diff\"", out.Handled, out.Prompt)
	}
}

// TestBuildSlashRegistryProjectPromptsUntrustedSkipped: when the project is not
// trusted, .pigo/prompts is not loaded.
func TestBuildSlashRegistryProjectPromptsUntrustedSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	cwdTmp := t.TempDir()
	testutil.WritePrompt(t, cwdTmp, filepath.Join(".pigo", "prompts"), "review.md", "Review: $ARGUMENTS")

	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		promptTemplateSources{
			projectDir:     filepath.Join(cwdTmp, ".pigo", "prompts"),
			projectTrusted: false,
		})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	if _, ok := reg.Lookup("review"); ok {
		t.Error("/review should NOT load from an untrusted project")
	}
}

// TestBuildSlashRegistryProjectMissingDirNoError: a missing .pigo/prompts is
// not an error (most projects don't have one).
func TestBuildSlashRegistryProjectMissingDirNoError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	cwdTmp := t.TempDir() // no .pigo/prompts created

	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		promptTemplateSources{
			projectDir:     filepath.Join(cwdTmp, ".pigo", "prompts"),
			projectTrusted: true,
		})
	if err != nil {
		t.Fatalf("missing .pigo/prompts should not error, got %v", err)
	}
	if reg == nil {
		t.Fatal("registry is nil")
	}
}

// TestBuildSlashRegistryProjectOverridesGlobal: a project template overrides a
// same-named global one (project tier wins, global shadowed).
func TestBuildSlashRegistryProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	// global
	testutil.WritePrompt(t, home, "prompts", "dup.md", "FROM GLOBAL")
	// project
	cwdTmp := t.TempDir()
	testutil.WritePrompt(t, cwdTmp, filepath.Join(".pigo", "prompts"), "dup.md", "FROM PROJECT")

	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		promptTemplateSources{
			projectDir:     filepath.Join(cwdTmp, ".pigo", "prompts"),
			projectTrusted: true,
		})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	cmd, ok := reg.Lookup("dup")
	if !ok {
		t.Fatal("/dup not found")
	}
	if got := cmd.Expand(""); got != "FROM PROJECT" {
		t.Errorf("project should override global, got %q", got)
	}
	found := false
	for _, e := range reg.Shadowed() {
		if e.Name == "dup" && e.Tier == runtime.TierGlobal {
			found = true
		}
	}
	if !found {
		t.Errorf("global dup should be shadowed with TierGlobal, got %v", reg.Shadowed())
	}
}

// TestBuildSlashRegistryNoPromptTemplatesDisablesProject: --no-prompt-templates
// suppresses project prompts too.
func TestBuildSlashRegistryNoPromptTemplatesDisablesProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	cwdTmp := t.TempDir()
	testutil.WritePrompt(t, cwdTmp, filepath.Join(".pigo", "prompts"), "review.md", "Review: $ARGUMENTS")

	reg, err := buildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		promptTemplateSources{
			disable:        true,
			projectDir:     filepath.Join(cwdTmp, ".pigo", "prompts"),
			projectTrusted: true,
		})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	if _, ok := reg.Lookup("review"); ok {
		t.Error("/review should NOT load under --no-prompt-templates")
	}
}
