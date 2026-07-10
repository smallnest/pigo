package tui

// Tests for role-styled transcript rendering (#95): each entryKind carries a
// distinct role prefix/glyph so conversation sources are distinguishable even
// under NO_COLOR, and with color enabled the theme styles each kind. These
// exercise the render path directly with synthetic entries — no live terminal.

import (
	"strings"
	"testing"
)

// TestRolePrefixesAreDistinct verifies every entry kind maps to a non-empty,
// unique role prefix — the glyph is what conveys source when color is stripped.
func TestRolePrefixesAreDistinct(t *testing.T) {
	kinds := []entryKind{entryUser, entryAssistant, entryToolCall, entryToolResult, entrySystem}
	seen := map[string]entryKind{}
	for _, k := range kinds {
		p := strings.TrimSpace(rolePrefix(k))
		if p == "" {
			t.Errorf("kind %d has an empty role prefix", k)
		}
		if prev, dup := seen[p]; dup {
			t.Errorf("kinds %d and %d share prefix %q", prev, k, p)
		}
		seen[p] = k
	}
}

// TestRenderEntryKeepsPrefixUnderNoColor is acceptance-critical: with color
// disabled, each rendered line must still carry its distinguishing prefix so
// meaning never rides on color alone.
func TestRenderEntryKeepsPrefixUnderNoColor(t *testing.T) {
	withNoColor(t, true)
	m := NewModel(nopRun(new(string)))
	cases := []struct {
		kind   entryKind
		text   string
		prefix string
	}{
		{entryUser, "hi", "you>"},
		{entryToolCall, "read_file", "⚙"},
		{entryToolResult, "ok", "↳"},
		{entrySystem, "interrupted", "·"},
	}
	for _, c := range cases {
		got := m.renderTranscriptEntry(transcriptEntry{Kind: c.kind, Text: c.text}, 40)
		if strings.Contains(got, "\x1b[") {
			t.Errorf("kind %d under NO_COLOR must be plain, got %q", c.kind, got)
		}
		if !strings.Contains(got, c.prefix) {
			t.Errorf("kind %d must keep prefix %q, got %q", c.kind, c.prefix, got)
		}
	}
	// Assistant carries its own accent glyph line even under NO_COLOR.
	a := m.renderTranscriptEntry(transcriptEntry{Kind: entryAssistant, Text: "text"}, 40)
	if !strings.Contains(a, strings.TrimSpace(rolePrefix(entryAssistant))) {
		t.Errorf("assistant must keep its role glyph under NO_COLOR, got %q", a)
	}
}

// TestRenderEntryStylesEachKindWithColor verifies that with color enabled every
// entry kind is colorized (ANSI present) via the theme, so sources are visually
// distinct — not just prefix-distinct.
func TestRenderEntryStylesEachKindWithColor(t *testing.T) {
	withNoColor(t, false)
	m := NewModel(nopRun(new(string)))
	for _, k := range []entryKind{entryUser, entryToolCall, entryToolResult, entrySystem} {
		got := m.renderTranscriptEntry(transcriptEntry{Kind: k, Text: "x"}, 40)
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("kind %d should be color-styled, got %q", k, got)
		}
	}
}

// TestPrefixedLineRespectsWidth verifies a prefixed line still folds within the
// display width (CJK included), so the role prefix does not push wide text past
// the viewport (#91 alignment).
func TestPrefixedLineRespectsWidth(t *testing.T) {
	withNoColor(t, true)
	m := NewModel(nopRun(new(string)))
	const width = 16
	long := "你> 这是一段需要按显示宽度折行的很长中文内容不应超出边界"
	got := m.renderTranscriptEntry(transcriptEntry{Kind: entryUser, Text: long}, width)
	for _, line := range strings.Split(got, "\n") {
		if w := displayWidth(line); w > width {
			t.Errorf("prefixed line %q width %d exceeds %d", line, w, width)
		}
	}
}
