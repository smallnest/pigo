package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Model is the root Bubble Tea model for the full-screen TUI. It composes a
// scrolling transcript (US-005) with the persistent status bar (#386) and a
// minimal input line, and owns the run lifecycle: on prompt submit it starts an
// agent run through the event bridge (bridge.go) and pumps the resulting tea.Msg
// stream into the transcript one message at a time. Downstream nodes grow the
// input into a full textarea (#390), render tool cards (#389), and wire the real
// session/run assembly (#392); the Init/Update/View contract and the alt-screen
// + quit-key handling stay stable.
type Model struct {
	opts  Options
	theme Theme

	// width and height track the terminal size reported by tea.WindowSizeMsg.
	// They are zero until the first size message arrives; View degrades to a
	// minimal render in that window.
	width  int
	height int

	// transcript is the scrolling message log (user / assistant / system turns).
	transcript transcript

	// input is the minimal prompt buffer. The full textarea (with CJK-aware
	// editing) lands in #390; this holds just enough to submit a line.
	input string

	// running is true while an agent run is draining through runCh. Input submit
	// is gated on it so a new run cannot start mid-run.
	running bool
	// runCh is the bridge channel for the in-flight run, or nil when idle. Update
	// re-issues waitForEvent(runCh) after every bridged msg except runEndMsg.
	runCh chan tea.Msg

	// startRunFn launches an agent run for the submitted prompt, returning the
	// bridge channel and the first waitForEvent Cmd (see bridge.startRun). It is a
	// seam: the real binding — constructing an AgentContext + RunConfig from opts
	// and the live session — lands with session wiring (#392). Until then it may
	// be nil, in which case a submit records the prompt but starts no run.
	startRunFn func(prompt string) (chan tea.Msg, tea.Cmd)

	// quitting is set when a quit key (Ctrl+C / Ctrl+D) is seen, so View can be a
	// no-op on the final frame while the program tears down and restores the
	// terminal.
	quitting bool

	// statusBar renders the persistent bottom line (#386, US-003). It is fed the
	// terminal width, telemetry-derived context usage, and the async git probe
	// result; View renders it just above the input line.
	statusBar statusBar

	// cwd is the launch directory, captured once at construction and reused for
	// the git probe and the status bar's path display.
	cwd string
}

// NewModel builds the root model from the assembled Options. It performs no I/O
// beyond reading the current working directory (for the status bar's path
// display and git probe); session/live/slash/trust assembly is deferred to
// downstream nodes.
func NewModel(opts Options) Model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	theme := DefaultTheme()
	return Model{
		opts:       opts,
		theme:      theme,
		transcript: newTranscript(theme),
		cwd:        cwd,
		statusBar:  newStatusBar(theme, opts, cwd),
	}
}

// Init implements tea.Model. It kicks off the async git probe so the status bar
// can show the branch/dirty state as soon as it resolves; the alt-screen is
// requested declaratively via the AltScreen field on the View returned by View.
func (m Model) Init() tea.Cmd {
	return fetchGitCmd(m.cwd)
}

// Update implements tea.Model. It tracks the terminal size, drives the minimal
// input line, starts runs on submit, and pumps bridged run events into the
// transcript and status bar. It quits on the standard exit keys (Ctrl+C /
// Ctrl+D).
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.transcript.setSize(msg.Width, transcriptHeight(msg.Height))
		return m, nil

	case gitInfoMsg:
		m.statusBar.SetGit(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case textDeltaMsg:
		m.transcript.appendDelta(msg.delta)
		return m, m.pumpNext()

	case turnEndMsg:
		m.transcript.finalizeTurn(msg.msg)
		return m, m.pumpNext()

	case toolStartMsg:
		// Tool cards land in #389; for now a system line records the activity.
		m.transcript.addSystem("· " + msg.name)
		return m, m.pumpNext()

	case toolUpdateMsg:
		return m, m.pumpNext()

	case toolEndMsg:
		return m, m.pumpNext()

	case telemetryMsg:
		// Feed the status bar's context-usage readout, then keep the pump running.
		m.statusBar.SetTelemetry(telemetryEventView{
			util:   msg.ev.ContextUtilization,
			window: msg.ev.ContextWindow,
		})
		return m, m.pumpNext()

	case compactionMsg:
		m.transcript.addSystem("（上下文已压缩）")
		return m, m.pumpNext()

	case runEndMsg:
		m.running = false
		m.runCh = nil
		if msg.err != nil {
			m.transcript.addSystem("运行结束：" + msg.err.Error())
		}
		// A run may have changed the working tree (edits, new files); re-probe git
		// so the status bar reflects the post-run dirty/ahead state.
		return m, fetchGitCmd(m.cwd)
	}
	return m, nil
}

// handleKey processes a key press: quit keys, prompt submit, minimal line
// editing, and viewport scrolling.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "ctrl+d":
		m.quitting = true
		return m, tea.Quit
	case "enter":
		if !m.running {
			return m.submit()
		}
		return m, nil
	case "backspace":
		if !m.running && m.input != "" {
			r := []rune(m.input)
			m.input = string(r[:len(r)-1])
		}
		return m, nil
	case "up", "down", "pgup", "pgdown", "home", "end":
		// Scrolling reaches the transcript viewport whether idle or running, so
		// history stays readable while a run streams.
		cmd := m.transcript.update(msg)
		return m, cmd
	}

	// Printable input extends the buffer (minimal; full textarea is #390). Gated
	// on idle so keystrokes don't corrupt a prompt while a run streams.
	if !m.running && msg.Text != "" {
		m.input += msg.Text
	}
	return m, nil
}

// submit starts a run for the current input line: it appends the user block,
// clears the buffer, and — when a run starter is wired — flips to running and
// returns the first pump Cmd. With no starter (pre-#392) it records the prompt
// and a system note without launching anything.
func (m Model) submit() (tea.Model, tea.Cmd) {
	prompt := strings.TrimSpace(m.input)
	if prompt == "" {
		return m, nil
	}
	m.transcript.addUser(prompt)
	m.input = ""

	if m.startRunFn == nil {
		m.transcript.addSystem("（运行未接入：会话装配见 #392）")
		return m, nil
	}

	ch, cmd := m.startRunFn(prompt)
	m.runCh = ch
	m.running = true
	return m, cmd
}

// pumpNext re-issues waitForEvent for the in-flight run so the next bridged msg
// is pulled. It returns nil once the run has ended (runCh cleared), stopping the
// pump.
func (m Model) pumpNext() tea.Cmd {
	if m.running && m.runCh != nil {
		return waitForEvent(m.runCh)
	}
	return nil
}

// View implements tea.Model. It renders the shell on the alt-screen: the
// scrolling transcript filling the top rows, the persistent status bar (#386) on
// the second-to-last row, and the minimal input line at the bottom. Setting
// AltScreen on the returned View is how Bubble Tea v2 enters/leaves the alternate
// screen buffer, so the user's scrollback is restored on quit.
func (m Model) View() tea.View {
	if m.quitting {
		return tea.View{AltScreen: true}
	}

	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	status := m.statusBar.Render(width)

	input := lipgloss.NewStyle().
		Width(width).
		Foreground(lipgloss.Color("241")).
		Render("> " + m.input)

	// Reserve one row for the status bar and one for the input line; the rest is
	// the transcript area. Guard against tiny terminals so the region never goes
	// negative.
	rows := transcriptHeight(height)
	sized := m.width > 0 && m.height > 0

	var b strings.Builder
	if sized {
		// The viewport pads its content to exactly `rows` lines.
		b.WriteString(m.transcript.view())
		b.WriteByte('\n')
	} else {
		for i := 0; i < rows; i++ {
			b.WriteByte('\n')
		}
	}
	b.WriteString(status)
	b.WriteByte('\n')
	b.WriteString(input)

	return tea.View{Content: b.String(), AltScreen: true}
}

// transcriptHeight returns the number of rows available to the transcript for a
// given total terminal height: the total minus the status bar and input rows,
// floored at zero so tiny terminals never produce a negative extent.
func transcriptHeight(total int) int {
	h := total - 2
	if h < 0 {
		h = 0
	}
	return h
}
