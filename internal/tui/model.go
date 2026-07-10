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

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/runtime"
)

// composerReservedRows is the number of terminal rows reserved below the
// scrolling transcript viewport for the composer: one blank spacer row plus the
// "> " input row. The viewport gets the remaining height so a tall transcript
// scrolls instead of pushing the input off-screen.
const composerReservedRows = 2

// RunFn starts an agent run for prompt and returns a stream of its events plus
// a cancel func the TUI calls on Ctrl+C interrupt. Injected so the Model is
// testable without a real provider. The steering callback is consulted by the
// loop between turns; it should return (and clear) any queued steering text.
type RunFn func(ctx context.Context, prompt string, steering func() []string) (*runtime.LoopEventStream, context.CancelFunc)

// agentEventMsg carries one loop event into Update, tagged with the runID it
// was produced under (for the stale-guard).
type agentEventMsg struct {
	runID int
	event agentcore.AgentEvent
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

	// input is the bubbles textinput widget backing the composer line. It owns
	// the raw input value and provides rune-level cursor movement and editing —
	// crucial for multi-byte UTF-8 (CJK/emoji), which the old byte-wise handling
	// dropped. uiState stays framework-free; the model bridges the widget to it.
	input textinput.Model

	// viewport scrolls the transcript (#92). The transcript is rendered to a
	// string, folded to width, and set as the viewport's content; the viewport
	// clips it to its height and lets the user page/scroll through history. It is
	// a pure display widget — uiState stays framework-free.
	viewport viewport.Model
	// vpReady is set once the first WindowSizeMsg has sized the viewport. Before
	// that the terminal dimensions are unknown, so View falls back to dumping the
	// whole transcript (pre-#92 behavior).
	vpReady bool
	// follow tracks whether the viewport should auto-scroll to the bottom on new
	// content. It starts true (follow the latest output) and is cleared when the
	// user scrolls up to read history, so streaming updates don't yank them back
	// down; re-armed once they scroll back to the bottom.
	follow bool

	// cancel interrupts the in-flight run's context (set while running).
	cancel context.CancelFunc
	// program is set by SetProgram so the event-drain goroutine can Send events
	// back into Update.
	program *tea.Program
	// quitting is set once the user confirms quit, so View can render a farewell.
	quitting bool

	// slash optionally resolves "/name" input into prompt text before a run is
	// started (US-029). When nil, input is submitted verbatim.
	slash *runtime.SlashRegistry
}

// SetSlashRegistry wires a slash-command registry so typed "/name" input is
// expanded before a run starts. Optional; when unset, input runs verbatim.
func (m *Model) SetSlashRegistry(r *runtime.SlashRegistry) { m.slash = r }

// newComposer builds the textinput widget used for the composer line. It is
// focused so it accepts keystrokes, with an empty prompt (the View draws its
// own "> " prefix). A real cursor is used so the terminal shows it natively.
func newComposer() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Focus()
	return ti
}

// NewModel builds a Model driven by run.
func NewModel(run RunFn) *Model {
	return &Model{state: newUIState(), run: run, input: newComposer(), viewport: viewport.New(), follow: true}
}

// NewModelWithHistory builds a Model whose transcript is pre-seeded from a
// resumed session's messages (US-024). The replayed transcript renders the
// prior conversation before any new input; the model starts idle so the next
// submit continues the session via the injected run func.
func NewModelWithHistory(run RunFn, history []agentcore.AgentMessage) *Model {
	m := &Model{state: newUIState(), run: run, input: newComposer(), viewport: viewport.New(), follow: true}
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
		// Size the transcript viewport to the window minus the composer rows.
		vh := m.height - composerReservedRows
		if vh < 1 {
			vh = 1
		}
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(vh)
		m.vpReady = true
		m.refreshViewport()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		m.state.applyEvent(msg.runID, msg.event)
		m.refreshViewport()
		return m, nil

	case runDoneMsg:
		m.state.finishRun(msg.runID, msg.err)
		m.cancel = nil
		m.refreshViewport()
		return m, nil
	}
	return m, nil
}

// refreshViewport re-renders the transcript into the viewport, folding each
// entry to the viewport width on rune boundaries (#91) so CJK/wide characters
// wrap without being split. If the user is following the tail (follow), the
// viewport is pinned to the bottom so streaming output stays visible; if they
// have scrolled up to read history, their position is preserved. A no-op until
// the first WindowSizeMsg sizes the viewport.
func (m *Model) refreshViewport() {
	if !m.vpReady {
		return
	}
	var b strings.Builder
	for i, e := range m.state.transcript {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(wrapWidth(renderEntry(e, m.viewport.Width()), m.viewport.Width()))
	}
	m.viewport.SetContent(b.String())
	if m.follow {
		m.viewport.GotoBottom()
	}
}

// handleKey processes a key press. Ctrl+C and Enter are handled explicitly
// (two-phase quit/interrupt and submit); every other key is delegated to the
// textinput widget, which handles multi-byte UTF-8 (CJK/emoji) input and
// rune-level cursor movement/deletion. Any such key also disarms the two-phase
// Ctrl+C.
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
		prompt, start := m.state.submit(m.input.Value())
		m.input.Reset()
		if start {
			// Expand a slash-command into its prompt text before running. An
			// unknown "/name" is surfaced as a local system line and no run
			// starts; a non-command prompt passes through unchanged.
			if m.slash != nil {
				expanded, handled, err := m.slash.Resolve(prompt)
				if err != nil {
					m.state.abortStartedRun(err.Error())
					return m, nil
				}
				if handled {
					prompt = expanded
				}
			}
			return m, m.startRun(prompt)
		}
		// A submitted (steering) message appended to the transcript should snap
		// the view back to the bottom so the user sees their echoed input.
		m.follow = true
		m.refreshViewport()
		return m, nil

	case "up", "down", "pgup", "pgdown", "ctrl+u", "ctrl+d":
		// Transcript scrolling (#92). Delegate to the viewport, then recompute
		// follow: if the user scrolled up off the bottom, stop auto-following so
		// streaming output doesn't yank them back; re-arm once they return to the
		// bottom. Scroll keys don't disarm the two-phase Ctrl+C (they aren't edits).
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(k)
		m.follow = m.viewport.AtBottom()
		return m, cmd

	default:
		// Any other key edits the composer. Delegating to textinput gives
		// rune-correct insertion (multi-byte CJK/emoji) and rune-level cursor
		// movement + backspace — fixing the old len(s)==1 byte-wise defect.
		m.state.disarmCtrlC()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
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
func (m *Model) drain(runID int, stream *runtime.LoopEventStream) {
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

// View implements tea.Model: it renders the scrolling transcript viewport then
// the input line as a plain string; bubbletea diffs and paints it (no custom
// incremental render). The transcript is held in a viewport (#92) that clips a
// tall history to the window and lets the user page/scroll through it; each
// entry inside is folded to width on rune boundaries so double-width CJK/emoji
// wrap without being split (#91). Before the first WindowSizeMsg sizes the
// viewport, View falls back to dumping the whole transcript (pre-#92 behavior).
func (m *Model) View() tea.View {
	if m.quitting {
		return tea.NewView("bye\n")
	}
	var b strings.Builder
	if m.vpReady {
		b.WriteString(m.viewport.View())
		b.WriteByte('\n')
	} else {
		for _, e := range m.state.transcript {
			b.WriteString(wrapWidth(renderEntry(e, m.width), m.width))
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n")
	b.WriteString("> ")
	b.WriteString(m.input.View())
	if m.state.running {
		b.WriteString("  … (running; type to steer, Ctrl+C to interrupt)")
	} else if m.state.ctrlCArmed {
		b.WriteString("  (press Ctrl+C again to quit)")
	}
	return tea.NewView(b.String())
}

// renderEntry formats one transcript entry with a role prefix. Assistant text
// is passed through fenceBuffer so an unterminated code fence renders cleanly
// (no half-open ``` breaking the layout mid-stream). width is the terminal
// width used to truncate a long tool-result summary on rune boundaries; a
// non-positive width disables that truncation.
func renderEntry(e transcriptEntry, width int) string {
	switch e.Kind {
	case entryUser:
		return "you> " + e.Text
	case entryAssistant:
		return fenceBuffer(e.Text)
	case entryToolCall:
		return "⚙ " + e.Text
	case entryToolResult:
		const prefix = "  ↳ "
		gist := firstLine(e.Text)
		if width > 0 {
			// Reserve the prefix's display columns so the whole line fits.
			gist = truncateWidth(gist, width-displayWidth(prefix))
		}
		return prefix + gist
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
