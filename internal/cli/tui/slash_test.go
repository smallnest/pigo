package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/runtime"
)

// typeInto feeds each rune of s to the model as a key press, returning the
// evolved model. It mirrors how a terminal delivers typed characters (Code +
// Text), including the leading "/" of a slash-command.
func typeInto(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// menuNames returns the "/name" of every candidate currently in the popup.
func menuNames(m Model) []string {
	out := make([]string, len(m.menu.filtered))
	for i, c := range m.menu.filtered {
		out[i] = "/" + c.Name
	}
	return out
}

func containsAll(hay []string, needles ...string) bool {
	set := make(map[string]bool, len(hay))
	for _, h := range hay {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}

// TestSlashMenuOpensOnSlash verifies that typing a bare "/" opens the popup with
// the built-in commands present (/model, /help among them).
func TestSlashMenuOpensOnSlash(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/").(Model)
	if !m.menu.active {
		t.Fatalf("menu should be active after typing '/'")
	}
	names := menuNames(m)
	if !containsAll(names, "/model", "/help") {
		t.Errorf("candidate set %v missing /model or /help", names)
	}
}

// TestSlashMenuFiltersByPrefix verifies the popup narrows to the typed prefix:
// "/mo" keeps /model and /models but drops /help.
func TestSlashMenuFiltersByPrefix(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/mo").(Model)
	if !m.menu.active {
		t.Fatalf("menu should be active for '/mo'")
	}
	names := menuNames(m)
	if !containsAll(names, "/model", "/models") {
		t.Errorf("candidate set %v missing /model or /models", names)
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "/mo") {
			t.Errorf("candidate %q does not match prefix /mo (set %v)", n, names)
		}
	}
}

// TestSlashMenuClosesOnSpace verifies name-completion stops once the buffer moves
// on to arguments (a space after the command name closes the popup).
func TestSlashMenuClosesOnSpace(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/model ").(Model)
	if m.menu.active {
		t.Errorf("menu should close once the command name is complete (buffer %q)", m.input.Value())
	}
}

// TestSlashMenuNavigation verifies arrow keys move the highlighted candidate and
// wrap at the ends.
func TestSlashMenuNavigation(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/").(Model)
	n := len(m.menu.filtered)
	if n < 2 {
		t.Fatalf("need at least two candidates to test navigation, got %d", n)
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := next.(Model).menu.selected; got != 1 {
		t.Errorf("after Down, selected = %d, want 1", got)
	}
	// Up from index 0 wraps to the last candidate.
	back, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := back.(Model).menu.selected; got != n-1 {
		t.Errorf("after Up from 0, selected = %d, want %d (wrap)", got, n-1)
	}
}

// TestSlashTabCompletes verifies Tab fills the buffer with the highlighted
// command and closes the popup (ready for arguments).
func TestSlashTabCompletes(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/hel").(Model)
	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	gm := got.(Model)
	if gm.input.Value() != "/help " {
		t.Errorf("after Tab, buffer = %q, want %q", gm.input.Value(), "/help ")
	}
	if gm.menu.active {
		t.Errorf("menu should close after Tab completion")
	}
}

// TestSlashHelpExecutesIntoTranscript verifies executing a built-in action
// command (/help) renders its output into the transcript as a system block —
// listing the available commands — without requiring a live provider.
func TestSlashHelpExecutesIntoTranscript(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/help").(Model)
	got, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	gm := got.(Model)
	// No run is started for an action command.
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatalf("/help should not quit")
			}
		}
	}
	if gm.running {
		t.Errorf("/help is an action command; model should stay idle")
	}
	joined := strings.Join(blockTexts(gm.transcript), "\n")
	if !strings.Contains(joined, "/help") || !strings.Contains(joined, "/model") {
		t.Errorf("/help output should list commands (/help, /model); transcript:\n%s", joined)
	}
	if gm.input.Value() != "" {
		t.Errorf("after executing /help, input = %q, want cleared", gm.input.Value())
	}
}

// TestSlashUnknownCommandReported verifies an unknown "/name" surfaces the
// resolver error into the transcript rather than being sent to the agent.
func TestSlashUnknownCommandReported(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/definitelynotacommand").(Model)
	// The popup filters to nothing, so it is inactive; Enter routes through submit.
	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	gm := got.(Model)
	if gm.running {
		t.Errorf("an unknown command must not start a run")
	}
	joined := strings.Join(blockTexts(gm.transcript), "\n")
	if !strings.Contains(joined, "unknown command") {
		t.Errorf("expected an unknown-command notice in transcript, got:\n%s", joined)
	}
}

// TestSlashMenuRendersAboveInput verifies the popup appears in the View while
// active, so the candidate list is visible above the input line.
func TestSlashMenuRendersAboveInput(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeInto(t, next, "/mo").(Model)
	view := m.View()
	if !strings.Contains(view.Content, "/model") {
		t.Errorf("active popup should render /model in the view content")
	}
}

// TestSlashPromptCommandStartsRun verifies a prompt (Expand) command's expanded
// text is fed to the run seam, not shown as a bare status. A stub startRunFn
// stands in for the live provider.
func TestSlashPromptCommandStartsRun(t *testing.T) {
	m := NewModel(Options{})
	// Inject a user prompt command directly into the registry.
	m.slash.AddUser(runtime.SlashCommand{
		Name:   "greet",
		Expand: func(args string) string { return "hello " + args },
	})
	var ran string
	m.startRunFn = func(prompt string) (chan tea.Msg, tea.Cmd) {
		ran = prompt
		ch := make(chan tea.Msg, 1)
		return ch, func() tea.Msg { return nil }
	}
	got, _ := m.runSlash("/greet world")
	gm := got.(Model)
	if ran != "hello world" {
		t.Errorf("prompt command should start a run with expanded text; got %q", ran)
	}
	if !gm.running {
		t.Errorf("model should be running after a prompt command")
	}
}
