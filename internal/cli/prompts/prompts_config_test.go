package prompts

// Tests for settings-tier prompt templates (US-007, #338): LoadPromptPaths
// loads file/dir entries (warning on missing), and BuildSlashRegistry
// registers them at the settings tier (overridden by global same-name
// templates). The applyFileConfig parse test stays in package main
// (cmd/pigo/prompts_config_test.go) since it drives cliOptions.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/testutil"
	"github.com/smallnest/pigo/internal/runtime"
)

func TestLoadSettingsPromptsFileDirMissing(t *testing.T) {
	home := t.TempDir()
	// file entry -> 1 cmd.
	filePath := filepath.Join(home, "single.md")
	if err := os.WriteFile(filePath, []byte("Single: $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dir entry -> 2 cmds.
	dirPath := filepath.Join(home, "promptsdir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, "a.md"), []byte("A: $1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, "b.md"), []byte("B: $1"), 0o644); err != nil {
		t.Fatal(err)
	}
	// missing entry -> 0 cmds (warned, not fatal).
	missing := filepath.Join(home, "nope")

	cmds := LoadPromptPaths([]string{filePath, dirPath, missing})
	if len(cmds) != 3 {
		t.Fatalf("got %d cmds, want 3 (file=1 + dir=2 + missing=0)", len(cmds))
	}
	names := map[string]bool{}
	for _, c := range cmds {
		names[c.Name] = true
	}
	for _, want := range []string{"single", "a", "b"} {
		if !names[want] {
			t.Errorf("missing cmd %q; got %v", want, names)
		}
	}
}

func TestBuildSlashRegistryLoadsSettingsPrompts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	// A settings-tier prompt dir (loaded via configPrompts).
	settingsDir := filepath.Join(home, "settings-prompts")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "review.md"), []byte("FROM SETTINGS"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := BuildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		PromptTemplateSources{Settings: []string{settingsDir}})
	if err != nil {
		t.Fatalf("BuildSlashRegistry: %v", err)
	}
	cmd, ok := reg.Lookup("review")
	if !ok {
		t.Fatal("/review not found")
	}
	if got := cmd.Expand(""); got != "FROM SETTINGS" {
		t.Errorf("settings prompt: got %q, want \"FROM SETTINGS\"", got)
	}
}

func TestBuildSlashRegistryGlobalOverridesSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	// global prompt (TierGlobal) under ~/.pigo/prompts.
	testutil.WritePrompt(t, home, "prompts", "dup.md", "FROM GLOBAL")
	// settings-tier prompt (file) of the same name, placed at the home root
	// (not under prompts/, so the global loop does not also load it).
	settingsFile := filepath.Join(home, "dup.md")
	if err := os.WriteFile(settingsFile, []byte("FROM SETTINGS"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := BuildSlashRegistry(&cli.LiveConfig{Model: "test", ProviderName: "test"}, nil, nil,
		PromptTemplateSources{Settings: []string{settingsFile}})
	if err != nil {
		t.Fatalf("BuildSlashRegistry: %v", err)
	}
	cmd, ok := reg.Lookup("dup")
	if !ok {
		t.Fatal("/dup not found")
	}
	if got := cmd.Expand(""); got != "FROM GLOBAL" {
		t.Errorf("global should override settings, got %q", got)
	}
	// The settings loser is shadowed with TierSettings.
	found := false
	for _, e := range reg.Shadowed() {
		if e.Name == "dup" && e.Tier == runtime.TierSettings {
			found = true
		}
	}
	if !found {
		t.Errorf("settings dup should be shadowed with TierSettings, got %v", reg.Shadowed())
	}
}
