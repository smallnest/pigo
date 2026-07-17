// This file implements the HTML→Markdown reduction used by the webfetch tool
// (US-012, #128). It wraps the JohannesKaufmann/html-to-markdown/v2 library: the
// base plugin already strips head/script/style/link/meta/iframe/noscript/input,
// and we additionally register the remaining page chrome (nav/footer/header/
// aside/form/svg/template) for removal so only readable content survives. The
// commonmark plugin renders headings, links, lists, code, emphasis, and tables.
package agenttool

import (
	"sync"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
)

// chromeElements are dropped whole (element + subtree) in addition to the base
// plugin's defaults: they carry no readable body content for a text extraction.
var chromeElements = []string{
	"nav", "footer", "header", "aside", "svg", "form", "template",
}

// mdConverter is the shared, configured converter. It is built once — NewConverter
// registers plugins and tag handlers, which is wasteful to repeat per call, and
// the converter is safe for concurrent ConvertString use.
var mdConverter = sync.OnceValue(func() *converter.Converter {
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
		),
	)
	for _, tag := range chromeElements {
		conv.Register.TagType(tag, converter.TagTypeRemove, converter.PriorityStandard)
	}
	return conv
})

// htmlToMarkdown converts body (HTML) to a simplified Markdown string. On a
// conversion error (rare — the parser is lenient) it falls back to the raw bytes
// so the caller always gets usable text.
func htmlToMarkdown(body []byte) string {
	md, err := mdConverter().ConvertString(string(body))
	if err != nil {
		return string(body)
	}
	return md
}
