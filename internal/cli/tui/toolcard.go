package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements the rich tool-call card component (US-006, SPEC 3.2,
// FR-6/7/8). A toolCard is a bordered inline block in the transcript that shows
// a single tool invocation: a header with the tool name and a status icon
// (running / success / warn), the decoded call arguments, and the tool's
// response rendered as an indented tree. Cards are created on toolStartMsg,
// completed on toolEndMsg, and toggled between a capped and a full response view
// with Ctrl+O (see model.go). All width math goes through ui.Width /
// WrapToWidth / TruncateToWidth so CJK and emoji (two columns) never split.

// cardState is the lifecycle of a tool card: running while the tool executes,
// then success or warn once it finishes (warn covers a reported tool error).
type cardState int

const (
	cardRunning cardState = iota
	cardSuccess
	cardWarn
)

// respNode is one line of a tool's response, with depth giving the tree indent
// level (each level is rendered as two leading spaces).
type respNode struct {
	text  string
	depth int
}

// toolCard is a single tool invocation rendered as a bordered card. input holds
// the decoded call arguments (nil when the args were not a JSON object);
// response is the parsed result tree, populated on completion. expanded flips
// the response between a capped preview and the full tree.
type toolCard struct {
	id       string
	name     string
	input    map[string]any
	response []respNode
	state    cardState
	expanded bool
}

// collapsedResponseLines is how many response lines a card shows before it is
// expanded; past this the preview is truncated and a Ctrl+O hint is appended.
const collapsedResponseLines = 5

// statusIcon returns the header status glyph for the card's state. Running is a
// spinner-like ellipsis, success a check, warn a bang.
func (c toolCard) statusIcon() string {
	switch c.state {
	case cardSuccess:
		return "✓"
	case cardWarn:
		return "!"
	default:
		return "…"
	}
}

// styledIcon renders the status glyph with the state's theme color: gray while
// running, green on success, yellow/red on warn.
func (c toolCard) styledIcon(theme Theme) string {
	icon := c.statusIcon()
	switch c.state {
	case cardSuccess:
		return theme.Success.Render(icon)
	case cardWarn:
		return theme.Warn.Render(icon)
	default:
		return theme.System.Render(icon)
	}
}

// render draws the card at the given content width: a rounded border wrapping a
// header (status icon + tool name), a "调用输入参数" section listing the input map,
// and a "Response" section with the tree lines. When not expanded the response
// is capped to collapsedResponseLines with a "(Ctrl+O for more)" hint; when
// expanded every line is shown.
func (c toolCard) render(theme Theme, width int) string {
	if width < 4 {
		width = 4
	}
	// The rounded border consumes one column on each side; wrap everything to the
	// inner width so nothing overflows the frame.
	inner := width - 2

	var lines []string

	icon := c.styledIcon(theme)
	nameBudget := inner - ui.Width(icon) - 1
	if nameBudget < 1 {
		nameBudget = 1
	}
	header := c.name
	if arg := c.primaryArg(); arg != "" {
		header = c.name + "(" + arg + ")"
	}
	header = TruncateToWidth(header, nameBudget)
	lines = append(lines, icon+" "+theme.ToolHeader.Render(header))

	if len(c.input) > 0 {
		lines = append(lines, theme.ToolBody.Render("调用输入参数"))
		for _, k := range sortedKeys(c.input) {
			kv := "  " + k + ": " + fmt.Sprintf("%v", c.input[k])
			lines = append(lines, theme.ToolBody.Render(WrapToWidth(kv, inner)))
		}
	}

	if len(c.response) > 0 {
		lines = append(lines, theme.ToolBody.Render("Response"))
		resp := c.response
		truncated := false
		if !c.expanded && len(resp) > collapsedResponseLines {
			resp = resp[:collapsedResponseLines]
			truncated = true
		}
		for _, n := range resp {
			indent := strings.Repeat("  ", n.depth)
			lines = append(lines, theme.ToolBody.Render(WrapToWidth(indent+n.text, inner)))
		}
		if truncated {
			lines = append(lines, theme.System.Render("(Ctrl+O for more)"))
		}
	}

	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorGray)).
		Width(inner)
	return border.Render(strings.Join(lines, "\n"))
}

// primaryArg returns the most salient call argument to inline in the card header
// so the user can see what the tool is operating on at a glance (FR-6), e.g.
// Bash(cd /x && git add -A). It picks the command for bash and the file path for
// the file tools, otherwise the first argument in sorted-key order. Returns ""
// when the call carried no arguments.
func (c toolCard) primaryArg() string {
	if len(c.input) == 0 {
		return ""
	}
	var key string
	switch strings.ToLower(c.name) {
	case "bash":
		key = "command"
	case "read", "write", "edit", "multiedit":
		key = "file_path"
	}
	if key != "" {
		if v, ok := c.input[key]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	keys := sortedKeys(c.input)
	return fmt.Sprintf("%v", c.input[keys[0]])
}

// sortedKeys returns the map keys in a stable (sorted) order so the input
// section renders deterministically instead of in Go's random map order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// parseToolResult splits a tool's textual result into response tree nodes,
// inferring depth from leading whitespace (every two leading spaces is one
// level). Trailing empty lines are trimmed so the card does not render blank
// tail rows.
func parseToolResult(result string) []respNode {
	lines := strings.Split(result, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	nodes := make([]respNode, 0, len(lines))
	for _, ln := range lines {
		leading := len(ln) - len(strings.TrimLeft(ln, " "))
		nodes = append(nodes, respNode{text: ln[leading:], depth: leading / 2})
	}
	return nodes
}
