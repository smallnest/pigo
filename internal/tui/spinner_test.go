package tui

// Tests for the streaming status indicator and tool-call cards (#93): a spinner
// animates while a run is in flight and stops when idle, and tool-call/result
// entries render as styled cards distinct from plain text. These drive
// Update/View with synthetic messages — no live terminal.

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// TestSpinnerTickAdvancesWhileRunning verifies a spinner.TickMsg advances the
// animation while a run is in flight and returns a follow-up tick command (so
// the animation keeps going without blocking Update).
func TestSpinnerTickAdvancesWhileRunning(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	m.state.running = true
	_, cmd := m.Update(m.spinner.Tick())
	if cmd == nil {
		t.Error("spinner tick while running should schedule the next tick")
	}
}

// TestSpinnerTickHaltsWhenIdle is acceptance-critical: once the run ends
// (running == false), a stray tick must NOT re-schedule itself, so the
// animation stops rather than spinning forever.
func TestSpinnerTickHaltsWhenIdle(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	m.state.running = false
	_, cmd := m.Update(spinner.TickMsg{})
	if cmd != nil {
		t.Error("spinner tick while idle must not schedule another tick")
	}
}

// TestRunningStatusShowsSpinnerAndHint verifies View shows the animated spinner
// plus the preserved running hint (steering + Ctrl+C interrupt semantics) while
// a run is in flight.
func TestRunningStatusShowsSpinnerAndHint(t *testing.T) {
	withNoColor(t, true) // strip ANSI so we can assert on plain text
	m := NewModel(nopRun(new(string)))
	m.state.running = true
	out := m.View().Content
	if !strings.Contains(out, "running") || !strings.Contains(out, "Ctrl+C") {
		t.Errorf("running status must preserve steer/interrupt hint, got %q", out)
	}
	// The MiniDot spinner's first frame is a braille dot; assert some spinner
	// glyph is present alongside the hint.
	frame := spinner.MiniDot.Frames[0]
	if !strings.Contains(out, frame) {
		t.Errorf("running view must include spinner frame %q, got %q", frame, out)
	}
}

// TestIdleStatusNoSpinner verifies that when idle, no running hint/spinner is
// shown (the composer stands alone).
func TestIdleStatusNoSpinner(t *testing.T) {
	withNoColor(t, true)
	m := NewModel(nopRun(new(string)))
	m.state.running = false
	out := m.View().Content
	if strings.Contains(out, "running") {
		t.Errorf("idle view must not show the running hint, got %q", out)
	}
}

// TestToolCardsAreStyledDistinctly is acceptance-critical: tool-call and
// tool-result entries must be visually distinct from plain assistant text. With
// color enabled, the styled tool lines carry ANSI escapes (their card styling),
// whereas the raw rendered text does not.
func TestToolCardsAreStyledDistinctly(t *testing.T) {
	withNoColor(t, false) // ensure colors are active
	m := NewModel(nopRun(new(string)))

	callStyled := m.styleEntry(entryToolCall, "⚙ read_file")
	resultStyled := m.styleEntry(entryToolResult, "  ↳ ok")
	if !strings.Contains(callStyled, "\x1b[") {
		t.Errorf("tool-call card must be styled (ANSI), got %q", callStyled)
	}
	if !strings.Contains(resultStyled, "\x1b[") {
		t.Errorf("tool-result card must be styled (ANSI), got %q", resultStyled)
	}
	// Tool call and tool result use different palette colors, so their styled
	// output must differ even for the same inner text.
	if a, b := m.styleEntry(entryToolCall, "x"), m.styleEntry(entryToolResult, "x"); a == b {
		t.Errorf("tool-call and tool-result should style differently, both = %q", a)
	}
}

// TestStyledEntryPreservesGlyphUnderNoColor verifies that under NO_COLOR the
// tool cards fall back to plain text (no ANSI) but keep their distinguishing
// prefix glyph, so meaning never relies on color alone.
func TestStyledEntryPreservesGlyphUnderNoColor(t *testing.T) {
	withNoColor(t, true)
	m := NewModel(nopRun(new(string)))
	got := m.styleEntry(entryToolCall, "⚙ read_file")
	if strings.Contains(got, "\x1b[") {
		t.Errorf("NO_COLOR tool card must be plain, got %q", got)
	}
	if !strings.Contains(got, "⚙") {
		t.Errorf("tool card must keep its glyph under NO_COLOR, got %q", got)
	}
}

// TestSpinnerStartsOnRunAndBatchesTick verifies startRun returns a command batch
// that includes the spinner tick, so the animation begins when a run starts.
func TestSpinnerStartsOnRunAndBatchesTick(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	typeRunes(m, "hi")
	_, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting a prompt should return a command (drain + spinner tick)")
	}
	// Executing the batched command should eventually surface a spinner.TickMsg
	// among the produced messages.
	if !producesSpinnerTick(cmd) {
		t.Error("startRun batch should include the spinner tick command")
	}
}

// producesSpinnerTick runs a (possibly batched) command and reports whether any
// resulting message is a spinner.TickMsg. It handles tea.BatchMsg by executing
// each sub-command.
func producesSpinnerTick(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	switch mm := msg.(type) {
	case spinner.TickMsg:
		return true
	case tea.BatchMsg:
		for _, c := range mm {
			if producesSpinnerTick(c) {
				return true
			}
		}
	}
	return false
}
