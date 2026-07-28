package ui

import "testing"

// In tests stdout is not a terminal, so Enabled() is false and RenderMarkdown
// must return the source verbatim — this is the contract that keeps piped
// output, CI logs, and golden tests free of ANSI escapes.
func TestRenderMarkdownRawWhenNotTerminal(t *testing.T) {
	src := "# Heading\n\nSome **bold** text.\n"
	if got := RenderMarkdown(src); got != src {
		t.Fatalf("RenderMarkdown on non-terminal = %q, want raw source unchanged", got)
	}
}

// An empty (or whitespace-only) reply must pass through untouched so the caller
// never prints a stray rendered blank block.
func TestRenderMarkdownEmptyPassthrough(t *testing.T) {
	for _, src := range []string{"", "   ", "\n\t\n"} {
		if got := RenderMarkdown(src); got != src {
			t.Fatalf("RenderMarkdown(%q) = %q, want unchanged", src, got)
		}
	}
}
