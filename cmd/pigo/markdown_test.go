package main

import "testing"

// In tests stdout is not a terminal, so colorEnabled() is false and
// renderMarkdown must return the source verbatim — this is the contract that
// keeps piped output, CI logs, and golden tests free of ANSI escapes.
func TestRenderMarkdownRawWhenNotTerminal(t *testing.T) {
	src := "# Heading\n\nSome **bold** text.\n"
	if got := renderMarkdown(src); got != src {
		t.Fatalf("renderMarkdown on non-terminal = %q, want raw source unchanged", got)
	}
}

// An empty (or whitespace-only) reply must pass through untouched so the caller
// never prints a stray rendered blank block.
func TestRenderMarkdownEmptyPassthrough(t *testing.T) {
	for _, src := range []string{"", "   ", "\n\t\n"} {
		if got := renderMarkdown(src); got != src {
			t.Fatalf("renderMarkdown(%q) = %q, want unchanged", src, got)
		}
	}
}
