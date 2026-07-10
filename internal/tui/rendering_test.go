package tui

// Tests for width-aware rendering (#91): display-width truncation and wrapping
// must cut on rune boundaries so a double-column CJK character or wide emoji is
// never split, and must count display columns (not bytes or runes).

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// TestDisplayWidthCountsColumns verifies width is display columns: ASCII is 1
// each, CJK is 2 each, so "abc中文" = 3 + 4 = 7.
func TestDisplayWidthCountsColumns(t *testing.T) {
	if got := displayWidth("abc中文"); got != 7 {
		t.Errorf("displayWidth(abc中文) = %d, want 7", got)
	}
	if got := displayWidth(""); got != 0 {
		t.Errorf("displayWidth(empty) = %d, want 0", got)
	}
}

// TestTruncateWidthNeverSplitsWideRune is acceptance-critical: truncating
// "abc中文def" to a width that lands mid-CJK must stop before the wide rune,
// never emit a half character, and never exceed the budget.
func TestTruncateWidthNeverSplitsWideRune(t *testing.T) {
	// "abc" = 3 cols, then "中" would take cols 4-5. Budget 4 cannot fit "中".
	got := truncateWidth("abc中文def", 4)
	if got != "abc" {
		t.Errorf("truncateWidth(...,4) = %q, want abc (must not split 中)", got)
	}
	if w := displayWidth(got); w > 4 {
		t.Errorf("truncated width = %d, exceeds budget 4", w)
	}
	// Budget 5 fits exactly "abc中" (3+2).
	if got := truncateWidth("abc中文def", 5); got != "abc中" {
		t.Errorf("truncateWidth(...,5) = %q, want abc中", got)
	}
	// Budget >= full width returns the whole string.
	full := "abc中文def"
	if got := truncateWidth(full, 100); got != full {
		t.Errorf("truncateWidth(full,100) = %q, want %q", got, full)
	}
	// Non-positive budget yields empty.
	if got := truncateWidth(full, 0); got != "" {
		t.Errorf("truncateWidth(full,0) = %q, want empty", got)
	}
}

// TestWrapWidthFoldsOnRuneBoundary is acceptance-critical: a long mixed line
// folded to a narrow width must break on rune boundaries — every wrapped line's
// display width stays within the budget and no wide rune is split.
func TestWrapWidthFoldsOnRuneBoundary(t *testing.T) {
	// "abc中文def" is 9 cols. Wrap to 4: expect "abc" | "中文"(4) | "def".
	got := wrapWidth("abc中文def", 4)
	lines := strings.Split(got, "\n")
	for _, ln := range lines {
		if w := displayWidth(ln); w > 4 {
			t.Errorf("wrapped line %q width %d exceeds 4", ln, w)
		}
		// No line may end or begin with a broken rune — Split on \n over a
		// valid UTF-8 string can't produce invalid runes, but assert the join
		// round-trips to the original glyph sequence.
	}
	if joined := strings.ReplaceAll(got, "\n", ""); joined != "abc中文def" {
		t.Errorf("wrap lost/reordered content: %q -> %q", got, joined)
	}
}

// TestWrapWidthPreservesExistingNewlines verifies hard breaks in the input are
// kept (each pre-existing line is wrapped independently).
func TestWrapWidthPreservesExistingNewlines(t *testing.T) {
	got := wrapWidth("ab\ncd", 10)
	if got != "ab\ncd" {
		t.Errorf("wrapWidth kept-newlines = %q, want ab\\ncd", got)
	}
}

// TestWrapWidthNonPositiveDisablesWrap verifies width<=0 only splits on
// existing newlines (no column folding), so a zero terminal width degrades
// gracefully rather than looping.
func TestWrapWidthNonPositiveDisablesWrap(t *testing.T) {
	s := "abc中文def"
	if got := wrapWidth(s, 0); got != s {
		t.Errorf("wrapWidth(s,0) = %q, want unchanged %q", got, s)
	}
}

// TestRenderEntryTruncatesToolResultByWidth verifies a long tool-result gist is
// truncated to the terminal width (accounting for the "  ↳ " prefix) on a rune
// boundary, so a CJK-heavy result summary does not overflow or split.
func TestRenderEntryTruncatesToolResultByWidth(t *testing.T) {
	e := transcriptEntry{Kind: entryToolResult, Text: strings.Repeat("中", 40)}
	out := renderEntry(e, 20)
	if w := displayWidth(out); w > 20 {
		t.Errorf("rendered tool-result width = %d, exceeds terminal width 20: %q", w, out)
	}
	if !strings.HasPrefix(out, "  ↳ ") {
		t.Errorf("tool-result must keep its prefix, got %q", out)
	}
	// The gist portion must contain only whole 中 runes (no partial bytes): its
	// width must be even (each 中 is 2 cols).
	gist := strings.TrimPrefix(out, "  ↳ ")
	if runewidth.StringWidth(gist)%2 != 0 {
		t.Errorf("gist %q split a wide rune (odd width)", gist)
	}
}

// TestRenderEntryZeroWidthNoTruncate verifies width<=0 leaves the tool-result
// gist untruncated (first-line only), preserving pre-#91 behavior when the
// terminal size is unknown.
func TestRenderEntryZeroWidthNoTruncate(t *testing.T) {
	e := transcriptEntry{Kind: entryToolResult, Text: "one line result"}
	if out := renderEntry(e, 0); out != "  ↳ one line result" {
		t.Errorf("zero-width render = %q, want '  ↳ one line result'", out)
	}
}
