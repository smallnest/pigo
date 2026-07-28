package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// This file implements the prompt input field of the full-screen TUI (US-007,
// FR-11/13/14). It wraps charm.land/bubbles/v2/textarea into a small `input`
// component so the model can embed a real multi-line editor instead of the
// throwaway string buffer the skeleton shipped with.
//
// Why textarea rather than a hand-rolled buffer: textarea edits by grapheme /
// rune, so CJK and emoji are inserted and deleted whole. This is exactly the
// class of bug the old REPL input had — it keyed on byte length (len==1) and
// silently dropped the trailing bytes of every multi-byte rune. We deliberately
// delegate all character handling to textarea and never touch bytes ourselves.

// newlineKey is the key chord that inserts a literal newline into the buffer.
// Enter is reserved by the model for submitting the prompt, so a hard line break
// is entered with Alt+Enter (ctrl+j is kept as a fallback for terminals that
// cannot emit alt+enter).
const newlineKey = "alt+enter"

// input is the prompt editor. It embeds a textarea.Model and exposes just the
// surface the root model needs: value/clear, focus/blur (input is blurred while
// a run is in flight so keystrokes never corrupt an in-flight prompt), a width
// setter driven by tea.WindowSizeMsg, and a render string for View.
type input struct {
	ta textarea.Model
}

// newInput builds a focused single-visible-row editor. The visible height stays
// at one row so the model's View row accounting (transcript + status + input)
// is unchanged; the buffer itself is unbounded and grows across newlines, and
// textarea scrolls its own viewport when the content exceeds the visible row.
func newInput() input {
	ta := textarea.New()
	ta.Prompt = "> "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	// Enter must NOT insert a newline: the model intercepts it as submit. Rebind
	// the newline action to Alt+Enter (see newlineKey) so multi-line input still
	// works while Enter stays free for submission.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys(newlineKey, "ctrl+j"),
		key.WithHelp(newlineKey, "insert newline"),
	)
	// Draw the cursor into the rendered string: the model composes View as a
	// plain string rather than driving textarea's real cursor reporting.
	ta.SetVirtualCursor(true)
	ta.Focus()
	return input{ta: ta}
}

// Update forwards a message (typically a key press) to the underlying textarea
// and returns the updated component. The model calls this only for keys it does
// not intercept itself (submit / newline / interrupt / quit), so textarea sees
// ordinary editing keys — including CJK and emoji runes, which it inserts whole.
func (in input) Update(msg tea.Msg) (input, tea.Cmd) {
	var cmd tea.Cmd
	in.ta, cmd = in.ta.Update(msg)
	return in, cmd
}

// Value returns the current buffer contents, including any embedded newlines.
func (in input) Value() string { return in.ta.Value() }

// SetValue replaces the buffer contents and moves the cursor to the end. It is
// used by slash autocomplete (Tab) to complete the buffer to the chosen command.
func (in *input) SetValue(s string) { in.ta.SetValue(s) }

// Clear empties the buffer and resets the cursor to the start.
func (in *input) Clear() { in.ta.Reset() }

// Focus enables editing and returns the cursor-blink Cmd.
func (in *input) Focus() tea.Cmd { return in.ta.Focus() }

// Blur disables editing (used while a run is in flight).
func (in *input) Blur() { in.ta.Blur() }

// Focused reports whether the editor currently accepts input.
func (in input) Focused() bool { return in.ta.Focused() }

// SetWidth resizes the editor to the terminal width so wrapping and the prompt
// column line up with the rest of the shell.
func (in *input) SetWidth(w int) {
	if w < 0 {
		w = 0
	}
	in.ta.SetWidth(w)
}

// View renders the editor to a string for embedding in the model's View.
func (in input) View() string { return in.ta.View() }
