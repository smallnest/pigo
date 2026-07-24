// This file renders assistant replies as Markdown for interactive terminals.
// The line-oriented REPL streams model text token-by-token, but Markdown can
// only be laid out once the whole block is known (a table or fenced code span
// needs its full extent). So rendering is a turn-end concern: streamRun buffers
// the streamed text and calls renderMarkdown once the assistant turn closes.
//
// Rendering is gated exactly like color (colorEnabled): only an interactive,
// NO_COLOR-unset stdout gets styled output. Pipes, files, CI, and tests receive
// the raw Markdown source unchanged, so machine consumers and golden tests are
// unaffected. Any renderer failure also falls back to the raw source — pretty
// output is never allowed to lose content.
package main

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

// mdRenderer is the lazily-built glamour renderer. Building it parses a style
// and compiles a chroma lexer set, so it is created once and reused across
// turns. A build failure leaves it nil and markdownEnabled false, degrading to
// raw output.
var (
	mdOnce     sync.Once
	mdRenderer *glamour.TermRenderer
)

// initMarkdown builds the shared renderer on first use. It uses glamour's
// auto style, which follows the terminal's dark/light background.
//
// WithWordWrap(0) disables glamour's hard word-wrap. That matters: with a fixed
// wrap width glamour pads every line with trailing-space background cells out to
// the full column count, so a three-line reply balloons into kilobytes of ANSI
// noise (measured: ~8KB for a short block at width 100 vs. ~0.5KB unwrapped).
// Disabling the wrap lets the terminal soft-wrap long lines itself and keeps the
// rendered output tight — the REPL doesn't track terminal size anyway.
func initMarkdown() {
	mdOnce.Do(func() {
		r, err := glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(0),
		)
		if err != nil {
			return
		}
		mdRenderer = r
	})
}

// renderMarkdown returns src rendered as styled terminal Markdown when output
// is an interactive terminal, and src unchanged otherwise. A nil/broken
// renderer or a render error also returns src, so content is never dropped in
// favor of styling. The returned string carries its own trailing newline from
// glamour; callers should not add another.
func renderMarkdown(src string) string {
	if !colorEnabled() {
		return src
	}
	if strings.TrimSpace(src) == "" {
		return src
	}
	initMarkdown()
	if mdRenderer == nil {
		return src
	}
	out, err := mdRenderer.Render(src)
	if err != nil {
		return src
	}
	return out
}
