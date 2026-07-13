package tui

// Tests for the interactive model picker (对标 pi agent's model picker). The
// pure picker state (open/move/current/close) is exercised on uiState directly,
// and the Model integration (mouse wheel navigation, click/Enter selection,
// /models interception) is driven through Update/handleKey with synthetic
// bubbletea messages — no live terminal.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/runtime"
)

func sampleItems() []PickerItem {
	return []PickerItem{
		{ID: "a/one", Label: "one"},
		{ID: "b/two", Label: "two"},
		{ID: "c/three", Label: "three"},
	}
}

// TestOpenPickerSelectsFirst verifies opening the picker activates it with the
// cursor on the first row, and that an empty list is a no-op.
func TestOpenPickerSelectsFirst(t *testing.T) {
	s := newUIState()
	s.openPicker(sampleItems())
	if !s.pick.active {
		t.Fatal("picker should be active after openPicker")
	}
	if s.pick.cursor != 0 {
		t.Errorf("cursor = %d, want 0", s.pick.cursor)
	}
	s2 := newUIState()
	s2.openPicker(nil)
	if s2.pick.active {
		t.Error("openPicker with no items must not activate")
	}
}

// TestPickerMoveClamps verifies navigation clamps at both ends rather than
// wrapping, so wheel/arrow spam stops at the edge.
func TestPickerMoveClamps(t *testing.T) {
	s := newUIState()
	s.openPicker(sampleItems())
	s.pickerMoveBy(-1) // already at top
	if s.pick.cursor != 0 {
		t.Errorf("cursor after up at top = %d, want 0", s.pick.cursor)
	}
	s.pickerMoveBy(1)
	s.pickerMoveBy(1)
	if s.pick.cursor != 2 {
		t.Errorf("cursor = %d, want 2", s.pick.cursor)
	}
	s.pickerMoveBy(5) // past the end
	if s.pick.cursor != 2 {
		t.Errorf("cursor clamps at %d, want 2", s.pick.cursor)
	}
	cur, ok := s.pickerCurrent()
	if !ok || cur.ID != "c/three" {
		t.Errorf("current = %+v ok=%v, want c/three", cur, ok)
	}
}

// TestClosePickerDeactivates verifies closePicker clears the picker state.
func TestClosePickerDeactivates(t *testing.T) {
	s := newUIState()
	s.openPicker(sampleItems())
	s.closePicker()
	if s.pick.active {
		t.Error("picker should be inactive after close")
	}
	if _, ok := s.pickerCurrent(); ok {
		t.Error("pickerCurrent must be false when inactive")
	}
}

// pickerModel builds a Model wired with a picker over sampleItems, capturing
// the id passed to the select callback.
func pickerModel(picked *string) *Model {
	m := NewModel(nopRun(new(string)))
	m.SetModelPicker(sampleItems, func(id string) string {
		*picked = id
		return "model switched to " + id
	})
	return m
}

// TestOpenModelPickerWiring verifies OpenModelPicker opens over the injected
// catalog, and reports false (does not open) when no picker is wired.
func TestOpenModelPickerWiring(t *testing.T) {
	m := pickerModel(new(string))
	if !m.OpenModelPicker() {
		t.Fatal("OpenModelPicker should open when wired")
	}
	if !m.state.pick.active {
		t.Error("picker not active after OpenModelPicker")
	}
	bare := NewModel(nopRun(new(string)))
	if bare.OpenModelPicker() {
		t.Error("OpenModelPicker must report false when no picker wired")
	}
}

// TestMouseWheelMovesSelection verifies wheel-down/up move the picker cursor
// through Update (the acceptance-critical "鼠标上下移动进行选择" path).
func TestMouseWheelMovesSelection(t *testing.T) {
	m := pickerModel(new(string))
	m.OpenModelPicker()
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.state.pick.cursor != 2 {
		t.Errorf("cursor after two wheel-downs = %d, want 2", m.state.pick.cursor)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.state.pick.cursor != 1 {
		t.Errorf("cursor after wheel-up = %d, want 1", m.state.pick.cursor)
	}
}

// TestPickerEnterSwitchesModel verifies Enter on the highlighted row invokes the
// select callback with that row's id, echoes a status line, and closes the picker.
func TestPickerEnterSwitchesModel(t *testing.T) {
	var picked string
	m := pickerModel(&picked)
	m.OpenModelPicker()
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown}) // cursor -> 1 (b/two)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if picked != "b/two" {
		t.Errorf("selected id = %q, want b/two", picked)
	}
	if m.state.pick.active {
		t.Error("picker should close after Enter")
	}
	last := m.state.transcript[len(m.state.transcript)-1]
	if last.Kind != entrySystem || last.Text != "model switched to b/two" {
		t.Errorf("status echo = %+v, want system 'model switched to b/two'", last)
	}
}

// TestPickerClickSelectsRow verifies a left click maps its Y to the item row,
// selects it, and switches the model.
func TestPickerClickSelectsRow(t *testing.T) {
	var picked string
	m := pickerModel(&picked)
	m.OpenModelPicker()
	// Row 0 is at Y = pickerHeaderRows; the third item (c/three) is +2.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, Y: pickerHeaderRows + 2})
	if picked != "c/three" {
		t.Errorf("clicked id = %q, want c/three", picked)
	}
	if m.state.pick.active {
		t.Error("picker should close after click select")
	}
}

// TestPickerEscCancels verifies Esc closes the picker without switching.
func TestPickerEscCancels(t *testing.T) {
	var picked string
	m := pickerModel(&picked)
	m.OpenModelPicker()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.state.pick.active {
		t.Error("picker should close on Esc")
	}
	if picked != "" {
		t.Errorf("Esc must not switch model, got %q", picked)
	}
}

// TestBareModelsOpensPicker verifies submitting "/models" (no arg) opens the
// picker via the Model's Enter path and leaves no stray transcript entry.
func TestBareModelsOpensPicker(t *testing.T) {
	m := pickerModel(new(string))
	// A slash registry is required so ResolveOutcome would otherwise handle it;
	// the picker interception happens before that.
	m.SetSlashRegistry(runtime.NewSlashRegistry())
	typeRunes(m, "/models")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.state.pick.active {
		t.Fatal("/models should open the picker")
	}
	for _, e := range m.state.transcript {
		if e.Kind == entryUser {
			t.Errorf("/models must not echo a user entry, got %+v", e)
		}
	}
	if m.state.running {
		t.Error("/models must not start a run")
	}
}

// TestIsBareModelsCommand verifies the picker trigger matches only bare
// "/models" (with optional surrounding space), not filtered "/models nvidia".
func TestIsBareModelsCommand(t *testing.T) {
	cases := map[string]bool{
		"/models":        true,
		"  /models  ":    true,
		"/models nvidia": false,
		"/model gpt-4o":  false,
		"hello":          false,
	}
	for in, want := range cases {
		if got := isBareModelsCommand(in); got != want {
			t.Errorf("isBareModelsCommand(%q) = %v, want %v", in, got, want)
		}
	}
}
