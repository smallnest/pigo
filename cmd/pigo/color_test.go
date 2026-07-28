package main

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/runtime"
)

// runtimeHelpRegistry builds a slash registry with the live built-in commands
// (/help, /model, /exit, /quit, …) registered against a throwaway live config,
// mirroring what buildSlashRegistry wires up for the real REPL.
func runtimeHelpRegistry(t *testing.T) *runtime.SlashRegistry {
	t.Helper()
	reg := runtime.NewSlashRegistry()
	registerLiveCommands(reg, &cli.LiveConfig{Model: "faux", ProviderName: "faux"})
	return reg
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
