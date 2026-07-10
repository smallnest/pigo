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

	"github.com/smallnest/pigo/internal/agent"
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
	input      string

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
func (s *uiState) replay(messages []agent.AgentMessage) {
	for _, m := range messages {
		switch msg := m.(type) {
		case agent.UserMessage:
			if text := userText(msg); text != "" {
				s.transcript = append(s.transcript, transcriptEntry{Kind: entryUser, Text: text})
			}
		case agent.AssistantMessage:
			if text := assistantText(msg); text != "" {
				s.transcript = append(s.transcript, transcriptEntry{Kind: entryAssistant, Text: text})
			}
			for _, c := range msg.ToolCalls() {
				s.transcript = append(s.transcript, transcriptEntry{Kind: entryToolCall, Text: c.Name})
			}
		case agent.ToolResultMessage:
			s.transcript = append(s.transcript, transcriptEntry{Kind: entryToolResult, Text: toolResultText(msg)})
		}
	}
}

// submit handles the user pressing Enter. When idle it starts a new run and
// returns (prompt, true): the caller launches the agent loop under the new
// runID. When a run is in flight it queues the text as steering and returns
// ("", false). Empty/whitespace input is ignored. Any keystroke disarms Ctrl+C.
func (s *uiState) submit() (prompt string, start bool) {
	s.ctrlCArmed = false
	text := strings.TrimSpace(s.input)
	s.input = ""
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
func (s *uiState) applyEvent(runID int, ev agent.AgentEvent) bool {
	if runID != s.runID {
		return false // stale-guard: event from an interrupted/old run
	}
	switch e := ev.(type) {
	case agent.MessageUpdateEvent:
		if a, ok := e.Message.(agent.AssistantMessage); ok {
			s.upsertStreamingAssistant(assistantText(a))
		}
	case agent.TurnEndEvent:
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
func assistantText(a agent.AssistantMessage) string {
	var b strings.Builder
	for _, c := range a.Content {
		if tc, ok := c.(agent.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// userText extracts the plain text of a user message (used when replaying a
// persisted session into the transcript).
func userText(u agent.UserMessage) string {
	var b strings.Builder
	for _, c := range u.Content {
		if tc, ok := c.(agent.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// toolResultText extracts the plain text of a tool result message.
func toolResultText(tr agent.ToolResultMessage) string {
	var b strings.Builder
	for _, c := range tr.Content {
		if tc, ok := c.(agent.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
