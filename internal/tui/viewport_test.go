package tui

// Tests for viewport scrolling of the transcript (#92): a tall transcript must
// be clipped to the window and scrollable, new output must auto-follow the
// bottom only while the user is at the bottom, and a window resize must resize
// the viewport without overflowing. These drive Update/handleKey with synthetic
// messages — no live terminal.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// sizeModel feeds a WindowSizeMsg so the viewport is initialized to w x h.
func sizeModel(m *Model, w, h int) {
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

// fillTranscript appends n plain assistant lines so the transcript exceeds a
// small viewport height, forcing scrolling.
func fillTranscript(m *Model, n int) {
	for i := 0; i < n; i++ {
		m.state.transcript = append(m.state.transcript, transcriptEntry{Kind: entryAssistant, Text: "line"})
	}
	m.refreshViewport()
}

// TestViewportSizesToWindowMinusComposer verifies the transcript viewport takes
// the window height minus the rows reserved for the composer, so the input line
// is never pushed off-screen.
func TestViewportSizesToWindowMinusComposer(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	sizeModel(m, 40, 20)
	if !m.vpReady {
		t.Fatal("viewport should be ready after WindowSizeMsg")
	}
	if got, want := m.viewport.Height(), 20-composerReservedRows; got != want {
		t.Errorf("viewport height = %d, want %d", got, want)
	}
	if got := m.viewport.Width(); got != 40 {
		t.Errorf("viewport width = %d, want 40", got)
	}
}

// TestViewportResizeUpdatesDimensions verifies a second WindowSizeMsg (terminal
// resize) re-sizes the viewport rather than leaving it stale.
func TestViewportResizeUpdatesDimensions(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	sizeModel(m, 40, 20)
	sizeModel(m, 100, 50)
	if got, want := m.viewport.Height(), 50-composerReservedRows; got != want {
		t.Errorf("resized viewport height = %d, want %d", got, want)
	}
	if got := m.viewport.Width(); got != 100 {
		t.Errorf("resized viewport width = %d, want 100", got)
	}
}

// TestViewportAutoFollowsBottomOnNewContent is acceptance-critical: while the
// user is at the bottom, new transcript content must keep the view pinned to the
// bottom (the latest output stays visible).
func TestViewportAutoFollowsBottomOnNewContent(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	sizeModel(m, 40, 6) // small: 4 visible transcript rows
	fillTranscript(m, 50)
	if !m.follow {
		t.Fatal("should still be following after content grows while at bottom")
	}
	if !m.viewport.AtBottom() {
		t.Error("viewport should be pinned to bottom while following")
	}
}

// TestScrollUpStopsAutoFollow is acceptance-critical: once the user scrolls up
// to read history, streaming/new content must NOT yank them back to the bottom.
func TestScrollUpStopsAutoFollow(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	sizeModel(m, 40, 6)
	fillTranscript(m, 50)

	// Scroll up: follow must clear.
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.follow {
		t.Fatal("scrolling up must stop auto-follow")
	}
	offBefore := m.viewport.YOffset()

	// New content arrives; the user's scroll position must be preserved (not
	// jumped to the bottom).
	fillTranscript(m, 10)
	if m.viewport.AtBottom() {
		t.Error("new content must not force the scrolled-up user back to bottom")
	}
	if m.viewport.YOffset() != offBefore {
		t.Errorf("scroll position moved from %d to %d on new content", offBefore, m.viewport.YOffset())
	}
}

// TestScrollBackToBottomReArmsFollow verifies that scrolling back down to the
// bottom re-arms auto-follow, so subsequent output tracks the tail again.
func TestScrollBackToBottomReArmsFollow(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	sizeModel(m, 40, 6)
	fillTranscript(m, 50)

	m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.follow {
		t.Fatal("scrolling up must stop auto-follow")
	}
	// Page down repeatedly until back at the bottom.
	for i := 0; i < 50 && !m.viewport.AtBottom(); i++ {
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if !m.follow {
		t.Error("returning to the bottom must re-arm auto-follow")
	}
}

// TestViewShowsViewportOnceReady verifies View renders the viewport (clipped)
// once sized, and still ends with the composer prompt.
func TestViewShowsViewportOnceReady(t *testing.T) {
	m := NewModel(nopRun(new(string)))
	sizeModel(m, 40, 6)
	fillTranscript(m, 50)
	out := m.View().Content
	if !strings.Contains(out, "> ") {
		t.Error("View must still render the composer prompt")
	}
	// The viewport clips to 4 transcript rows, so far fewer than 50 "line"s show.
	if strings.Count(out, "line") >= 50 {
		t.Error("viewport should clip a tall transcript, not render all 50 lines")
	}
}
