// This file holds width-aware rendering helpers (#91). The terminal measures
// text by display columns, not bytes or runes: a CJK ideograph or wide emoji
// occupies two columns, a combining mark zero. Wrapping, truncating, or
// aligning by len()/byte count therefore mis-positions everything once
// non-ASCII text appears. These helpers compute display width via
// go-runewidth (the same backing lipgloss.Width uses) and always cut on rune
// boundaries so a wide character is never split across a boundary.
package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// displayWidth reports the number of terminal columns s occupies. It is the
// width primitive every layout decision here is built on, replacing len().
func displayWidth(s string) int { return runewidth.StringWidth(s) }

// truncateWidth returns the longest prefix of s whose display width is <= max,
// cut on a rune boundary so a wide (double-column) character is never sliced in
// half. A non-positive max yields "". Newlines are treated as ordinary runes;
// callers that want a single line should pass one line (see firstLine).
func truncateWidth(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if displayWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > max {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// wrapWidth folds s into lines no wider than max display columns, breaking on
// rune boundaries (never mid-wide-character). Existing newlines in s are
// preserved as hard breaks. A non-positive max disables wrapping (s is returned
// split only on its existing newlines). This is a deliberately simple
// column-based wrap — it does not break on word boundaries, matching the
// transcript's character-oriented content (code, CJK prose) where hard column
// wrapping is the safe default.
func wrapWidth(s string, max int) string {
	if max <= 0 {
		return s
	}
	var out strings.Builder
	lines := strings.Split(s, "\n")
	for li, line := range lines {
		if li > 0 {
			out.WriteByte('\n')
		}
		w := 0
		for _, r := range line {
			rw := runewidth.RuneWidth(r)
			if w+rw > max && w > 0 {
				out.WriteByte('\n')
				w = 0
			}
			out.WriteRune(r)
			w += rw
		}
	}
	return out.String()
}
