package tui

// Tests for the slash-command autocomplete menu (输入斜杠后弹出菜单选择命令/技能).
// Pure menu-state transitions run on uiState; the Model integration (typing "/"
// to open, filtering, Tab-completion, Enter/Esc) is driven through handleKey
// with synthetic key presses and a real SlashRegistry — no live terminal.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/runtime"
)

// menuRegistry builds a registry with a few user prompt commands (对标 loaded
// slash commands and skills) for driving the menu.
func menuRegistry() *runtime.SlashRegistry {
	r := runtime.NewSlashRegistry()
	r.AddUser(runtime.SlashCommand{Name: "review", Description: "review code", Expand: func(a string) string { return "review " + a }})
	r.AddUser(runtime.SlashCommand{Name: "refactor", Description: "refactor code", Expand: func(a string) string { return "refactor " + a }})
	r.AddUser(runtime.SlashCommand{Name: "summarize", Description: "summarize", Expand: func(a string) string { return "summarize " + a }})
	return r
}

// menuModel builds a Model wired with the menu registry.
func menuModel() *Model {
	m := NewModel(nopRun(new(string)))
	m.SetSlashRegistry(menuRegistry())
	return m
}

// TestSlashOpensMenu verifies typing "/" opens the autocomplete menu listing
// all commands.
func TestSlashOpensMenu(t *testing.T) {
	m := menuModel()
	typeRunes(m, "/")
	if !m.state.menu.active {
		t.Fatal("typing / should open the slash menu")
	}
	if len(m.state.menu.items) != 3 {
		t.Errorf("menu items = %d, want 3 (all commands)", len(m.state.menu.items))
	}
}

// TestMenuFiltersByPrefix verifies continuing to type filters the menu to the
// commands whose names share the typed prefix.
func TestMenuFiltersByPrefix(t *testing.T) {
	m := menuModel()
	typeRunes(m, "/re")
	if !m.state.menu.active {
		t.Fatal("menu should stay open while naming a command")
	}
	if len(m.state.menu.items) != 2 {
		t.Fatalf("menu items = %d, want 2 (review, refactor)", len(m.state.menu.items))
	}
	for _, it := range m.state.menu.items {
		if it.Name != "review" && it.Name != "refactor" {
			t.Errorf("unexpected match %q", it.Name)
		}
	}
}

// TestMenuClosesOnSpace verifies the menu closes once a space is typed (the
// rest of the line is arguments, no longer a command name).
func TestMenuClosesOnSpace(t *testing.T) {
	m := menuModel()
	typeRunes(m, "/review ")
	if m.state.menu.active {
		t.Error("menu should close after a space (arguments follow)")
	}
}

// TestMenuNoMatchCloses verifies a prefix matching nothing closes the menu.
func TestMenuNoMatchCloses(t *testing.T) {
	m := menuModel()
	typeRunes(m, "/zzz")
	if m.state.menu.active {
		t.Error("menu should close when no command matches the prefix")
	}
}

// TestMenuArrowMovesSelection verifies down/up move the highlighted row.
func TestMenuArrowMovesSelection(t *testing.T) {
	m := menuModel()
	typeRunes(m, "/")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.state.menu.cursor != 1 {
		t.Errorf("cursor after down = %d, want 1", m.state.menu.cursor)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.state.menu.cursor != 0 {
		t.Errorf("cursor after up = %d, want 0", m.state.menu.cursor)
	}
}

// TestMenuTabCompletes verifies Tab completes the highlighted command into the
// composer as "/name " and closes the menu.
func TestMenuTabCompletes(t *testing.T) {
	m := menuModel()
	typeRunes(m, "/ref") // matches refactor only
	if len(m.state.menu.items) != 1 || m.state.menu.items[0].Name != "refactor" {
		t.Fatalf("prefix /ref should match only refactor, got %+v", m.state.menu.items)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.input.Value(); got != "/refactor " {
		t.Errorf("composer after Tab = %q, want %q", got, "/refactor ")
	}
	if m.state.menu.active {
		t.Error("menu should close after Tab completion")
	}
}

// TestMenuEscCloses verifies Esc dismisses the menu without altering the input.
func TestMenuEscCloses(t *testing.T) {
	m := menuModel()
	typeRunes(m, "/re")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.state.menu.active {
		t.Error("menu should close on Esc")
	}
	if got := m.input.Value(); got != "/re" {
		t.Errorf("Esc must not change the composer, got %q", got)
	}
}

// TestMenuEnterCompletesPartial verifies Enter on an open menu with a PARTIAL
// prefix completes the highlighted command into the composer (as "/name ")
// instead of submitting "/prefix" — which would resolve to an unknown command.
// A second Enter (menu now closed) submits it.
func TestMenuEnterCompletesPartial(t *testing.T) {
	var captured string
	m := NewModel(nopRun(&captured))
	m.SetSlashRegistry(menuRegistry())
	typeRunes(m, "/ref") // partial: matches refactor only, not a full name
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.state.menu.active {
		t.Error("menu should close after Enter completion")
	}
	if got := m.input.Value(); got != "/refactor " {
		t.Errorf("composer after Enter = %q, want %q", got, "/refactor ")
	}
	if captured != "" {
		t.Errorf("Enter on a partial prefix must not submit, but ran %q", captured)
	}
}

// TestMenuEnterSubmitsFullName verifies Enter submits when the composer already
// spells a complete command name (the user typed it in full), so a fully-typed
// "/review" runs without needing a second keystroke.
func TestMenuEnterSubmitsFullName(t *testing.T) {
	var captured string
	m := NewModel(nopRun(&captured))
	m.SetSlashRegistry(menuRegistry())
	typeRunes(m, "/review") // exact command name
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.state.menu.active {
		t.Error("menu should close after Enter")
	}
	if captured != "review " {
		t.Errorf("Enter on a full name should submit the expanded command, got %q", captured)
	}
}

// TestSlashMenuPrefix verifies the naming-a-command detector.
func TestSlashMenuPrefix(t *testing.T) {
	cases := []struct {
		in     string
		name   string
		naming bool
	}{
		{"/mod", "mod", true},
		{"/", "", true},
		{"/model x", "", false},
		{"hi", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		name, naming := slashMenuPrefix(c.in)
		if name != c.name || naming != c.naming {
			t.Errorf("slashMenuPrefix(%q) = (%q, %v), want (%q, %v)", c.in, name, naming, c.name, c.naming)
		}
	}
}
