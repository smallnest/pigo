package main

// Tests for resolveThinkingLevel: the CLI end of the layered config chain
// (US-023). It resolves the effective reasoning-effort level through
// default < global ($PIGO_HOME/config.json) < project (./.pigo/config.json)
// < env (PIGO_THINKING_LEVEL) < --thinking-level flag, and rejects an invalid
// value. Each test isolates PIGO_HOME and the working directory so it never
// reads the developer's real config.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// isolateConfig points PIGO_HOME at a temp dir and chdir's into a temp working
// directory (restored on cleanup), so no real global/project config leaks in.
func isolateConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	t.Setenv("PIGO_THINKING_LEVEL", "")

	wd := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return home
}

// writeConfig writes a config.json layer with the given thinkingLevel at path.
func writeConfig(t *testing.T, path, level string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"thinkingLevel":"` + level + `"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestResolveThinkingLevelDefault verifies the built-in default (medium) applies
// when no layer sets a level.
func TestResolveThinkingLevelDefault(t *testing.T) {
	isolateConfig(t)
	got, err := resolveThinkingLevel("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != agentcore.ThinkingMedium {
		t.Errorf("level = %q, want medium", got)
	}
}

// TestResolveThinkingLevelFlagWins verifies the --thinking-level flag overrides
// every lower layer (global, project, and env).
func TestResolveThinkingLevelFlagWins(t *testing.T) {
	home := isolateConfig(t)
	writeConfig(t, filepath.Join(home, "config.json"), "low")
	writeConfig(t, filepath.Join(".pigo", "config.json"), "high")
	t.Setenv("PIGO_THINKING_LEVEL", "minimal")

	got, err := resolveThinkingLevel("xhigh")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != agentcore.ThinkingXHigh {
		t.Errorf("level = %q, want xhigh (flag wins)", got)
	}
}

// TestResolveThinkingLevelEnvOverProject verifies env beats project, and project
// beats global (precedence: global < project < env).
func TestResolveThinkingLevelEnvOverProject(t *testing.T) {
	home := isolateConfig(t)
	writeConfig(t, filepath.Join(home, "config.json"), "low")
	writeConfig(t, filepath.Join(".pigo", "config.json"), "high")
	t.Setenv("PIGO_THINKING_LEVEL", "off")

	got, err := resolveThinkingLevel("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != agentcore.ThinkingOff {
		t.Errorf("level = %q, want off (env over project/global)", got)
	}
}

// TestResolveThinkingLevelProjectOverGlobal verifies the project layer overrides
// the global layer when env and flag are unset.
func TestResolveThinkingLevelProjectOverGlobal(t *testing.T) {
	home := isolateConfig(t)
	writeConfig(t, filepath.Join(home, "config.json"), "low")
	writeConfig(t, filepath.Join(".pigo", "config.json"), "high")

	got, err := resolveThinkingLevel("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != agentcore.ThinkingHigh {
		t.Errorf("level = %q, want high (project over global)", got)
	}
}

// TestResolveThinkingLevelInvalid verifies an unknown value is a hard error
// (surfaced for exit-code mapping), not silently coerced.
func TestResolveThinkingLevelInvalid(t *testing.T) {
	isolateConfig(t)
	if _, err := resolveThinkingLevel("turbo"); err == nil {
		t.Error("expected error for invalid thinking level, got nil")
	}
}
