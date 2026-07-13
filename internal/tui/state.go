// Package tui implements the interactive terminal interface (US-022, #41),
// built on charm.land/bubbletea v2. The design deliberately splits into two
// layers:
//
//   - state.go holds a pure, framework-free state machine (uiState) whose
//     transitions — runID stale-guard, steering queue, two-phase Ctrl+C — are
//     unit-tested directly, without rendering or a live terminal.
//   - model.go wraps that state in a bubbletea Model and bridges the agent
//     loop's EventStream into the Update loop via Program.Send.
//
// Rendering follows bubbletea's contract: View returns a plain string and the
// framework diffs it; pigo does not hand-roll incremental screen updates. This
// file is the state machine.
package tui

import (
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// entryKind classifies a transcript line for rendering.
type entryKind int

const (
	entryUser       entryKind = iota // user input
	entryAssistant                   // assistant text (may stream/grow)
	entryToolCall                    // a tool invocation
	entryToolResult                  // a tool's result
	entrySystem                      // local status (interrupts, errors)
)

// transcriptEntry is one rendered line of the conversation. For a streaming
// assistant message, text grows in place across message_update events.
type transcriptEntry struct {
	Kind entryKind
	Text string
	// streaming marks the assistant entry currently being appended to, so
	// successive message_update events replace rather than append entries.
	streaming bool
}

// uiState is the pure interactive state. It has no dependency on bubbletea or
// on a terminal, so every transition is exercised by table tests. The bubbletea
// Model owns a uiState and forwards user/agent events into these methods.
type uiState struct {
	transcript []transcriptEntry

	// running reports whether an agent run is in flight. Input submitted while
	// running is queued as steering rather than starting a new run.
	running bool
	// runID identifies the in-flight run. Every agent event carries the runID it
	// was produced under; events whose runID != runID are stale (from a run that
	// was interrupted/superseded) and are dropped — the stale-guard.
	runID int
	// steering holds messages typed during a run, injected before the next turn
	// (pi per-turn steering semantics).
	steering []string

	// ctrlCArmed implements the two-phase Ctrl+C: the first press "arms"
	// (requesting interrupt of a run, or a warning when idle); a second press
	// before anything else resets it confirms the quit.
	ctrlCArmed bool

	// pick holds the interactive model-picker state. When pick.active is true the
	// TUI is in picker mode: keyboard/mouse navigation moves pick.cursor and
	// selection switches the live model, rather than editing the composer or
	// scrolling the transcript.
	pick picker

	// menu holds the slash-command autocomplete state. When menu.active is true a
	// popup above the composer lists the slash commands (and skills) matching the
	// "/prefix" currently typed; arrow keys move menu.cursor and Tab/Enter
	// completes the highlighted command into the composer. Unlike the picker it
	// does NOT take over the view — the user keeps typing to filter.
	menu slashMenu
}

// menuItem is one row in the slash-command autocomplete menu: the command name
// (without the leading "/") and its description.
type menuItem struct {
	Name string
	Desc string
}

// slashMenu is the pure state of the slash-command autocomplete popup. Like the
// picker it is framework-free so its transitions are unit-tested directly.
type slashMenu struct {
	active bool
	items  []menuItem
	cursor int
}

// setMenu opens (or refreshes) the autocomplete menu over items, preserving the
// cursor where possible but clamping it into range. An empty item list closes
// the menu (nothing matches the typed prefix).
func (s *uiState) setMenu(items []menuItem) {
	if len(items) == 0 {
		s.menu = slashMenu{}
		return
	}
	cursor := s.menu.cursor
	if cursor > len(items)-1 {
		cursor = len(items) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	s.menu = slashMenu{active: true, items: items, cursor: cursor}
}

// closeMenu dismisses the autocomplete menu.
func (s *uiState) closeMenu() { s.menu = slashMenu{} }

// menuMoveBy moves the menu selection by delta (negative = up), clamped to the
// item range. A no-op when the menu is inactive.
func (s *uiState) menuMoveBy(delta int) {
	if !s.menu.active {
		return
	}
	c := s.menu.cursor + delta
	if c < 0 {
		c = 0
	}
	if c > len(s.menu.items)-1 {
		c = len(s.menu.items) - 1
	}
	s.menu.cursor = c
}

// menuCurrent returns the highlighted menu item and true, or a zero item and
// false when the menu is inactive/empty.
func (s *uiState) menuCurrent() (menuItem, bool) {
	if !s.menu.active || len(s.menu.items) == 0 {
		return menuItem{}, false
	}
	return s.menu.items[s.menu.cursor], true
}

// PickerItem is one selectable row in the model picker: the model id to switch
// to and the human label shown in the list. Exported so the cmd layer can
// supply the preset catalog without the tui package importing provider data.
type PickerItem struct {
	ID    string
	Label string
}

// picker is the pure state of the interactive model picker (对标 pi agent's
// model picker). It is framework-free like the rest of uiState so its
// navigation transitions are unit-tested directly.
type picker struct {
	active bool
	items  []PickerItem
	cursor int
}

// openPicker enters picker mode over items with the cursor on the first row. It
// is a no-op when items is empty (nothing to pick), leaving the picker closed.
func (s *uiState) openPicker(items []PickerItem) {
	if len(items) == 0 {
		return
	}
	s.ctrlCArmed = false
	s.pick = picker{active: true, items: items, cursor: 0}
}

// pickerMoveBy moves the selection cursor by delta (negative = up), clamped to
// the item range so wheel/arrow spam at either end stops at the edge rather
// than wrapping. A no-op when the picker is inactive.
func (s *uiState) pickerMoveBy(delta int) {
	if !s.pick.active {
		return
	}
	c := s.pick.cursor + delta
	if c < 0 {
		c = 0
	}
	if c > len(s.pick.items)-1 {
		c = len(s.pick.items) - 1
	}
	s.pick.cursor = c
}

// pickerCurrent returns the item under the cursor and true, or a zero item and
// false when the picker is inactive/empty.
func (s *uiState) pickerCurrent() (PickerItem, bool) {
	if !s.pick.active || len(s.pick.items) == 0 {
		return PickerItem{}, false
	}
	return s.pick.items[s.pick.cursor], true
}

// closePicker leaves picker mode.
func (s *uiState) closePicker() { s.pick = picker{} }

// pushSystem appends a local system status line to the transcript (used by the
// picker to echo the outcome of a model switch, since a picker action is not an
// agent run).
func (s *uiState) pushSystem(text string) {
	s.transcript = append(s.transcript, transcriptEntry{Kind: entrySystem, Text: text})
}

// newUIState returns an empty interactive state.
func newUIState() *uiState {
	return &uiState{runID: 0}
}

// replay seeds the transcript from a persisted session's messages so a resumed
// session renders its prior conversation before any new input. It maps each
// message role to the matching transcript entries: user text, assistant text,
// assistant tool calls, and tool results — the same shapes applyEvent produces
// for a live run, so a replayed transcript is indistinguishable from one built
// turn-by-turn. It does not touch runID/running (a resumed session starts idle).
func (s *uiState) replay(messages []agentcore.AgentMessage) {
	for _, m := range messages {
		switch msg := m.(type) {
		case agentcore.UserMessage:
			if text := userText(msg); text != "" {
				s.transcript = append(s.transcript, transcriptEntry{Kind: entryUser, Text: text})
			}
		case agentcore.AssistantMessage:
			if text := assistantText(msg); text != "" {
				s.transcript = append(s.transcript, transcriptEntry{Kind: entryAssistant, Text: text})
			}
			for _, c := range msg.ToolCalls() {
				s.transcript = append(s.transcript, transcriptEntry{Kind: entryToolCall, Text: c.Name})
			}
		case agentcore.ToolResultMessage:
			s.transcript = append(s.transcript, transcriptEntry{Kind: entryToolResult, Text: toolResultText(msg)})
		}
	}
}

// submit handles the user pressing Enter. The caller passes the current input
// text (owned by the model's textinput widget). When idle it starts a new run
// and returns (prompt, true): the caller launches the agent loop under the new
// runID. When a run is in flight it queues the text as steering and returns
// ("", false). Empty/whitespace input is ignored. Any keystroke disarms Ctrl+C.
func (s *uiState) submit(input string) (prompt string, start bool) {
	s.ctrlCArmed = false
	text := strings.TrimSpace(input)
	if text == "" {
		return "", false
	}
	if s.running {
		// Steering: queue for injection before the next turn; echo it so the user
		// sees it was accepted.
		s.steering = append(s.steering, text)
		s.transcript = append(s.transcript, transcriptEntry{Kind: entryUser, Text: text})
		return "", false
	}
	// Start a fresh run.
	s.runID++
	s.running = true
	s.transcript = append(s.transcript, transcriptEntry{Kind: entryUser, Text: text})
	return text, true
}

// drainSteering returns and clears the queued steering messages, called by the
// bridge when the loop asks for per-turn steering.
func (s *uiState) drainSteering() []string {
	if len(s.steering) == 0 {
		return nil
	}
	out := s.steering
	s.steering = nil
	return out
}

// pressCtrlC implements two-phase interrupt/quit. It returns the action the
// caller must perform:
//
//   - "interrupt": a run is in flight and this is the first press — the caller
//     cancels the run's context; a second press is not required to stop a run.
//   - "quit": idle and the second consecutive press — the caller exits.
//   - "arm": idle and the first press — nothing happens yet but a hint shows.
//
// Any other keystroke disarms (handled in submit / typing paths).
func (s *uiState) pressCtrlC() string {
	if s.running {
		// First Ctrl+C during a run interrupts it. Do not quit the program.
		s.ctrlCArmed = false
		s.transcript = append(s.transcript, transcriptEntry{Kind: entrySystem, Text: "^C interrupt — stopping run"})
		return "interrupt"
	}
	if s.ctrlCArmed {
		return "quit"
	}
	s.ctrlCArmed = true
	return "arm"
}

// disarmCtrlC clears the armed state (called on any non-Ctrl+C key).
func (s *uiState) disarmCtrlC() { s.ctrlCArmed = false }

// applyEvent folds one agent event into the transcript, guarding on runID: an
// event from a superseded run (runID != s.runID) is dropped. It returns false
// when the event was stale (dropped), true when applied.
func (s *uiState) applyEvent(runID int, ev agentcore.AgentEvent) bool {
	if runID != s.runID {
		return false // stale-guard: event from an interrupted/old run
	}
	switch e := ev.(type) {
	case agentcore.MessageUpdateEvent:
		if a, ok := e.Message.(agentcore.AssistantMessage); ok {
			s.upsertStreamingAssistant(assistantText(a))
		}
	case agentcore.TurnEndEvent:
		// Finalize the streaming assistant text, then surface any tool calls.
		if text := assistantText(e.Message); text != "" {
			s.upsertStreamingAssistant(text)
		}
		s.finalizeStreaming()
		for _, c := range e.Message.ToolCalls() {
			s.transcript = append(s.transcript, transcriptEntry{Kind: entryToolCall, Text: c.Name})
		}
		for _, tr := range e.ToolResults {
			s.transcript = append(s.transcript, transcriptEntry{Kind: entryToolResult, Text: toolResultText(tr)})
		}
	}
	return true
}

// finishRun marks the run complete (called on runDone). A stale runID is
// ignored so a late completion from a superseded run does not clear a newer
// run's running flag.
func (s *uiState) finishRun(runID int, err error) bool {
	if runID != s.runID {
		return false
	}
	s.finalizeStreaming()
	s.running = false
	if err != nil {
		s.transcript = append(s.transcript, transcriptEntry{Kind: entrySystem, Text: "error: " + err.Error()})
	}
	return true
}

// abortStartedRun undoes a run that submit() started but that never launched
// (e.g. an unknown slash-command). It clears running, finalizes any streaming
// entry, and records a local system line explaining why. The runID stays bumped
// so any late events from a prior run remain stale.
func (s *uiState) abortStartedRun(reason string) {
	s.finalizeStreaming()
	s.running = false
	s.transcript = append(s.transcript, transcriptEntry{Kind: entrySystem, Text: reason})
}

// cancelStartedRun undoes a run that submit() started but that is being handled
// locally instead (e.g. opening the model picker). It clears running and
// removes the user entry submit() echoed, leaving no trace in the transcript.
// The runID stays bumped so any late events from a prior run remain stale.
func (s *uiState) cancelStartedRun() {
	s.running = false
	if n := len(s.transcript); n > 0 && s.transcript[n-1].Kind == entryUser {
		s.transcript = s.transcript[:n-1]
	}
}

// upsertStreamingAssistant sets the text of the current streaming assistant
// entry, creating it on the first update of a turn.
func (s *uiState) upsertStreamingAssistant(text string) {
	if text == "" {
		return
	}
	if n := len(s.transcript); n > 0 && s.transcript[n-1].streaming {
		s.transcript[n-1].Text = text
		return
	}
	s.transcript = append(s.transcript, transcriptEntry{Kind: entryAssistant, Text: text, streaming: true})
}

// finalizeStreaming clears the streaming flag on the last assistant entry so a
// subsequent turn starts a new entry rather than overwriting this one.
func (s *uiState) finalizeStreaming() {
	if n := len(s.transcript); n > 0 && s.transcript[n-1].streaming {
		s.transcript[n-1].streaming = false
	}
}

// assistantText extracts the plain text of an assistant message.
func assistantText(a agentcore.AssistantMessage) string {
	var b strings.Builder
	for _, c := range a.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// userText extracts the plain text of a user message (used when replaying a
// persisted session into the transcript).
func userText(u agentcore.UserMessage) string {
	var b strings.Builder
	for _, c := range u.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// toolResultText extracts the plain text of a tool result message.
func toolResultText(tr agentcore.ToolResultMessage) string {
	var b strings.Builder
	for _, c := range tr.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
