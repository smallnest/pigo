package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file renders finalized assistant turns as Markdown inside the TUI
// transcript (fix #3, mirroring the REPL's ui.RenderMarkdown). The REPL renders
// once at turn-end because Markdown can only be laid out when the whole block is
// known; the transcript does the same — only a finalized assistant block is
// passed through here, never the still-streaming one.
//
// Unlike the REPL's shared renderer (WithWordWrap(0), which relies on the raw
// terminal to soft-wrap), the transcript lives inside a fixed-width viewport
// that does NOT soft-wrap, so we must wrap the Markdown to the content width
// ourselves. Renderers are therefore cached per width and rebuilt when the width
// changes (a resize), which is rare enough that the rebuild cost is negligible.

var (
	mdMu    sync.Mutex
	mdCache = map[int]*glamour.TermRenderer{}
)

// rendererFor returns a glamour renderer that word-wraps to width columns,
// building and caching one per distinct width. A build failure caches nothing
// and returns nil so callers fall back to the raw source.
func rendererFor(width int) *glamour.TermRenderer {
	mdMu.Lock()
	defer mdMu.Unlock()
	if r, ok := mdCache[width]; ok {
		return r
	}
	wrap := width
	if wrap < 0 {
		wrap = 0
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(wrap),
	)
	if err != nil {
		return nil
	}
	mdCache[width] = r
	return r
}

// renderMarkdown returns src rendered as styled terminal Markdown wrapped to
// width columns. It is gated exactly like the REPL's renderer: when output is
// not an interactive terminal (pipes, tests) the raw source is returned so
// golden tests and machine consumers are unaffected. A nil/broken renderer or a
// render error also returns the raw source, so content is never dropped. The
// trailing newline glamour appends is trimmed so the block joins cleanly with
// its neighbors in the transcript.
func renderMarkdown(src string, width int) string {
	if !ui.Enabled() {
		return src
	}
	if strings.TrimSpace(src) == "" {
		return src
	}
	r := rendererFor(width)
	if r == nil {
		return src
	}
	out, err := r.Render(src)
	if err != nil {
		return src
	}
	// Glamour word-wraps prose to width, but its document margin and
	// non-wrapping elements (code blocks, tables) can still emit lines wider than
	// the content column. The transcript viewport does not clip horizontally, so
	// an over-wide line would spill into (and visually erase) the persistent
	// scrollbar column on its right. Hard-wrap the rendered output — ANSI- and
	// wide-char-aware — so every line fits within width and the scrollbar stays
	// put. Prose already within width is untouched.
	trimmed := strings.Trim(out, "\n")
	if width > 0 {
		trimmed = ansi.Hardwrap(trimmed, width, false)
	}
	return trimmed
}
