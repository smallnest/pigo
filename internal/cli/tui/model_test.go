package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestModelQuitKeys verifies the root model returns tea.Quit on the standard
// exit keys (Ctrl+C / Ctrl+D), which is how Bubble Tea tears down the program
// and restores the terminal from the alt-screen.
func TestModelQuitKeys(t *testing.T) {
	for _, key := range []string{"ctrl+c", "ctrl+d"} {
		m := NewModel(Options{})
		got, cmd := m.Update(keyPress(key))
		if cmd == nil {
			t.Fatalf("%s: expected a quit command, got nil", key)
		}
		if msg := cmd(); msg != (tea.QuitMsg{}) {
			t.Errorf("%s: cmd produced %T, want tea.QuitMsg", key, msg)
		}
		if !got.(Model).quitting {
			t.Errorf("%s: model should be marked quitting", key)
		}
	}
}

// TestModelViewShell verifies the empty shell renders on the alt-screen and,
// once a size is known, occupies the full terminal height (empty transcript rows
// + status bar + input line), with the real status bar (#386) painting its
// fields.
func TestModelViewShell(t *testing.T) {
	m := NewModel(Options{Model: "test-model"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	view := next.View()
	if !view.AltScreen {
		t.Error("View should request the alt-screen")
	}
	if got := strings.Count(view.Content, "\n"); got != 9 {
		t.Errorf("newline count = %d, want 9 (10 rows)", got)
	}
	if !strings.Contains(view.Content, "test-model") {
		t.Errorf("status bar model field missing from view: %q", view.Content)
	}
}

// keyPress builds a KeyPressMsg matching String()==s for the simple keys used
// in these tests (ctrl+<letter>).
func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{}
	}
}
