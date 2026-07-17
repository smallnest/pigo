// This file implements the minimal HTML→Markdown reduction used by the webfetch
// tool (US-012, #128). It is deliberately small: the goal is readable text for a
// model, not a faithful Markdown round-trip. We walk the parsed node tree,
// dropping non-content elements (script/style/nav/etc.), turning headings, links,
// list items, code and paragraphs into light Markdown, and collapsing runs of
// blank lines. No external Markdown library is pulled in.
package agenttool

import (
	"strings"

	"golang.org/x/net/html"
)

// skipElements are dropped whole (element + subtree): they carry no readable body
// content or are chrome/noise for a text extraction.
var skipElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "head": true,
	"nav": true, "footer": true, "header": true, "aside": true,
	"svg": true, "form": true, "iframe": true, "template": true,
}

// htmlToMarkdown parses body as HTML and renders a simplified Markdown string.
// A parse error (malformed HTML) is not fatal — the parser is lenient — so this
// never returns an error; unparseable input yields whatever text was recovered.
func htmlToMarkdown(body []byte) string {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return string(body)
	}
	var b strings.Builder
	renderNode(&b, doc)
	return collapseBlankLines(b.String())
}

// renderNode walks n depth-first, appending Markdown for content nodes to b.
func renderNode(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		// Collapse internal whitespace runs to single spaces, but preserve a
		// single leading/trailing space so inline text around elements (links,
		// bold) does not get glued together ("A <a>x</a> b" must keep its spaces).
		text := collapseInlineSpace(n.Data)
		if text != "" {
			b.WriteString(text)
		}
		return
	case html.ElementNode:
		if skipElements[n.Data] {
			return
		}
	}

	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		b.WriteString("\n\n")
		b.WriteString(strings.Repeat("#", level))
		b.WriteString(" ")
		renderChildren(b, n)
		b.WriteString("\n\n")
		return
	case "p", "div", "section", "article":
		b.WriteString("\n\n")
		renderChildren(b, n)
		b.WriteString("\n\n")
		return
	case "br":
		b.WriteString("\n")
		return
	case "li":
		b.WriteString("\n- ")
		renderChildren(b, n)
		return
	case "a":
		renderLink(b, n)
		return
	case "code":
		b.WriteString("`")
		renderChildren(b, n)
		b.WriteString("`")
		return
	case "pre":
		b.WriteString("\n\n```\n")
		renderChildren(b, n)
		b.WriteString("\n```\n\n")
		return
	case "strong", "b":
		b.WriteString("**")
		renderChildren(b, n)
		b.WriteString("**")
		return
	case "em", "i":
		b.WriteString("*")
		renderChildren(b, n)
		b.WriteString("*")
		return
	case "tr":
		b.WriteString("\n")
		renderChildren(b, n)
		return
	case "td", "th":
		renderChildren(b, n)
		b.WriteString(" | ")
		return
	}

	renderChildren(b, n)
}

// renderChildren renders every child of n in order.
func renderChildren(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(b, c)
	}
}

// renderLink renders an <a> as "[text](href)" when an href is present and
// differs from the text, else just the text.
func renderLink(b *strings.Builder, n *html.Node) {
	href := ""
	for _, attr := range n.Attr {
		if attr.Key == "href" {
			href = strings.TrimSpace(attr.Val)
			break
		}
	}
	var inner strings.Builder
	renderChildren(&inner, n)
	text := strings.TrimSpace(inner.String())
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") {
		b.WriteString(text)
		return
	}
	if text == "" {
		text = href
	}
	b.WriteString("[")
	b.WriteString(text)
	b.WriteString("](")
	b.WriteString(href)
	b.WriteString(")")
}

// collapseInlineSpace collapses internal whitespace runs in a text node to
// single spaces while preserving a single leading or trailing space when the
// original had one — so adjacent inline elements keep the spaces between them.
// An all-whitespace node collapses to a single space (not empty), which is what
// separates two inline siblings; block-level collapsing later trims stray runs.
func collapseInlineSpace(s string) string {
	if strings.TrimSpace(s) == "" {
		if s == "" {
			return ""
		}
		return " "
	}
	leading := s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r'
	last := s[len(s)-1]
	trailing := last == ' ' || last == '\t' || last == '\n' || last == '\r'
	out := strings.Join(strings.Fields(s), " ")
	if leading {
		out = " " + out
	}
	if trailing {
		out = out + " "
	}
	return out
}

// collapseBlankLines trims trailing spaces per line and collapses 3+ consecutive
// newlines down to a blank-line separator, then trims the whole string.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	var out []string
	blank := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
