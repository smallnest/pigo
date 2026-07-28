package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/agentcore"
)

// This file implements the scrolling transcript region of the full-screen TUI
// (US-005, SPEC 5.1 transcript, FR-5/FR-10). The transcript owns a
// viewport.Model and an ordered list of rendered blocks (user / assistant /
// system turns). Streaming assistant text arrives as textDeltaMsg values that
// append to the current assistant block; turnEndMsg finalizes it. Content is
// re-flowed through the viewport with theme.WrapToWidth at the live width so CJK
// and emoji never split mid-rune. Tool cards are a later node (#389); this file
// leaves a clean seam (system lines) without building cards.

// blockRole distinguishes the three transcript block kinds so each renders with
// its own theme style.
type blockRole int

const (
	roleUser blockRole = iota
	roleAssistant
	roleSystem
	roleTool
)

// transcriptBlock is one rendered turn in the transcript. text is the raw
// (unstyled, unwrapped) message body; the role selects the theme style and any
// prefix applied at render time. For roleTool blocks text is unused and card
// points at the live tool card (#389); the pointer lets a later toolEndMsg /
// Ctrl+O mutate the card in place and have it re-render on the next reflow.
type transcriptBlock struct {
	role blockRole
	text string
	card *toolCard
}

// transcript is the scrolling message log. It wraps a viewport.Model and keeps
// the source blocks so it can re-flow on width changes. activeAssistant indexes
// the assistant block currently receiving streaming deltas, or -1 when no turn
// is streaming.
type transcript struct {
	vp    viewport.Model
	theme Theme

	// width is the content width (terminal columns) the blocks wrap to. It is
	// separate from the viewport's own width so reflow measurements stay stable
	// even before the first size message.
	width int

	blocks          []transcriptBlock
	activeAssistant int
}

// newTranscript builds an empty transcript with the given theme. The viewport
// starts zero-sized; the model drives setSize from the first tea.WindowSizeMsg.
func newTranscript(theme Theme) transcript {
	vp := viewport.New()
	return transcript{
		vp:              vp,
		theme:           theme,
		activeAssistant: -1,
	}
}

// setSize resizes the transcript's viewport and re-flows the blocks to the new
// width. A non-positive dimension is clamped to zero so the viewport never sees
// a negative extent.
func (t *transcript) setSize(width, height int) {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	t.width = width
	t.vp.SetWidth(width)
	t.vp.SetHeight(height)
	t.reflow()
}

// addUser appends a user turn and closes any streaming assistant block, then
// re-flows (sticking to the bottom when already there).
func (t *transcript) addUser(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleUser, text: text})
	t.activeAssistant = -1
	t.reflow()
}

// addSystem appends a system / meta notice (used for run lifecycle and other
// inline notes).
func (t *transcript) addSystem(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleSystem, text: text})
	t.reflow()
}

// addToolCard appends a rich tool-call card (#389) as an ordered block so it
// renders inline in the transcript. The card is held by pointer, so a later
// state change (toolEndMsg) or expand toggle (Ctrl+O) followed by reflow
// re-renders it in place.
func (t *transcript) addToolCard(c *toolCard) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleTool, card: c})
	t.reflow()
}

// appendDelta grows the current assistant block by delta, creating the block on
// the first delta of a turn. The re-flow auto-sticks to the bottom when the user
// has not scrolled up.
func (t *transcript) appendDelta(delta string) {
	if t.activeAssistant < 0 {
		t.blocks = append(t.blocks, transcriptBlock{role: roleAssistant})
		t.activeAssistant = len(t.blocks) - 1
	}
	t.blocks[t.activeAssistant].text += delta
	t.reflow()
}

// finalizeTurn closes the streaming assistant block. When the final message
// carries text it becomes the block's authoritative body (covering turns that
// arrive without incremental deltas); otherwise the accumulated deltas stand.
func (t *transcript) finalizeTurn(msg agentcore.AssistantMessage) {
	text := agentcore.ContentToText(msg.Content)
	if t.activeAssistant >= 0 {
		if text != "" {
			t.blocks[t.activeAssistant].text = text
		}
	} else if text != "" {
		t.blocks = append(t.blocks, transcriptBlock{role: roleAssistant, text: text})
	}
	t.activeAssistant = -1
	t.reflow()
}

// update forwards a message (typically a key press or scroll) to the viewport so
// PgUp/PgDn/arrow scrolling works. Auto-stick is decided per content change in
// reflow, so a user scroll-up here naturally pauses it until they return to the
// bottom.
func (t *transcript) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	t.vp, cmd = t.vp.Update(msg)
	return cmd
}

// overflowing reports whether the transcript has more content than fits in the
// viewport, i.e. there is history to scroll. relayout uses this to reserve the
// scrollbar column only when scrolling is possible, and view uses it to decide
// whether to attach the thumb at all.
func (t transcript) overflowing() bool {
	return t.vp.Height() > 0 && t.vp.TotalLineCount() > t.vp.Height()
}

// view renders the current visible slice of the transcript with a persistent
// one-column vertical scrollbar down the right edge (FR-10). The scrollbar is
// always present (relayout reserves its column) so it never flickers in and out:
// a light-gray track (░) runs the full height and a darker thumb (█) marks the
// visible window. When the whole transcript fits, the thumb fills the column.
func (t transcript) view() string {
	return lipgloss.JoinHorizontal(lipgloss.Top, t.vp.View(), t.scrollbar())
}

// scrollbar renders the one-column vertical scrollbar the height of the
// viewport. A proportional thumb (█) marks the visible window and its position
// marks the scroll offset, so scrolling up through history moves the thumb; the
// remaining rows draw a light-gray track (░). When the content fits (no
// overflow) the thumb fills the full height. This is a shaded gutter, not the
// thin │ rule that was removed earlier.
func (t transcript) scrollbar() string {
	h := t.vp.Height()
	if h <= 0 {
		return ""
	}
	total := t.vp.TotalLineCount()
	thumb := h
	pos := 0
	if total > h {
		thumb = h * h / total
		if thumb < 1 {
			thumb = 1
		}
		maxOff := total - h
		off := t.vp.YOffset()
		if off > maxOff {
			off = maxOff
		}
		if maxOff > 0 {
			pos = off * (h - thumb) / maxOff
		}
	}
	var b strings.Builder
	for i := 0; i < h; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i >= pos && i < pos+thumb {
			b.WriteString(t.theme.ScrollThumb.Render("█"))
		} else {
			b.WriteString(t.theme.ScrollTrack.Render("░"))
		}
	}
	return b.String()
}

// reflow re-renders every block to the current width and pushes the joined
// content into the viewport. It captures the bottom-stick state before mutating
// content: if the viewport was at the bottom, new content auto-scrolls
// (GotoBottom); if the user had scrolled up, the offset is preserved so reading
// history is not interrupted.
func (t *transcript) reflow() {
	stick := t.vp.AtBottom()

	var b strings.Builder
	for i, blk := range t.blocks {
		if i > 0 {
			b.WriteByte('\n')
			// Separate a new user turn from the previous turn with a blank line so
			// consecutive requests are visually distinct.
			if blk.role == roleUser {
				b.WriteByte('\n')
			}
		}
		b.WriteString(t.renderBlock(blk, i == t.activeAssistant))
	}

	t.vp.SetContent(b.String())

	if stick {
		t.vp.GotoBottom()
	}
}

// renderBlock wraps a block's text to the content width and applies the role's
// theme style. Wrapping happens on the raw text (measured in display columns via
// WrapToWidth) before styling so ANSI escapes never confuse the width math and
// no double-width rune is split. A finalized assistant block is rendered as
// Markdown (fix #3, mirroring the REPL's turn-end render); the still-streaming
// block (streaming==true) stays plain text because Markdown can only be laid out
// once the whole block is known.
func (t transcript) renderBlock(blk transcriptBlock, streaming bool) string {
	if blk.role == roleTool && blk.card != nil {
		return blk.card.render(t.theme, t.width)
	}
	switch blk.role {
	case roleUser:
		return t.theme.User.Render(WrapToWidth(blk.text, t.width))
	case roleSystem:
		return t.theme.System.Render(WrapToWidth(blk.text, t.width))
	default:
		if streaming {
			return t.theme.Assistant.Render(WrapToWidth(blk.text, t.width))
		}
		return renderMarkdown(blk.text, t.width)
	}
}
