package ui

import "charm.land/lipgloss/v2"

// Width reports the number of terminal cells the rendered string s occupies. It
// delegates to lipgloss v2's width measurement, which strips ANSI escape
// sequences and counts East Asian wide / fullwidth runes (CJK, emoji) as two
// columns. This is the single width primitive the TUI and REPL layers style
// through (per the tui-agent SPEC), so alignment stays consistent across the
// codebase instead of each caller hand-rolling its own East-Asian-width table.
//
// For multi-line input, Width returns the width of the widest line (lipgloss
// measures the bounding box), matching how the renderer lays text out.
func Width(s string) int {
	return lipgloss.Width(s)
}
