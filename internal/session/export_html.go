// This file holds the HTML rendering helpers for session export (US-008, #124).
// The output is a single self-contained document: all CSS is inlined in a
// <style> block, there are no <script> tags, no external fonts, and no network
// requests, so the transcript renders identically offline and cannot phone home.
// Every piece of session-derived text is escaped via html.EscapeString before it
// reaches the document.
package session

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// htmlHead emits the document head + opening container, embedding the session
// metadata (all escaped). The CSS is deliberately inline and minimal so the file
// is self-contained and depends on no external resource.
func htmlHead(header SessionHeader) string {
	title := header.ID
	if title == "" {
		title = "session"
	}
	meta := []string{}
	if header.Model != "" {
		meta = append(meta, "model: "+html.EscapeString(header.Model))
	}
	if header.Provider != "" {
		meta = append(meta, "provider: "+html.EscapeString(header.Provider))
	}
	if !header.UpdatedAt.IsZero() {
		meta = append(meta, "updated: "+html.EscapeString(header.UpdatedAt.Format(time.RFC3339)))
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>pigo session — %s</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #FAF9F6; color: #1a1a1a; padding: 2rem 1.5rem; line-height: 1.6; }
.container { max-width: 820px; margin: 0 auto; }
h1 { font-size: 1.25rem; font-weight: 700; margin-bottom: 0.25rem; }
.meta { color: #8B8680; font-size: 0.8rem; margin-bottom: 1.5rem; }
.msg { border-radius: 8px; padding: 0.75rem 1rem; margin-bottom: 0.75rem; border: 1px solid #E8E4DE; background: #fff; }
.role { font-size: 0.7rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.4rem; }
.msg.user { background: #F0F4F8; border-color: #4A6FA5; }
.msg.user .role { color: #4A6FA5; }
.msg.assistant { background: #F5F8F6; border-color: #5B8A72; }
.msg.assistant .role { color: #5B8A72; }
.msg.tool { background: #FDF8F0; border-color: #D4A843; }
.msg.tool .role { color: #B8860B; }
.msg.compaction { background: #FFF5F0; border-color: #D97757; }
.msg.compaction .role { color: #D97757; }
.text { white-space: pre-wrap; word-break: break-word; font-size: 0.875rem; }
.toolcall { margin-top: 0.5rem; padding: 0.5rem 0.75rem; background: #F0EEE9; border-radius: 6px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.78rem; white-space: pre-wrap; word-break: break-word; }
.toolcall .tname { font-weight: 600; color: #B8860B; }
.footer { text-align: center; margin-top: 2rem; color: #B0AAA4; font-size: 0.7rem; }
</style>
</head>
<body>
<div class="container">
<h1>pigo session — %s</h1>
<p class="meta">%s</p>
`, html.EscapeString(title), html.EscapeString(title), strings.Join(meta, " · "))
}

// htmlFoot closes the container and document.
func htmlFoot() string {
	return `<p class="footer">Exported by pigo /export</p>
</div>
</body>
</html>
`
}

// renderEntryHTML renders one entry as a role-colored message block. Text and
// tool arguments are escaped so no session content can inject markup.
func renderEntryHTML(e Entry) string {
	switch m := e.Message.(type) {
	case agentcore.UserMessage:
		return msgBlock("user", "User", html.EscapeString(agentcore.ContentToText(m.Content)), "")
	case agentcore.AssistantMessage:
		var tools strings.Builder
		for _, c := range m.ToolCalls() {
			args := strings.TrimSpace(string(c.Arguments))
			tools.WriteString(fmt.Sprintf(`<div class="toolcall"><span class="tname">→ %s</span> %s</div>`,
				html.EscapeString(c.Name), html.EscapeString(args)))
		}
		return msgBlock("assistant", "Assistant", html.EscapeString(agentcore.ContentToText(m.Content)), tools.String())
	case agentcore.ToolResultMessage:
		label := "Tool Result"
		if m.ToolName != "" {
			label = "Tool Result: " + m.ToolName
		}
		return msgBlock("tool", html.EscapeString(label), html.EscapeString(agentcore.ContentToText(m.Content)), "")
	case agentcore.CompactionMessage:
		return msgBlock("compaction", "Compaction", html.EscapeString(m.Summary), "")
	default:
		return msgBlock("assistant", html.EscapeString(e.Message.Role()), "", "")
	}
}

// msgBlock assembles one .msg block with a role class, a role label, the escaped
// body text, and optional pre-rendered tool-call HTML. Callers MUST pass already
// escaped text/label; extra is trusted HTML built here from escaped parts.
func msgBlock(class, label, escapedText, extra string) string {
	var b strings.Builder
	b.WriteString(`<div class="msg `)
	b.WriteString(class)
	b.WriteString(`"><div class="role">`)
	b.WriteString(label)
	b.WriteString(`</div>`)
	if escapedText != "" {
		b.WriteString(`<div class="text">`)
		b.WriteString(escapedText)
		b.WriteString(`</div>`)
	}
	b.WriteString(extra)
	b.WriteString("</div>\n")
	return b.String()
}
