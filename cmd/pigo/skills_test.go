package main

// Tests for default skill loading from ~/.agents/skills and its exposure as
// /skill-name slash commands. skillsDir honors the PIGO_SKILLS_DIR override so
// the loader can be pointed at a temp dir without touching the real home dir.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

// writeSkill creates a skill markdown file with the given frontmatter body.
func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill %s: %v", name, err)
	}
}

// TestLoadSkillCommandsFromDir verifies skills in PIGO_SKILLS_DIR are loaded and
// exposed as /skill-name commands whose expansion is the skill body.
func TestLoadSkillCommandsFromDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", dir)
	writeSkill(t, dir, "greet.md", "---\nname: greet\ndescription: say hello\n---\nYou are a friendly greeter.")

	cmds, err := loadSkillCommands()
	if err != nil {
		t.Fatalf("loadSkillCommands: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("got %d commands, want 1", len(cmds))
	}
	c := cmds[0]
	if c.Name != "greet" {
		t.Errorf("Name = %q, want greet", c.Name)
	}
	if c.Description != "say hello" {
		t.Errorf("Description = %q, want 'say hello'", c.Description)
	}
	if c.Expand == nil {
		t.Fatal("skill command must be a prompt command (Expand != nil)")
	}
	if got := c.Expand(""); got != "You are a friendly greeter." {
		t.Errorf("Expand(\"\") = %q, want the skill body", got)
	}
}

// TestLoadSkillCommandsMissingDir verifies a missing skills directory yields no
// commands and no error (skills are optional).
func TestLoadSkillCommandsMissingDir(t *testing.T) {
	t.Setenv("PIGO_SKILLS_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	cmds, err := loadSkillCommands()
	if err != nil {
		t.Fatalf("missing dir must not error: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("got %d commands, want 0", len(cmds))
	}
}

// TestBuildSlashRegistryIncludesSkills verifies buildSlashRegistry wires loaded
// skills into the registry so /skill-name resolves.
func TestBuildSlashRegistryIncludesSkills(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", dir)
	// Keep the user-commands path from touching a real home dir.
	t.Setenv("PIGO_HOME", t.TempDir())
	writeSkill(t, dir, "summarize.md", "---\nname: summarize\ndescription: summarize input\n---\nSummarize the following: $ARGUMENTS")

	reg, err := buildSlashRegistry(&liveRunConfig{model: "test", providerName: "test"}, false)
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	out, err := reg.ResolveOutcome("/summarize hello world")
	if err != nil {
		t.Fatalf("ResolveOutcome: %v", err)
	}
	if !out.Handled {
		t.Fatal("/summarize should be handled by the registry")
	}
	if out.Kind != runtime.SlashPrompt {
		t.Errorf("Kind = %v, want SlashPrompt", out.Kind)
	}
	if out.Prompt != "Summarize the following: hello world" {
		t.Errorf("Prompt = %q, want $ARGUMENTS substituted", out.Prompt)
	}
}

// TestBuildSlashRegistryNoSkills verifies that when noSkills is true, skill
// discovery is skipped so a /skill-name command is not registered even though a
// skill file exists on disk (对标 pi 的 --no-skills).
func TestBuildSlashRegistryNoSkills(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", dir)
	t.Setenv("PIGO_HOME", t.TempDir())
	writeSkill(t, dir, "summarize.md", "---\nname: summarize\ndescription: summarize input\n---\nSummarize the following: $ARGUMENTS")

	reg, err := buildSlashRegistry(&liveRunConfig{model: "test", providerName: "test"}, true)
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	// With discovery disabled, /summarize was never registered, so the registry
	// treats it as an unknown command (an error) rather than a handled one.
	if _, err := reg.ResolveOutcome("/summarize hello world"); err == nil {
		t.Error("/summarize must be unknown when --no-skills disables discovery")
	}
}
