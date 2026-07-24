package main

// Tests for /btw discoverability (#283, US-006/FR-10): /btw must appear in the
// /help listing and be completable at the REPL slash prompt. Both flow from
// registering "btw" as a listed built-in in registerLiveCommands; these tests
// pin that so the command can't silently drop off help/completion.

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/runtime"
)

// TestBtwListedInHelp verifies registerLiveCommands registers /btw so /help (and
// any consumer of reg.List()) surfaces it with a usage description.
func TestBtwListedInHelp(t *testing.T) {
	reg := runtime.NewSlashRegistry()
	registerLiveCommands(reg, &liveRunConfig{})

	var btw *runtime.SlashCommand
	for _, c := range reg.List() {
		if c.Name == "btw" {
			cc := c
			btw = &cc
			break
		}
	}
	if btw == nil {
		t.Fatalf("registerLiveCommands must register /btw so /help lists it")
	}
	if !strings.Contains(btw.Description, "side question") {
		t.Errorf("/btw help description should explain the side question, got %q", btw.Description)
	}
}

// TestBtwSlashCompletion verifies the line editor completes "/b" to "/btw" once
// the command is registered — the same reg.List() drives both help and
// completion, so registration is all that's needed.
func TestBtwSlashCompletion(t *testing.T) {
	reg := runtime.NewSlashRegistry()
	registerLiveCommands(reg, &liveRunConfig{})
	e := newREPLLineEditor(nil, nil, nil, reg, nil)

	cands := e.suggestions("/bt")
	found := false
	for _, c := range cands {
		if c == "/btw" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected /btw among completions for %q, got %v", "/bt", cands)
	}
}
