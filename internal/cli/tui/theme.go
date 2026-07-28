package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// Theme bundles the lipgloss styles for every visual element the TUI paints so
// the transcript, tool cards and status bar share one palette instead of each
// call site hand-rolling colors (see tasks/spec-tui-agent.md Sections 2.2, 5.1).
// The reference palette is: success green, error/warn red & yellow, file/accent
// blue, and gray for secondary chrome. Styles are plain value types, so a Theme
// is cheap to copy and safe to pass by value.
type Theme struct {
	// User styles the human's turns in the transcript.
	User lipgloss.Style
	// Assistant styles the model's turns in the transcript.
	Assistant lipgloss.Style
	// System styles system / meta notices (secondary gray).
	System lipgloss.Style
	// ToolHeader styles the title line of a tool invocation card.
	ToolHeader lipgloss.Style
	// ToolBody styles the body/output region of a tool card.
	ToolBody lipgloss.Style
	// StatusBar styles the persistent bottom status bar.
	StatusBar lipgloss.Style
	// Accent styles file names and other highlighted tokens (blue).
	Accent lipgloss.Style
	// Error styles failure messages (red).
	Error lipgloss.Style
	// Warn styles warnings (yellow).
	Warn lipgloss.Style
	// Success styles successful outcomes (green).
	Success lipgloss.Style
	// ScrollThumb styles the transcript scrollbar thumb (medium gray block).
	ScrollThumb lipgloss.Style
	// ScrollTrack styles the transcript scrollbar track (dim shaded column).
	ScrollTrack lipgloss.Style
}

// Palette color numbers use the ANSI 256-color cube so the theme renders
// consistently across terminals without depending on true-color support.
const (
	colorSuccess = "42"  // green
	colorError   = "196" // red
	colorWarn    = "214" // yellow/amber
	colorAccent  = "39"  // blue (file names, highlights)
	colorGray    = "245" // secondary / muted text
	colorScroll  = "244" // scrollbar thumb (medium gray, visible on light+dark)
	colorTrack   = "252" // scrollbar track (very light gray, subtle)
	colorUser    = "15"  // bright white
	colorAssist  = "252" // near-white
	colorStatus  = "62"  // status bar background (violet)
)

// DefaultTheme returns the built-in palette described in the SPEC: success
// green, error/warn red & yellow, file/accent blue, and gray for secondary
// chrome. It performs no I/O and never panics, so callers can construct it
// eagerly at startup.
func DefaultTheme() Theme {
	return Theme{
		User: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorUser)).
			Bold(true),
		Assistant: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorAssist)),
		System: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGray)).
			Italic(true),
		ToolHeader: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorAccent)).
			Bold(true),
		ToolBody: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGray)),
		StatusBar: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorUser)).
			Background(lipgloss.Color(colorStatus)),
		Accent: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorAccent)),
		Error: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorError)).
			Bold(true),
		Warn: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorWarn)),
		Success: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorSuccess)),
		ScrollThumb: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorScroll)),
		ScrollTrack: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorTrack)),
	}
}

// ellipsis is the single rune appended to truncated strings. It is itself a
// single display column, so it is cheap to reserve room for.
const ellipsis = "…"

// WrapToWidth wraps s to at most width display columns per line, measuring width
// by terminal cells (CJK and emoji count as two) via ui.Width rather than byte
// length. It never splits inside a multi-byte rune or a double-width character:
// a rune that would overflow the current line starts a new line instead. Any
// existing newlines in s are preserved as hard breaks. A non-positive width is
// treated as "no wrapping" and s is returned unchanged.
func WrapToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}

	var out strings.Builder
	lines := strings.Split(s, "\n")
	for li, line := range lines {
		if li > 0 {
			out.WriteByte('\n')
		}
		wrapLine(&out, line, width)
	}
	return out.String()
}

// wrapLine wraps a single newline-free line into out, breaking on rune
// boundaries so no double-width rune is ever cut in half.
func wrapLine(out *strings.Builder, line string, width int) {
	cur := 0 // display width accumulated on the current output line
	first := true
	for _, r := range line {
		rw := ui.Width(string(r))
		if !first && cur+rw > width {
			out.WriteByte('\n')
			cur = 0
		}
		out.WriteRune(r)
		cur += rw
		first = false
	}
}

// TruncateToWidth returns s clipped to at most width display columns, appending
// an ellipsis "…" when it removes content. Width is measured in terminal cells
// (CJK and emoji count as two) via ui.Width, and truncation happens on rune
// boundaries so a double-width character is never sliced. The returned string's
// display width is guaranteed to be <= width. A non-positive width yields the
// empty string.
func TruncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ui.Width(s) <= width {
		return s
	}

	// Reserve room for the ellipsis. If width is too small to even hold the
	// ellipsis plus one column, fall back to fitting bare runes into width.
	budget := width - ui.Width(ellipsis)
	if budget <= 0 {
		return fitRunes(s, width)
	}

	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := ui.Width(string(r))
		if used+rw > budget {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteString(ellipsis)
	return b.String()
}

// fitRunes packs as many leading runes of s as fit within width columns without
// any ellipsis, breaking on rune boundaries.
func fitRunes(s string, width int) string {
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := ui.Width(string(r))
		if used+rw > width {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}
