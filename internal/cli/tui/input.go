package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
//
// Shift+Enter inserts a newline so the editor is a true multi-line composer;
// plain Enter submits (intercepted by the model, never reaching textarea). The
// default InsertNewline binding (Enter) is therefore rebound to Shift+Enter. See
// model.handleKey.

// maxInputRows caps how tall the editor grows as the user adds lines. Past this
// the buffer keeps growing but textarea scrolls its own viewport, so the shell's
// row accounting stays bounded and the transcript never collapses to nothing.
const maxInputRows = 6

// input is the prompt editor. It embeds a textarea.Model and exposes just the
// surface the root model needs: value/clear, focus/blur (input is blurred while
// a run is in flight so keystrokes never corrupt an in-flight prompt), a width
// setter driven by tea.WindowSizeMsg, and a render string for View.
type input struct {
	ta textarea.Model
	// width is the full editor width (terminal columns) last set via SetWidth. It
	// is the span of the top/bottom rules drawn around the editor (Claude-Code
	// style), kept separately because textarea's own Width() reports only the
	// inner text area (prompt column excluded).
	width int
}

// newInput builds a focused editor. It starts one row tall and grows with the
// buffer (up to maxInputRows) as the user inserts newlines with Enter. The
// buffer itself is unbounded; beyond maxInputRows textarea scrolls internally.
func newInput() input {
	ta := textarea.New()
	ta.Prompt = "> "
	ta.Placeholder = "输入消息…（Enter 发送，Shift+Enter / Ctrl+J 换行）"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = maxInputRows
	ta.SetHeight(1)
	// Rebind InsertNewline from its default (Enter) to keys that break a line in
	// the multi-line composer, since plain Enter is the model's submit key
	// (handleKey intercepts it before textarea sees it). Shift+Enter is the
	// primary binding, but it is only distinguishable from Enter on terminals
	// that speak the Kitty keyboard protocol (kitty, ghostty, wezterm, recent
	// iTerm2). On terminals without it (macOS Terminal.app, tmux by default)
	// Shift+Enter arrives byte-identical to Enter and would submit — dropping the
	// line the user meant to continue. Ctrl+J (a literal LF, always distinct from
	// Enter's CR) and Alt+Enter are reliable fallbacks so a newline works
	// everywhere; all three split the line at the cursor and keep typed text.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "ctrl+j", "alt+enter"),
		key.WithHelp("shift+enter", "insert newline"),
	)
	// Draw the cursor into the rendered string: the model composes View as a
	// plain string rather than driving textarea's real cursor reporting.
	ta.SetVirtualCursor(true)
	// Drop the default cursor-line background highlight so the composer is framed
	// only by the top/bottom rules (see View), matching Claude Code — no fill.
	styles := ta.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(styles)
	ta.Focus()
	return input{ta: ta}
}

// Update forwards a message (typically a key press) to the underlying textarea
// and returns the updated component. The model calls this only for keys it does
// not intercept itself (submit / interrupt / quit), so textarea sees ordinary
// editing keys — including Enter (newline) and CJK / emoji runes, which it
// inserts whole. The visible height is re-synced to the line count afterwards so
// the editor grows and shrinks with the content.
func (in input) Update(msg tea.Msg) (input, tea.Cmd) {
	var cmd tea.Cmd
	in.ta, cmd = in.ta.Update(msg)
	in.syncHeight()
	return in, cmd
}

// syncHeight resizes the visible editor to the buffer's line count, clamped to
// [1, maxInputRows]. It is called after every edit and after programmatic value
// changes so the shell reserves exactly as many rows as the input needs.
func (in *input) syncHeight() {
	n := in.ta.LineCount()
	if n < 1 {
		n = 1
	}
	if n > maxInputRows {
		n = maxInputRows
	}
	in.ta.SetHeight(n)
}

// Height reports the current visible row count of the editor so the model can
// reserve that many rows in its View layout. It includes the two rule rows (top
// and bottom) drawn around the textarea.
func (in input) Height() int { return in.ta.Height() + 2 }

// Value returns the current buffer contents, including any embedded newlines.
func (in input) Value() string { return in.ta.Value() }

// SetValue replaces the buffer contents and moves the cursor to the end. It is
// used by slash autocomplete (Tab) to complete the buffer to the chosen command.
func (in *input) SetValue(s string) {
	in.ta.SetValue(s)
	in.syncHeight()
}

// Clear empties the buffer and resets the cursor to the start.
func (in *input) Clear() {
	in.ta.Reset()
	in.syncHeight()
}

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
	in.width = w
	in.ta.SetWidth(w)
}

// View renders the editor to a string for embedding in the model's View. The
// textarea is framed with a top and bottom rule (no side borders) in the muted
// gray, mirroring Claude Code's composer — a pair of horizontal lines rather
// than a background fill. The rules span the full editor width.
func (in input) View() string {
	style := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, true, false).
		BorderForeground(lipgloss.Color(colorGray))
	if in.width > 0 {
		style = style.Width(in.width)
	}
	return style.Render(in.ta.View())
}
