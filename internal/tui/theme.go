// This file defines the TUI's centralized theme layer (#89). All color and
// style decisions live here so the transcript, composer, spinner, and tool
// cards share one consistent, switchable palette rather than scattering ANSI
// codes across the render path.
//
// Two palettes are provided — dark (the default) and light — resolved into a
// tuiTheme of lipgloss styles by buildTheme. The NO_COLOR convention is
// honored: when the environment sets NO_COLOR (to any value), styling is
// stripped to plain text, so meaning must never rely on color alone — the
// render path keeps role prefixes/glyphs for that reason.
package tui

import (
	"os"

	"charm.land/lipgloss/v2"
)

// themeMode selects which palette buildTheme resolves.
type themeMode int

const (
	themeDark  themeMode = iota // dark palette (default)
	themeLight                  // light palette
)

// tuiTheme holds the resolved lipgloss styles for every element the TUI
// renders. A style may be a no-op (plain) style when colors are disabled; the
// render path applies them uniformly either way.
type tuiTheme struct {
	// user styles the user's own prompt lines.
	user lipgloss.Style
	// assistant styles the assistant's reply text.
	assistant lipgloss.Style
	// toolCall styles a tool-invocation line/card header.
	toolCall lipgloss.Style
	// toolResult styles a tool-result line/card body.
	toolResult lipgloss.Style
	// system styles local status lines (interrupts, errors-as-status).
	system lipgloss.Style
	// accent styles emphasized affordances (hints, spinner, running status).
	accent lipgloss.Style
	// errorStyle styles error text.
	errorStyle lipgloss.Style
}

// palette is the raw set of colors a theme mode maps to, before being resolved
// into styles. Keeping the colors separate from the styles lets buildTheme
// share one style-shaping routine across both modes.
type palette struct {
	user       string
	assistant  string
	toolCall   string
	toolResult string
	system     string
	accent     string
	errorColor string
}

// darkPalette is the default palette, tuned for dark terminals.
var darkPalette = palette{
	user:       "12", // bright blue
	assistant:  "15", // near-white
	toolCall:   "13", // bright magenta
	toolResult: "8",  // grey
	system:     "11", // bright yellow
	accent:     "14", // bright cyan
	errorColor: "9",  // bright red
}

// lightPalette is tuned for light terminals (darker inks for contrast).
var lightPalette = palette{
	user:       "4", // blue
	assistant:  "0", // black
	toolCall:   "5", // magenta
	toolResult: "8", // grey
	system:     "3", // yellow/olive
	accent:     "6", // cyan
	errorColor: "1", // red
}

// noColor reports whether the NO_COLOR convention is in effect. Per the
// standard (https://no-color.org), any non-empty value of NO_COLOR disables
// color. This is read at build-time of the theme so tests can toggle it.
func noColor() bool {
	_, set := os.LookupEnv("NO_COLOR")
	return set
}

// buildTheme resolves mode into a tuiTheme. When NO_COLOR is set, every style
// is a plain (uncolored) lipgloss.Style, so output carries no ANSI color codes
// and the TUI must lean on prefixes/glyphs to convey role — which the render
// path does.
func buildTheme(mode themeMode) tuiTheme {
	if noColor() {
		plain := lipgloss.NewStyle()
		return tuiTheme{
			user:       plain,
			assistant:  plain,
			toolCall:   plain,
			toolResult: plain,
			system:     plain,
			accent:     plain,
			errorStyle: plain,
		}
	}

	p := darkPalette
	if mode == themeLight {
		p = lightPalette
	}

	fg := func(c string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
	}
	return tuiTheme{
		user:       fg(p.user).Bold(true),
		assistant:  fg(p.assistant),
		toolCall:   fg(p.toolCall),
		toolResult: fg(p.toolResult),
		system:     fg(p.system),
		accent:     fg(p.accent),
		errorStyle: fg(p.errorColor).Bold(true),
	}
}
