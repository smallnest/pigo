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

// findSkill returns the skill with the given name from the set, or nil.
func findSkill(skills []*runtime.Skill, name string) *runtime.Skill {
	for _, s := range skills {
		if s.Frontmatter.Name == name {
			return s
		}
	}
	return nil
}

// TestLoadSkillsFromDir verifies skills in PIGO_SKILLS_DIR are loaded and expose
// a /skill-name slash command whose expansion is the skill body.
func TestLoadSkillsFromDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", dir)
	t.Setenv("PIGO_HOME", t.TempDir())
	writeSkill(t, dir, "greet.md", "---\nname: greet\ndescription: say hello\n---\nYou are a friendly greeter.")

	skills, err := loadSkills(false)
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	s := findSkill(skills, "greet")
	if s == nil {
		t.Fatal("greet skill not loaded")
	}
	c := s.SlashCommand()
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

// TestLoadSkillsNoSkills verifies --no-skills skips discovery entirely: no
// skills are loaded and the skills dir is left untouched (no bootstrap).
func TestLoadSkillsNoSkills(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", dir)
	t.Setenv("PIGO_HOME", t.TempDir())

	skills, err := loadSkills(true)
	if err != nil {
		t.Fatalf("loadSkills(true): %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("got %d skills, want 0 under --no-skills", len(skills))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read skills dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("--no-skills must not bootstrap; skills dir has %d entries", len(entries))
	}
}

// TestBuildSlashRegistryIncludesSkills verifies buildSlashRegistry wires the
// pre-loaded skills into the registry so /skill-name resolves.
func TestBuildSlashRegistryIncludesSkills(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", dir)
	// Keep the user-commands path from touching a real home dir.
	t.Setenv("PIGO_HOME", t.TempDir())
	writeSkill(t, dir, "summarize.md", "---\nname: summarize\ndescription: summarize input\n---\nSummarize the following: $ARGUMENTS")

	skills, err := loadSkills(false)
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	reg, err := buildSlashRegistry(&liveRunConfig{model: "test", providerName: "test"}, skills, nil)
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

// TestBuildSlashRegistryNoSkills verifies that when no skills are passed (as
// under --no-skills, where loadSkills returns nil), a /skill-name command is not
// registered even though a skill file exists on disk (对标 pi 的 --no-skills).
func TestBuildSlashRegistryNoSkills(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())

	reg, err := buildSlashRegistry(&liveRunConfig{model: "test", providerName: "test"}, nil, nil)
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	// With no skills registered, /summarize is an unknown command (an error)
	// rather than a handled one.
	if _, err := reg.ResolveOutcome("/summarize hello world"); err == nil {
		t.Error("/summarize must be unknown when no skills are registered")
	}
}

// TestLoadSkillsBootstrapsBuiltinSkills verifies that on a fresh
// PIGO_SKILLS_DIR the built-in skills are installed and loaded (first-run
// bootstrap), so e.g. /prd resolves without any manual install.
func TestLoadSkillsBootstrapsBuiltinSkills(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", dir)
	t.Setenv("PIGO_HOME", t.TempDir())

	skills, err := loadSkills(false)
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	reg, err := buildSlashRegistry(&liveRunConfig{model: "test", providerName: "test"}, skills, nil)
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	for _, name := range []string{"/prd", "/refactor", "/architecture-diagram", "/weather"} {
		out, err := reg.ResolveOutcome(name)
		if err != nil {
			t.Errorf("%s should be registered after bootstrap: %v", name, err)
			continue
		}
		if !out.Handled {
			t.Errorf("%s should be handled by the registry after bootstrap", name)
		}
	}
}
