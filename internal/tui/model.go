// This file wraps the pure uiState (state.go) in a charm.land/bubbletea v2
// Model and bridges the agent loop into bubbletea's Update loop.
//
// The bridge: when a run starts, a goroutine drains the loop's EventStream and
// forwards each event to the Program via Send, tagged with the run's id. Update
// receives those as agentEventMsg/runDoneMsg and folds them through uiState,
// which drops any tagged with a stale runID. This keeps all mutation on the
// single Update goroutine (bubbletea's contract) while the loop runs
// concurrently.
package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agent"
)

// RunFn starts an agent run for prompt and returns a stream of its events plus
// a cancel func the TUI calls on Ctrl+C interrupt. Injected so the Model is
// testable without a real provider. The steering callback is consulted by the
// loop between turns; it should return (and clear) any queued steering text.
type RunFn func(ctx context.Context, prompt string, steering func() []string) (*agent.LoopEventStream, context.CancelFunc)

// agentEventMsg carries one loop event into Update, tagged with the runID it
// was produced under (for the stale-guard).
type agentEventMsg struct {
	runID int
	event agent.AgentEvent
}

// runDoneMsg signals a run finished, tagged with its runID.
type runDoneMsg struct {
	runID int
	err   error
}

// Model is the bubbletea model for the interactive TUI. It is a single
// monolithic model with a flat state struct (uiState), per the acceptance
// criteria — no nested sub-models, no hand-rolled diffing.
type Model struct {
	state  *uiState
	run    RunFn
	width  int
	height int

	// cancel interrupts the in-flight run's context (set while running).
	cancel context.CancelFunc
	// program is set by SetProgram so the event-drain goroutine can Send events
	// back into Update.
	program *tea.Program
	// quitting is set once the user confirms quit, so View can render a farewell.
	quitting bool
}

// NewModel builds a Model driven by run.
func NewModel(run RunFn) *Model {
	return &Model{state: newUIState(), run: run}
}

// NewModelWithHistory builds a Model whose transcript is pre-seeded from a
// resumed session's messages (US-024). The replayed transcript renders the
// prior conversation before any new input; the model starts idle so the next
// submit continues the session via the injected run func.
func NewModelWithHistory(run RunFn, history []agent.AgentMessage) *Model {
	m := &Model{state: newUIState(), run: run}
	m.state.replay(history)
	return m
}

// SetProgram wires the running Program so the bridge goroutine can Send events.
// Must be called before Run (the cmd/pigo entry point does this).
func (m *Model) SetProgram(p *tea.Program) { m.program = p }

// Init implements tea.Model. No initial command.
func (m *Model) Init() tea.Cmd { return nil }

// Update implements tea.Model: it dispatches keyboard input and bridged agent
// events, mutating uiState. All state mutation happens here on bubbletea's
// single Update goroutine.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		m.state.applyEvent(msg.runID, msg.event)
		return m, nil

	case runDoneMsg:
		m.state.finishRun(msg.runID, msg.err)
		m.cancel = nil
		return m, nil
	}
	return m, nil
}

// handleKey processes a key press.
func (m *Model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		switch m.state.pressCtrlC() {
		case "interrupt":
			if m.cancel != nil {
				m.cancel()
			}
			return m, nil
		case "quit":
			m.quitting = true
			return m, tea.Quit
		case "arm":
			return m, nil
		}
		return m, nil

	case "enter":
		prompt, start := m.state.submit()
		if start {
			return m, m.startRun(prompt)
		}
		return m, nil

	case "backspace":
		m.state.disarmCtrlC()
		if n := len(m.state.input); n > 0 {
			m.state.input = m.state.input[:n-1]
		}
		return m, nil

	default:
		// Printable text: append to the input line.
		if s := k.String(); len(s) == 1 {
			m.state.disarmCtrlC()
			m.state.input += s
		}
		return m, nil
	}
}

// startRun launches the agent run for prompt and returns a command that drains
// its event stream into Update. The drain runs in a goroutine spawned by the
// command; each event is Sent back tagged with the current runID.
func (m *Model) startRun(prompt string) tea.Cmd {
	runID := m.state.runID
	stream, cancel := m.run(context.Background(), prompt, m.state.drainSteering)
	m.cancel = cancel
	return func() tea.Msg {
		go m.drain(runID, stream)
		return nil
	}
}

// drain forwards every event from stream into Update via Program.Send, then
// sends runDoneMsg with the stream result error. Runs in its own goroutine so
// the (unbuffered) stream is fully consumed — no producer leak.
func (m *Model) drain(runID int, stream *agent.LoopEventStream) {
	for ev := range stream.Events() {
		if m.program != nil {
			m.program.Send(agentEventMsg{runID: runID, event: ev})
		}
	}
	_, err := stream.Result(context.Background())
	if m.program != nil {
		m.program.Send(runDoneMsg{runID: runID, err: err})
	}
}

// View implements tea.Model: it renders the transcript then the input line as a
// plain string; bubbletea diffs and paints it (no custom incremental render).
func (m *Model) View() tea.View {
	if m.quitting {
		return tea.NewView("bye\n")
	}
	var b strings.Builder
	for _, e := range m.state.transcript {
		b.WriteString(renderEntry(e))
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString("> ")
	b.WriteString(m.state.input)
	if m.state.running {
		b.WriteString("  … (running; type to steer, Ctrl+C to interrupt)")
	} else if m.state.ctrlCArmed {
		b.WriteString("  (press Ctrl+C again to quit)")
	}
	return tea.NewView(b.String())
}

// renderEntry formats one transcript entry with a role prefix. Assistant text
// is passed through fenceBuffer so an unterminated code fence renders cleanly
// (no half-open ``` breaking the layout mid-stream).
func renderEntry(e transcriptEntry) string {
	switch e.Kind {
	case entryUser:
		return "you> " + e.Text
	case entryAssistant:
		return fenceBuffer(e.Text)
	case entryToolCall:
		return "⚙ " + e.Text
	case entryToolResult:
		return "  ↳ " + firstLine(e.Text)
	case entrySystem:
		return "· " + e.Text
	default:
		return e.Text
	}
}

// fenceBuffer closes a dangling code fence: if the text has an odd number of
// ``` fences (a code block opened by streaming but not yet closed), append a
// closing fence so the rendered output is well-formed. This is the "code block
// fence buffering" the interactive spec calls for.
func fenceBuffer(text string) string {
	if strings.Count(text, "```")%2 == 1 {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "```"
	}
	return text
}

// firstLine returns the first line of s (tool results can be long; the
// transcript shows a one-line gist).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
