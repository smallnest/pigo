package tui

// Tests for the interactive TUI state machine (US-022, #41). These exercise the
// pure uiState transitions — runID stale-guard, steering queue, two-phase
// Ctrl+C — directly, without a terminal or golden-frame snapshots, per the
// acceptance criteria ("内部 state machine 单测…不做 golden-frame 快照").

import (
	"encoding/json"
	"testing"

	"github.com/smallnest/pigo/internal/agent"
)

// assistantEvent builds a MessageUpdateEvent carrying streamed assistant text.
func assistantUpdate(text string) agent.MessageUpdateEvent {
	return agent.MessageUpdateEvent{
		Message: agent.AssistantMessage{
			RoleField: agent.RoleAssistant,
			Content:   agent.ContentList{agent.NewTextContent(text)},
		},
	}
}

// turnEnd builds a TurnEndEvent with the given final text and tool call names.
func turnEnd(text string, toolNames ...string) agent.TurnEndEvent {
	content := agent.ContentList{}
	if text != "" {
		content = append(content, agent.NewTextContent(text))
	}
	for _, n := range toolNames {
		content = append(content, agent.NewToolCallContent("id-"+n, n, json.RawMessage(`{}`)))
	}
	return agent.TurnEndEvent{
		Message: agent.AssistantMessage{RoleField: agent.RoleAssistant, Content: content},
	}
}

// TestSubmitStartsRunWhenIdle verifies the first submit starts a run, bumps the
// runID, sets running, and echoes the user input.
func TestSubmitStartsRunWhenIdle(t *testing.T) {
	s := newUIState()
	s.input = "hello"
	prompt, start := s.submit()
	if !start || prompt != "hello" {
		t.Fatalf("submit idle: got (%q, %v), want (hello, true)", prompt, start)
	}
	if !s.running {
		t.Error("running should be true after starting a run")
	}
	if s.runID != 1 {
		t.Errorf("runID = %d, want 1", s.runID)
	}
	if len(s.transcript) != 1 || s.transcript[0].Kind != entryUser || s.transcript[0].Text != "hello" {
		t.Errorf("user input not echoed to transcript: %+v", s.transcript)
	}
}

// TestSubmitWhileRunningQueuesSteering is acceptance-critical: input submitted
// during a run is queued as steering, not started as a new run.
func TestSubmitWhileRunningQueuesSteering(t *testing.T) {
	s := newUIState()
	s.input = "first"
	s.submit() // starts run 1
	s.input = "steer me"
	prompt, start := s.submit()
	if start || prompt != "" {
		t.Fatalf("submit while running: got (%q, %v), want (\"\", false)", prompt, start)
	}
	if s.runID != 1 {
		t.Errorf("runID must not change while running: %d", s.runID)
	}
	got := s.drainSteering()
	if len(got) != 1 || got[0] != "steer me" {
		t.Errorf("steering queue = %v, want [steer me]", got)
	}
	// Draining clears the queue.
	if again := s.drainSteering(); again != nil {
		t.Errorf("steering must be empty after drain, got %v", again)
	}
}

// TestSubmitIgnoresEmptyInput verifies whitespace-only input neither starts a
// run nor queues steering.
func TestSubmitIgnoresEmptyInput(t *testing.T) {
	s := newUIState()
	s.input = "   "
	if _, start := s.submit(); start {
		t.Error("whitespace input must not start a run")
	}
	if len(s.transcript) != 0 {
		t.Errorf("whitespace input must not echo to transcript: %+v", s.transcript)
	}
}

// TestStaleRunIDEventsDropped is acceptance-critical: events tagged with a
// runID other than the current one are dropped (the stale-guard). This is the
// interrupt-then-new-run race: run 1's late events must not corrupt run 2.
func TestStaleRunIDEventsDropped(t *testing.T) {
	s := newUIState()
	s.input = "q1"
	s.submit() // run 1
	// Simulate interrupt + new run: bump to run 2 by finishing then resubmitting.
	s.finishRun(1, nil)
	s.input = "q2"
	s.submit() // run 2 (runID == 2)

	// A late event from run 1 must be dropped.
	if applied := s.applyEvent(1, assistantUpdate("stale text")); applied {
		t.Error("event with stale runID=1 must be dropped")
	}
	// An event from the current run 2 is applied.
	if applied := s.applyEvent(2, assistantUpdate("live text")); !applied {
		t.Error("event with current runID=2 must be applied")
	}
	// The live text must be present and the stale text absent.
	var hasLive, hasStale bool
	for _, e := range s.transcript {
		if e.Text == "live text" {
			hasLive = true
		}
		if e.Text == "stale text" {
			hasStale = true
		}
	}
	if !hasLive {
		t.Error("live run-2 text missing from transcript")
	}
	if hasStale {
		t.Error("stale run-1 text leaked into transcript")
	}
}

// TestStreamingAssistantUpsert verifies successive message_update events grow
// one assistant entry in place, and turn_end finalizes it so the next turn
// starts a fresh entry.
func TestStreamingAssistantUpsert(t *testing.T) {
	s := newUIState()
	s.input = "go"
	s.submit()
	s.applyEvent(1, assistantUpdate("Hel"))
	s.applyEvent(1, assistantUpdate("Hello"))
	// One assistant entry, latest text.
	assistantCount := 0
	for _, e := range s.transcript {
		if e.Kind == entryAssistant {
			assistantCount++
			if e.Text != "Hello" {
				t.Errorf("assistant text = %q, want Hello", e.Text)
			}
		}
	}
	if assistantCount != 1 {
		t.Errorf("want 1 streaming assistant entry, got %d", assistantCount)
	}
	// turn_end with a tool call finalizes and appends the tool call.
	s.applyEvent(1, turnEnd("Hello", "read"))
	var toolCalls int
	for _, e := range s.transcript {
		if e.Kind == entryToolCall {
			toolCalls++
		}
	}
	if toolCalls != 1 {
		t.Errorf("want 1 tool-call entry after turn_end, got %d", toolCalls)
	}
}

// TestTwoPhaseCtrlCQuitWhenIdle is acceptance-critical: idle, the first Ctrl+C
// arms (no quit), and a second consecutive press quits.
func TestTwoPhaseCtrlCQuitWhenIdle(t *testing.T) {
	s := newUIState()
	if act := s.pressCtrlC(); act != "arm" {
		t.Fatalf("first idle Ctrl+C = %q, want arm", act)
	}
	if act := s.pressCtrlC(); act != "quit" {
		t.Fatalf("second idle Ctrl+C = %q, want quit", act)
	}
}

// TestCtrlCDisarmedByOtherKey verifies a keystroke between two Ctrl+C presses
// resets the arm, so the second press re-arms rather than quitting.
func TestCtrlCDisarmedByOtherKey(t *testing.T) {
	s := newUIState()
	s.pressCtrlC() // arm
	s.disarmCtrlC()
	if act := s.pressCtrlC(); act != "arm" {
		t.Errorf("Ctrl+C after disarm = %q, want arm (not quit)", act)
	}
}

// TestCtrlCInterruptsRun is acceptance-critical: during a run, Ctrl+C
// interrupts the run rather than quitting, and does not require a second press.
func TestCtrlCInterruptsRun(t *testing.T) {
	s := newUIState()
	s.input = "long task"
	s.submit() // running
	if act := s.pressCtrlC(); act != "interrupt" {
		t.Fatalf("Ctrl+C during run = %q, want interrupt", act)
	}
	// A single interrupt must never be reported as quit.
	if s.ctrlCArmed {
		t.Error("interrupt must not leave Ctrl+C armed (would risk accidental quit)")
	}
}

// TestFinishRunClearsRunning verifies finishRun clears running for the matching
// runID and ignores a stale completion.
func TestFinishRunClearsRunning(t *testing.T) {
	s := newUIState()
	s.input = "x"
	s.submit() // run 1
	if ok := s.finishRun(2, nil); ok {
		t.Error("finishRun with stale runID=2 must be ignored")
	}
	if !s.running {
		t.Error("running must stay true after a stale finish")
	}
	if ok := s.finishRun(1, nil); !ok {
		t.Error("finishRun with current runID=1 must apply")
	}
	if s.running {
		t.Error("running must be false after matching finish")
	}
}

// TestFenceBufferClosesDanglingFence verifies an unterminated code fence is
// closed for clean rendering (code-block fence buffering).
func TestFenceBufferClosesDanglingFence(t *testing.T) {
	open := "here:\n```go\nfmt.Println(1)"
	got := fenceBuffer(open)
	if got == open {
		t.Error("dangling fence must be closed")
	}
	// A balanced fence is left untouched.
	balanced := "```go\nx\n```"
	if fenceBuffer(balanced) != balanced {
		t.Error("balanced fences must be left unchanged")
	}
}

// TestAbortStartedRunClearsRunning verifies abortStartedRun (used when an
// unknown slash-command is submitted) clears running and records a system line,
// so the started-but-never-launched run does not wedge the UI (US-029).
func TestAbortStartedRunClearsRunning(t *testing.T) {
	s := newUIState()
	s.input = "/nope"
	if _, start := s.submit(); !start {
		t.Fatal("submit should start a run before slash resolution")
	}
	if !s.running {
		t.Fatal("running should be true after submit")
	}
	s.abortStartedRun(`unknown command "/nope"`)
	if s.running {
		t.Error("running must be false after abortStartedRun")
	}
	last := s.transcript[len(s.transcript)-1]
	if last.Kind != entrySystem || last.Text != `unknown command "/nope"` {
		t.Errorf("expected system line with reason, got %+v", last)
	}
	// A subsequent submit starts a fresh run (state is not wedged).
	s.input = "hi"
	if _, start := s.submit(); !start {
		t.Error("submit after abort should start a run")
	}
}

// TestReplaySeedsTranscript verifies replay reconstructs the transcript from a
// persisted session's messages (US-024): user text, assistant text, tool
// calls, and tool results appear in order, so a resumed session renders its
// prior conversation. A resumed session starts idle (running=false, runID=0).
func TestReplaySeedsTranscript(t *testing.T) {
	s := newUIState()
	history := []agent.AgentMessage{
		agent.UserMessage{RoleField: agent.RoleUser, Content: agent.ContentList{agent.NewTextContent("read main.go")}},
		agent.AssistantMessage{
			RoleField: agent.RoleAssistant,
			Content:   agent.ContentList{agent.NewTextContent("Reading."), agent.NewToolCallContent("c1", "read", json.RawMessage(`{}`))},
		},
		agent.ToolResultMessage{RoleField: agent.RoleToolResult, ToolCallID: "c1", ToolName: "read", Content: agent.ContentList{agent.NewTextContent("package main")}},
		agent.AssistantMessage{RoleField: agent.RoleAssistant, Content: agent.ContentList{agent.NewTextContent("It is package main.")}},
	}
	s.replay(history)

	if s.running || s.runID != 0 {
		t.Errorf("resumed session must start idle: running=%v runID=%d", s.running, s.runID)
	}
	kinds := []entryKind{entryUser, entryAssistant, entryToolCall, entryToolResult, entryAssistant}
	if len(s.transcript) != len(kinds) {
		t.Fatalf("transcript len = %d, want %d: %+v", len(s.transcript), len(kinds), s.transcript)
	}
	for i, want := range kinds {
		if s.transcript[i].Kind != want {
			t.Errorf("transcript[%d].Kind = %d, want %d", i, s.transcript[i].Kind, want)
		}
	}
	if s.transcript[0].Text != "read main.go" {
		t.Errorf("first entry text = %q", s.transcript[0].Text)
	}
	if s.transcript[2].Text != "read" {
		t.Errorf("tool call entry text = %q, want read", s.transcript[2].Text)
	}
	// After replay, a submit starts a fresh run at runID 1 (continues session).
	s.input = "and now?"
	if _, start := s.submit(); !start || s.runID != 1 {
		t.Errorf("submit after replay should start run 1, got runID=%d", s.runID)
	}
}
