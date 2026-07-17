// Tests for the HTML→Markdown reduction (US-012, #128): headings, links, lists,
// code, and dropped chrome/script elements.
package agenttool

import (
	"strings"
	"testing"
)

// TestHTMLToMarkdownBasics checks headings, paragraphs, and links render.
func TestHTMLToMarkdownBasics(t *testing.T) {
	html := `<html><body>
		<h2>Section</h2>
		<p>Some <strong>bold</strong> and a <a href="https://go.dev">link</a>.</p>
	</body></html>`
	md := htmlToMarkdown([]byte(html))
	for _, want := range []string{"## Section", "**bold**", "[link](https://go.dev)"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q in:\n%s", want, md)
		}
	}
}

// TestHTMLToMarkdownDropsChrome checks script/style/nav content is removed.
func TestHTMLToMarkdownDropsChrome(t *testing.T) {
	html := `<html><head><style>.x{}</style></head><body>
		<nav>menu links</nav>
		<script>tracker()</script>
		<p>real content</p>
		<footer>copyright</footer>
	</body></html>`
	md := htmlToMarkdown([]byte(html))
	if !strings.Contains(md, "real content") {
		t.Errorf("body content dropped: %q", md)
	}
	for _, gone := range []string{"tracker()", ".x{}", "menu links", "copyright"} {
		if strings.Contains(md, gone) {
			t.Errorf("chrome/noise %q leaked into: %q", gone, md)
		}
	}
}

// TestHTMLToMarkdownLists checks list items become dashes.
func TestHTMLToMarkdownLists(t *testing.T) {
	html := `<ul><li>one</li><li>two</li></ul>`
	md := htmlToMarkdown([]byte(html))
	if !strings.Contains(md, "- one") || !strings.Contains(md, "- two") {
		t.Errorf("list not rendered: %q", md)
	}
}

// TestHTMLToMarkdownInlineSpacing checks spaces around inline elements survive
// (regression: "A <a>link</a> here" must not become "Alinkhere").
func TestHTMLToMarkdownInlineSpacing(t *testing.T) {
	md := htmlToMarkdown([]byte(`<p>A <a href="https://x.io">link</a> here.</p>`))
	if !strings.Contains(md, "A [link](https://x.io) here.") {
		t.Errorf("inline spacing lost: %q", md)
	}
}

// TestCollapseBlankLines checks 3+ newlines collapse to a single blank line.
func TestCollapseBlankLines(t *testing.T) {
	got := collapseBlankLines("a\n\n\n\nb")
	if got != "a\n\nb" {
		t.Errorf("collapseBlankLines = %q, want %q", got, "a\n\nb")
	}
}
