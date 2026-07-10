package tui

// Tests for slash-command dispatch in the TUI: a prompt command expands and
// starts a run; an action command (e.g. /model) runs its side effect and shows
// a status line WITHOUT starting a run; an unknown command surfaces an error
// line and starts no run. These drive handleKey's Enter path with a wired
// SlashRegistry — no live terminal.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/runtime"
)

// TestSlashActionDoesNotStartRun is acceptance-critical: submitting an action
// command must run its side effect and echo the status, but NOT hand any prompt
// to the run func (no agent turn starts).
func TestSlashActionDoesNotStartRun(t *testing.T) {
	var started string
	m := NewModel(nopRun(&started))
	reg := runtime.NewSlashRegistry()
	var ran bool
	reg.AddBuiltin(runtime.SlashCommand{
		Name:   "model",
		Action: func(args string) string { ran = true; return "model: fake-model" },
	})
	m.SetSlashRegistry(reg)

	typeRunes(m, "/model")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !ran {
		t.Error("action command side effect did not run")
	}
	if started != "" {
		t.Errorf("action command must not start a run, but run got prompt %q", started)
	}
	if m.state.running {
		t.Error("state must not be running after an action command")
	}
	last := m.state.transcript[len(m.state.transcript)-1]
	if last.Kind != entrySystem || !strings.Contains(last.Text, "fake-model") {
		t.Errorf("action status not echoed as system line, got %+v", last)
	}
}

// TestSlashPromptCommandStartsRun verifies a prompt command still expands and
// starts a run (regression guard for the outcome refactor).
func TestSlashPromptCommandStartsRun(t *testing.T) {
	var started string
	m := NewModel(nopRun(&started))
	reg := runtime.NewSlashRegistry()
	reg.AddUser(runtime.SlashCommand{Name: "greet", Expand: func(args string) string { return "hello " + args }})
	m.SetSlashRegistry(reg)

	typeRunes(m, "/greet world")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if started != "hello world" {
		t.Errorf("prompt command started run with %q, want 'hello world'", started)
	}
	if !m.state.running {
		t.Error("state should be running after a prompt command starts a run")
	}
}

// TestSlashUnknownCommandStartsNoRun verifies an unknown "/name" surfaces the
// error as a local system line and starts no run.
func TestSlashUnknownCommandStartsNoRun(t *testing.T) {
	var started string
	m := NewModel(nopRun(&started))
	m.SetSlashRegistry(runtime.NewSlashRegistry())

	typeRunes(m, "/definitely-not-a-command")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if started != "" {
		t.Errorf("unknown command must not start a run, got prompt %q", started)
	}
	if m.state.running {
		t.Error("state must not be running after an unknown command")
	}
	last := m.state.transcript[len(m.state.transcript)-1]
	if last.Kind != entrySystem || !strings.Contains(last.Text, "unknown command") {
		t.Errorf("unknown command should surface an error system line, got %+v", last)
	}
}
