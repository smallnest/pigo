package tui

// Tests for assistant Markdown rendering (#94): assistant transcript text is
// rendered as Markdown (headings, lists, fenced code) rather than raw text, the
// wrap width tracks the viewport, an unterminated code fence still renders
// cleanly (fenceBuffer), and under NO_COLOR the output degrades to plain,
// uncolored — but still readable — text. These drive the render path with
// synthetic entries; no live terminal.

import (
	"strings"
	"testing"
)

// assistantEntry appends one assistant transcript entry with the given text.
func assistantEntry(m *Model, text string) {
	m.state.transcript = append(m.state.transcript, transcriptEntry{Kind: entryAssistant, Text: text})
}

// TestAssistantRendersMarkdownNotRaw is acceptance-critical: assistant text is
// rendered through the Markdown renderer, so a fenced code block is reflowed
// into a styled block rather than surfacing the raw ``` fence lines verbatim.
func TestAssistantRendersMarkdownNotRaw(t *testing.T) {
	withNoColor(t, false)
	m := NewModel(nopRun(new(string)))
	src := "# Title\n\nsome **bold** text\n\n```go\nfmt.Println(1)\n```"
	got := m.renderTranscriptEntry(transcriptEntry{Kind: entryAssistant, Text: src}, 40)
	// glamour renders headings/emphasis with ANSI styling and reflows the code
	// block; the raw markdown markers should not survive verbatim.
	if strings.Contains(got, "# Title") {
		t.Errorf("assistant markdown should render the heading, not emit raw '# Title': %q", got)
	}
	if !strings.Contains(got, "Title") {
		t.Errorf("rendered output should still contain the heading text: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("with color enabled, rendered markdown should carry ANSI styling: %q", got)
	}
}

// TestAssistantMarkdownRespectsWidth verifies the Markdown render wraps to the
// given width: no rendered line exceeds the display width bound (CJK included),
// so it stays aligned with the viewport (US-003).
func TestAssistantMarkdownRespectsWidth(t *testing.T) {
	withNoColor(t, true) // plain text so we measure content, not ANSI
	m := NewModel(nopRun(new(string)))
	const width = 20
	long := "这是一段很长的中文文本用来测试自动折行是否遵守视口宽度限制不会超出边界"
	got := m.renderTranscriptEntry(transcriptEntry{Kind: entryAssistant, Text: long}, width)
	for _, line := range strings.Split(got, "\n") {
		if w := displayWidth(line); w > width {
			t.Errorf("rendered line %q width %d exceeds wrap width %d", line, w, width)
		}
	}
}

// TestAssistantUnterminatedFenceRendersCleanly verifies fenceBuffer semantics
// are preserved: a streaming reply with an open (unterminated) ``` fence still
// renders without leaving a dangling half-open fence that breaks layout.
func TestAssistantUnterminatedFenceRendersCleanly(t *testing.T) {
	withNoColor(t, true)
	m := NewModel(nopRun(new(string)))
	// An opened but not-yet-closed code fence (mid-stream).
	src := "here is code:\n```go\nfmt.Println(1)"
	got := m.renderTranscriptEntry(transcriptEntry{Kind: entryAssistant, Text: src}, 40)
	if strings.TrimSpace(got) == "" {
		t.Fatal("unterminated fence should still render non-empty output")
	}
	// The code content should appear (the buffered close fence lets glamour
	// treat it as a complete block rather than dropping it).
	if !strings.Contains(got, "fmt.Println(1)") {
		t.Errorf("code inside an unterminated fence should still render: %q", got)
	}
}

// TestAssistantNoColorIsPlain verifies that under NO_COLOR the assistant
// Markdown output carries no ANSI color escapes but is still non-empty and
// readable (content preserved).
func TestAssistantNoColorIsPlain(t *testing.T) {
	withNoColor(t, true)
	m := NewModel(nopRun(new(string)))
	got := m.renderTranscriptEntry(transcriptEntry{Kind: entryAssistant, Text: "# Heading\n\nbody text"}, 40)
	if strings.Contains(got, "\x1b[") {
		t.Errorf("NO_COLOR assistant render must be plain (no ANSI), got %q", got)
	}
	if !strings.Contains(got, "Heading") || !strings.Contains(got, "body text") {
		t.Errorf("NO_COLOR render must preserve readable content, got %q", got)
	}
}

// TestNonAssistantEntriesUnaffected verifies non-assistant entries still take
// the plain wrap+style path (not Markdown), so a user line keeps its "you> "
// prefix rather than being reinterpreted as Markdown.
func TestNonAssistantEntriesUnaffected(t *testing.T) {
	withNoColor(t, true)
	m := NewModel(nopRun(new(string)))
	got := m.renderTranscriptEntry(transcriptEntry{Kind: entryUser, Text: "# not a heading"}, 40)
	if !strings.Contains(got, "you> ") {
		t.Errorf("user entry must keep its role prefix, got %q", got)
	}
	if !strings.Contains(got, "# not a heading") {
		t.Errorf("user entry text must survive verbatim (not markdown-rendered), got %q", got)
	}
}
