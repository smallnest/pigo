// Tests for the HTML→Markdown reduction (US-012, #128): headings, links, lists,
// code, and dropped chrome/script elements. The conversion is delegated to the
// html-to-markdown/v2 library; these tests pin the behavior webfetch relies on.
package agenttool

import (
	"strings"
	"testing"
)

// TestHTMLToMarkdownBasics checks headings, emphasis, and links render.
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

// TestHTMLToMarkdownDropsChrome checks script/style/nav/footer content is removed
// while real body content survives.
func TestHTMLToMarkdownDropsChrome(t *testing.T) {
	html := `<html><head><style>.x{}</style></head><body>
		<nav>menu links</nav>
		<script>tracker()</script>
		<p>real content</p>
		<footer>copyright notice</footer>
	</body></html>`
	md := htmlToMarkdown([]byte(html))
	if !strings.Contains(md, "real content") {
		t.Errorf("body content dropped: %q", md)
	}
	for _, gone := range []string{"tracker()", ".x{}", "menu links", "copyright notice"} {
		if strings.Contains(md, gone) {
			t.Errorf("chrome/noise %q leaked into: %q", gone, md)
		}
	}
}

// TestHTMLToMarkdownLists checks list items become dashes.
func TestHTMLToMarkdownLists(t *testing.T) {
	html := `<ul><li>one</li><li>two</li></ul>`
	md := htmlToMarkdown([]byte(html))
	if !strings.Contains(md, "one") || !strings.Contains(md, "two") {
		t.Errorf("list not rendered: %q", md)
	}
	if !strings.Contains(md, "- ") {
		t.Errorf("list markers missing: %q", md)
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

// TestHTMLToMarkdownEmptyFallback checks empty input does not panic.
func TestHTMLToMarkdownEmptyFallback(t *testing.T) {
	if got := htmlToMarkdown([]byte("")); strings.TrimSpace(got) != "" {
		t.Errorf("empty input = %q, want empty", got)
	}
}
