// This file adds Markdown rendering for assistant transcript text (#94). The
// assistant streams Markdown (headings, lists, fenced code); rendering it as
// styled output — with syntax-highlighted code blocks — rather than raw text
// makes long replies far more readable in the terminal.
//
// Rendering is delegated to glamour (goldmark + a terminal style). glamour does
// its own rune-width-aware word wrapping, so unlike plain transcript lines the
// assistant text is NOT run through wrapWidth/styleEntry afterwards — glamour
// owns both wrapping and coloring for this one entry kind. The wrap width is
// bound to the viewport width so CJK text stays aligned (US-003), and under
// NO_COLOR a plain ("notty") style is used so output degrades to readable,
// uncolored text.
package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/muesli/reflow/wrap"
)

// mdRenderer wraps a glamour.TermRenderer, rebuilding it when the wrap width or
// the color mode changes. glamour renderers are width- and style-bound at
// construction, so we cache one and only re-create it when a dimension it
// depends on shifts (a terminal resize, or NO_COLOR toggling in tests).
type mdRenderer struct {
	mu      sync.Mutex
	tr      *glamour.TermRenderer
	width   int
	noColor bool
}

// newMDRenderer returns an empty renderer; the underlying glamour renderer is
// built lazily on the first render call once a width is known.
func newMDRenderer() *mdRenderer { return &mdRenderer{width: -1} }

// render turns Markdown source into styled, width-wrapped terminal output. The
// wrap width matches the viewport so rendered lines never exceed it and CJK
// stays aligned; noColor selects glamour's plain "notty" style so output has no
// ANSI color (mirroring the theme's NO_COLOR behavior). On any glamour error,
// or a non-positive width, it falls back to the raw source so text is never
// lost. A trailing newline from glamour's block layout is trimmed so the
// transcript's own inter-entry spacing isn't doubled.
func (r *mdRenderer) render(src string, width int, noColor bool) string {
	if width <= 0 {
		return src
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tr == nil || width != r.width || noColor != r.noColor {
		style := "dark"
		if noColor {
			style = "notty"
		}
		tr, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle(style),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return src
		}
		r.tr, r.width, r.noColor = tr, width, noColor
	}
	out, err := r.tr.Render(src)
	if err != nil {
		return src
	}
	out = strings.Trim(out, "\n")
	// glamour word-wraps on spaces, which never breaks a long run of CJK (no
	// spaces to break on), so such a line can still overflow the viewport. Apply
	// a hard character wrap as a safety net: reflow/wrap force-breaks any line
	// exceeding the limit on a rune boundary while preserving ANSI escapes, so
	// CJK prose stays within width (US-003) without splitting a wide glyph.
	return wrap.String(out, width)
}
