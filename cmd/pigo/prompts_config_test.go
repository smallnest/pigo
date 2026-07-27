package main

// Tests for config.toml `prompts` array -> settings-tier prompt templates
// (US-007, #338): fileConfig parses the array, applyFileConfig passes it to
// cliOptions, loadSettingsPrompts loads file/dir entries (warning on missing),
// and buildSlashRegistry registers them at the settings tier (overridden by
// global same-name templates).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

func TestLoadFileConfigPromptsArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "prompts = [\"./my-prompts\", \"/abs/x.md\"]\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFileConfig(path)
	if err != nil {
		t.Fatalf("loadFileConfig: %v", err)
	}
	if len(cfg.Prompts) != 2 || cfg.Prompts[0] != "./my-prompts" || cfg.Prompts[1] != "/abs/x.md" {
		t.Errorf("Prompts = %v, want [./my-prompts /abs/x.md]", cfg.Prompts)
	}
}

func TestApplyFileConfigPrompts(t *testing.T) {
	var opts cliOptions
	cfg := fileConfig{Prompts: []string{"./my-prompts", "/abs/x.md"}}
	applyFileConfig(&opts, cfg, func(string) bool { return false })
	if len(opts.configPrompts) != 2 || opts.configPrompts[0] != "./my-prompts" || opts.configPrompts[1] != "/abs/x.md" {
		t.Errorf("opts.configPrompts = %v, want [./my-prompts /abs/x.md]", opts.configPrompts)
	}
}

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

	cmds := loadPromptPaths([]string{filePath, dirPath, missing})
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

	reg, err := buildSlashRegistry(&liveRunConfig{model: "test", providerName: "test"}, nil, nil,
		promptTemplateSources{settings: []string{settingsDir}})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
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
	writePrompt(t, home, "prompts", "dup.md", "FROM GLOBAL")
	// settings-tier prompt (file) of the same name, placed at the home root
	// (not under prompts/, so the global loop does not also load it).
	settingsFile := filepath.Join(home, "dup.md")
	if err := os.WriteFile(settingsFile, []byte("FROM SETTINGS"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := buildSlashRegistry(&liveRunConfig{model: "test", providerName: "test"}, nil, nil,
		promptTemplateSources{settings: []string{settingsFile}})
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
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
