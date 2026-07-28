// This file holds the compact tool-activity renderers shared by the REPL, the
// /goal autonomous loop, and /btw side threads: a tool call is shown as a green
// "→ tool:" line and a tool result as a green "← result:" (or red "← error:")
// line, with multi-line output collapsed to one line. The todo tool is the one
// exception — its result is printed in full so the live checklist stays visible.
package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// RenderToolResult prints a tool result to out: the todo tool's result is shown
// in full (indented) so the live checklist stays visible; every other result is
// collapsed to a single "← result:"/"← error:" line.
func RenderToolResult(out io.Writer, tr agentcore.ToolResultMessage) {
	text := agentcore.ContentToText(tr.Content)
	color := Enabled()
	if tr.ToolName == "todo" && !tr.IsError {
		fmt.Fprintln(out, "  "+Colorize(color, Green, "← todo:"))
		for _, line := range strings.Split(text, "\n") {
			fmt.Fprintf(out, "    %s\n", line)
		}
		return
	}
	if tr.IsError {
		fmt.Fprintf(out, "  %s %s\n", Colorize(color, Red, "← error:"), OneLine(text))
		return
	}
	fmt.Fprintf(out, "  %s %s\n", Colorize(color, Green, "← result:"), OneLine(text))
}

// ToolCallLabel renders a tool call as "name args" for the compact "→ tool:"
// status. Empty or "{}" arguments collapse to just the name.
func ToolCallLabel(c agentcore.ToolCallContent) string {
	args := strings.TrimSpace(string(c.Arguments))
	if args == "" || args == "{}" {
		return c.Name
	}
	return c.Name + " " + OneLine(args)
}

// OneLine collapses a possibly multi-line string into a single trimmed line,
// truncating very long values, for the compact tool-activity statuses.
func OneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	const max = 120
	if len(s) > max {
		s = s[:max] + " …"
	}
	return s
}
