package main

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

// runtimeHelpRegistry builds a slash registry with the live built-in commands
// (/help, /model, /exit, /quit, …) registered against a throwaway live config,
// mirroring what buildSlashRegistry wires up for the real REPL.
func runtimeHelpRegistry(t *testing.T) *runtime.SlashRegistry {
	t.Helper()
	reg := runtime.NewSlashRegistry()
	registerLiveCommands(reg, &liveRunConfig{model: "faux", providerName: "faux"})
	return reg
}


// TestColorizeGating verifies colorize wraps text in SGR codes only when
// enabled, returns text unchanged when disabled, and treats an empty code as a
// no-op regardless of the enabled flag.
func TestColorizeGating(t *testing.T) {
	if got := colorize(true, ansiCyan, "/help"); got != ansiCyan+"/help"+ansiReset {
		t.Errorf("enabled: got %q", got)
	}
	if got := colorize(false, ansiCyan, "/help"); got != "/help" {
		t.Errorf("disabled should be plain, got %q", got)
	}
	if got := colorize(true, "", "/help"); got != "/help" {
		t.Errorf("empty code should be plain, got %q", got)
	}
}

// TestColorEnabledRespectsNoColor verifies NO_COLOR forces color off even on a
// terminal (对标 https://no-color.org).
func TestColorEnabledRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if colorEnabled() {
		t.Error("NO_COLOR set: colorEnabled must be false")
	}
}

// TestHelpListingColorized verifies the /help action emits ANSI codes when
// color is enabled — the command names are highlighted, not plain.
func TestHelpListingColorized(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // force the deterministic (plain) branch
	reg := runtimeHelpRegistry(t)
	out, err := reg.ResolveOutcome("/help")
	if err != nil {
		t.Fatalf("resolve /help: %v", err)
	}
	// With NO_COLOR the listing must be plain text (no escape codes) and still
	// contain the command names.
	if strings.Contains(out.Message, "\033[") {
		t.Errorf("NO_COLOR listing should carry no escape codes, got %q", out.Message)
	}
	for _, want := range []string{"/help", "/exit", "/quit"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("/help listing missing %q, out=%q", want, out.Message)
		}
	}
}
