package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/ui"
)

// ansiRE strips SGR escape sequences so tests can inspect the raw text the
// transcript stored, independent of the theme's coloring.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// apply runs one Update tick and returns the concrete Model, failing on an
// unexpected model type. It keeps the streaming tests terse.
func apply(t *testing.T, m tea.Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return got
}

// TestTranscriptStreamingConcat feeds a run of text deltas then a turn end and
// asserts the assistant block accumulates the deltas in order and the joined
// text is rendered in the View.
func TestTranscriptStreamingConcat(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, textDeltaMsg{delta: "Hello "})
	m = apply(t, m, textDeltaMsg{delta: "world"})
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("Hello world")},
	}})

	if n := len(m.transcript.blocks); n != 1 {
		t.Fatalf("block count = %d, want 1 assistant block", n)
	}
	if got := m.transcript.blocks[0]; got.role != roleAssistant || got.text != "Hello world" {
		t.Errorf("assistant block = %+v, want role assistant text %q", got, "Hello world")
	}
	if content := stripANSI(m.View().Content); !strings.Contains(content, "Hello world") {
		t.Errorf("rendered View missing streamed text; got:\n%s", content)
	}
	// The turn was finalized, so a fresh delta starts a NEW assistant block.
	if m.transcript.activeAssistant != -1 {
		t.Errorf("activeAssistant = %d after turn end, want -1", m.transcript.activeAssistant)
	}
}

// TestTranscriptAutoStick verifies the stick-to-bottom rule: while the viewport
// is at the bottom, new content keeps it pinned there; once the user scrolls up,
// new content no longer forces a jump to the bottom.
func TestTranscriptAutoStick(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(20, 3) // 3 visible rows

	for i := 0; i < 6; i++ {
		tr.addUser("line")
	}
	if !tr.vp.AtBottom() {
		t.Fatal("transcript should stick to the bottom while at the bottom")
	}

	// More content while pinned keeps it pinned.
	tr.addUser("more")
	if !tr.vp.AtBottom() {
		t.Fatal("new content should keep a bottom-pinned transcript at the bottom")
	}

	// Simulate a user scroll-up through the viewport's key handling.
	tr.update(tea.KeyPressMsg{Code: tea.KeyUp})
	if tr.vp.AtBottom() {
		t.Fatal("scrolling up should move the viewport off the bottom")
	}

	// Content arriving while scrolled up must NOT yank the view back to bottom.
	tr.addUser("streamed while reading history")
	if tr.vp.AtBottom() {
		t.Error("auto-stick should stay paused after the user scrolls up")
	}
}

// TestTranscriptCJKWrap feeds a long CJK line into a narrow transcript and
// asserts every wrapped line fits the width in display columns (not bytes) and
// that no rune was dropped or split.
func TestTranscriptCJKWrap(t *testing.T) {
	const width = 10
	tr := newTranscript(DefaultTheme())
	tr.setSize(width, 20)

	line := strings.Repeat("你好世界", 5) // 20 CJK runes = 40 display columns
	tr.addUser(line)

	content := tr.vp.GetContent()
	lines := strings.Split(content, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected the CJK line to wrap onto multiple rows, got %d line(s)", len(lines))
	}
	for i, ln := range lines {
		if w := ui.Width(ln); w > width {
			t.Errorf("wrapped line %d width = %d columns, want <= %d: %q", i, w, width, stripANSI(ln))
		}
	}
	// No rune was cut or dropped: every source rune survives the wrap.
	if got := strings.Count(stripANSI(content), "你"); got != 5 {
		t.Errorf("counted %d 你 runes after wrap, want 5", got)
	}
}

// TestTranscriptScrollbar verifies the scrollbar policy: the gutter is absent
// while the content fits (no overflow → full width, no thumb), and once the
// content overflows a light-gray thumb "█" appears with no track line "│".
func TestTranscriptScrollbar(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(20, 4) // 4 visible rows

	// Two short lines fit in 4 rows: no overflow, no scrollbar drawn.
	tr.addUser("one")
	tr.addUser("two")
	if tr.overflowing() {
		t.Fatal("transcript should not overflow while content fits")
	}
	if got := stripANSI(tr.view()); strings.Contains(got, "█") || strings.Contains(got, "│") {
		t.Errorf("no scrollbar expected while content fits; got:\n%q", got)
	}

	// Enough lines to exceed 4 rows: now it overflows and the thumb appears.
	for i := 0; i < 10; i++ {
		tr.addUser("line")
	}
	if !tr.overflowing() {
		t.Fatal("transcript should overflow once content exceeds the viewport")
	}
	view := stripANSI(tr.view())
	if !strings.Contains(view, "█") {
		t.Errorf("expected a scrollbar thumb █ while overflowing; got:\n%q", view)
	}
	if strings.Contains(view, "│") {
		t.Errorf("scrollbar must not draw a track line │; got:\n%q", view)
	}
}
