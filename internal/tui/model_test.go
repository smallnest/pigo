package tui

// Tests for the bubbletea Model's key handling, focused on #90: multi-byte
// UTF-8 (CJK/emoji) input and rune-level editing via the bubbles textinput
// widget. These drive handleKey with synthetic key presses — no live terminal —
// and assert the composer value and the prompt handed to submit.

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/runtime"
)

// runeKey builds a KeyPressMsg for a single printable rune, mirroring what the
// terminal input reader produces: Code is the rune and Text carries the
// printable character(s) that textinput inserts.
func runeKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// typeRunes feeds each rune of s to handleKey as an individual key press.
func typeRunes(m *Model, s string) {
	for _, r := range s {
		m.handleKey(runeKey(r))
	}
}

// nopRun is a RunFn that records the prompt it was started with and returns an
// already-closed event stream so the model does not block.
func nopRun(captured *string) RunFn {
	return func(ctx context.Context, prompt string, steering func() []string) (*runtime.LoopEventStream, context.CancelFunc) {
		*captured = prompt
		stream := agentcore.NewEventStream[agentcore.AgentEvent, []agentcore.AgentMessage](0)
		stream.SetResult(nil)
		stream.Close()
		return stream, func() {}
	}
}

// TestComposerAcceptsCJKInput is the acceptance-critical #90 case: typing a
// multi-byte CJK string must appear verbatim in the composer, not be dropped
// (the old handleKey default only kept len(s)==1 keys, silently discarding CJK).
func TestComposerAcceptsCJKInput(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	typeRunes(m, "你好世界")
	if got := m.input.Value(); got != "你好世界" {
		t.Errorf("composer value = %q, want 你好世界", got)
	}
}

// TestComposerMixedInput verifies mixed ASCII/CJK/emoji all survive input.
func TestComposerMixedInput(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	typeRunes(m, "abc中文🌍")
	if got := m.input.Value(); got != "abc中文🌍" {
		t.Errorf("composer value = %q, want abc中文🌍", got)
	}
}

// TestBackspaceDeletesWholeRune verifies backspace removes one whole CJK rune
// at a time (not one byte, which would corrupt the UTF-8), fixing the old
// byte-slice truncation.
func TestBackspaceDeletesWholeRune(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	typeRunes(m, "你好世界")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.input.Value(); got != "你好世" {
		t.Errorf("after one backspace = %q, want 你好世", got)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.input.Value(); got != "你好" {
		t.Errorf("after two backspaces = %q, want 你好", got)
	}
}

// TestSubmitSendsExactCJKPromptAndClears verifies Enter hands the exact typed
// CJK text to the run and clears the composer, and that the transcript echoes
// the same text (UTF-8 preserved end to end).
func TestSubmitSendsExactCJKPromptAndClears(t *testing.T) {
	var got string
	m := NewModel(nopRun(&got))
	typeRunes(m, "你好，帮我读 README")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if got != "你好，帮我读 README" {
		t.Errorf("prompt sent to run = %q, want 你好，帮我读 README", got)
	}
	if m.input.Value() != "" {
		t.Errorf("composer must clear after submit, got %q", m.input.Value())
	}
	last := m.state.transcript[len(m.state.transcript)-1]
	if last.Kind != entryUser || last.Text != "你好，帮我读 README" {
		t.Errorf("transcript echo = %+v, want user 你好，帮我读 README", last)
	}
}

// TestTypingDisarmsCtrlC verifies that typing (delegated to textinput) still
// disarms the two-phase Ctrl+C, preserving the pre-#90 semantics.
func TestTypingDisarmsCtrlC(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	if act := m.state.pressCtrlC(); act != "arm" {
		t.Fatalf("first Ctrl+C = %q, want arm", act)
	}
	typeRunes(m, "中") // any keystroke disarms
	if m.state.ctrlCArmed {
		t.Error("typing must disarm Ctrl+C")
	}
}
