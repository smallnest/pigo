package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrlKey builds a Ctrl+<letter> key press matching String()=="ctrl+<letter>".
func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

// TestParseToolResult verifies depth inference from leading spaces and trailing
// blank-line trimming.
func TestParseToolResult(t *testing.T) {
	nodes := parseToolResult("root\n  child\n    grandchild\n\n")
	if len(nodes) != 3 {
		t.Fatalf("node count = %d, want 3 (trailing blank trimmed)", len(nodes))
	}
	want := []respNode{
		{text: "root", depth: 0},
		{text: "child", depth: 1},
		{text: "grandchild", depth: 2},
	}
	for i, w := range want {
		if nodes[i] != w {
			t.Errorf("node[%d] = %+v, want %+v", i, nodes[i], w)
		}
	}
}

// TestToolCardRender checks the header (name + status icon), the input section,
// and the response tree lines appear in the rendered card, and that the status
// icon reflects the state.
func TestToolCardRender(t *testing.T) {
	theme := DefaultTheme()
	cases := []struct {
		name  string
		state cardState
		icon  string
	}{
		{"running", cardRunning, "…"},
		{"success", cardSuccess, "✓"},
		{"warn", cardWarn, "!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := toolCard{
				id:       "1",
				name:     "read_file",
				input:    map[string]any{"path": "/tmp/x"},
				response: parseToolResult("line one\n  nested"),
				state:    tc.state,
			}
			out := card.render(theme, 60)
			for _, want := range []string{"read_file", tc.icon, "调用输入参数", "path: /tmp/x", "Response", "line one", "nested"} {
				if !strings.Contains(out, want) {
					t.Errorf("render missing %q\n%s", want, out)
				}
			}
		})
	}
}

// TestToolCardExpandTruncation verifies the collapsed card caps the response and
// shows the Ctrl+O hint, while the expanded card reveals every line.
func TestToolCardExpandTruncation(t *testing.T) {
	theme := DefaultTheme()
	var b strings.Builder
	for i := 0; i < collapsedResponseLines+3; i++ {
		b.WriteString("resp-line-")
		b.WriteByte(byte('a' + i))
		b.WriteByte('\n')
	}
	card := toolCard{name: "grep", response: parseToolResult(b.String()), state: cardSuccess}

	collapsed := card.render(theme, 60)
	if !strings.Contains(collapsed, "(Ctrl+O for more)") {
		t.Errorf("collapsed card should show Ctrl+O hint\n%s", collapsed)
	}
	lastLine := "resp-line-" + string(byte('a'+collapsedResponseLines+2))
	if strings.Contains(collapsed, lastLine) {
		t.Errorf("collapsed card should not show %q\n%s", lastLine, collapsed)
	}

	card.expanded = true
	expanded := card.render(theme, 60)
	if strings.Contains(expanded, "(Ctrl+O for more)") {
		t.Errorf("expanded card should not show Ctrl+O hint\n%s", expanded)
	}
	if !strings.Contains(expanded, lastLine) {
		t.Errorf("expanded card should show %q\n%s", lastLine, expanded)
	}
}

// TestModelToolCardFlow drives the model through a tool start/end and asserts the
// card is created, transitions running→success, and that a failed tool yields
// warn.
func TestModelToolCardFlow(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(toolStartMsg{id: "t1", name: "read_file", input: map[string]any{"path": "a.go"}})
	mm := next.(Model)
	card, ok := mm.toolCards["t1"]
	if !ok {
		t.Fatalf("toolStartMsg should create a card")
	}
	if card.state != cardRunning {
		t.Errorf("new card state = %v, want cardRunning", card.state)
	}

	next, _ = mm.Update(toolEndMsg{id: "t1", ok: true, result: "done\n  detail"})
	mm = next.(Model)
	if mm.toolCards["t1"].state != cardSuccess {
		t.Errorf("state after ok end = %v, want cardSuccess", mm.toolCards["t1"].state)
	}
	if len(mm.toolCards["t1"].response) != 2 {
		t.Errorf("response nodes = %d, want 2", len(mm.toolCards["t1"].response))
	}

	// A failed tool flips the same card to warn.
	next, _ = m.Update(toolStartMsg{id: "t2", name: "bash"})
	mm = next.(Model)
	next, _ = mm.Update(toolEndMsg{id: "t2", ok: false, result: "boom"})
	mm = next.(Model)
	if mm.toolCards["t2"].state != cardWarn {
		t.Errorf("state after failed end = %v, want cardWarn", mm.toolCards["t2"].state)
	}
}

// TestModelCtrlOTogglesExpanded verifies Ctrl+O flips the most-recent card's
// expanded flag so more response lines become visible.
func TestModelCtrlOTogglesExpanded(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	mm := next.(Model)

	var b strings.Builder
	for i := 0; i < collapsedResponseLines+3; i++ {
		b.WriteString("row")
		b.WriteByte(byte('0' + i))
		b.WriteByte('\n')
	}
	next, _ = mm.Update(toolStartMsg{id: "t1", name: "grep"})
	mm = next.(Model)
	next, _ = mm.Update(toolEndMsg{id: "t1", ok: true, result: b.String()})
	mm = next.(Model)

	if mm.lastToolCard.expanded {
		t.Fatalf("card should start collapsed")
	}
	next, _ = mm.Update(ctrlKey('o'))
	mm = next.(Model)
	if !mm.lastToolCard.expanded {
		t.Errorf("Ctrl+O should expand the most-recent card")
	}
	// Toggling again collapses it.
	next, _ = mm.Update(ctrlKey('o'))
	mm = next.(Model)
	if mm.lastToolCard.expanded {
		t.Errorf("second Ctrl+O should collapse the card")
	}
}
