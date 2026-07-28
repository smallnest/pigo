package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Model is the root Bubble Tea model for the full-screen TUI. In this skeleton
// node it holds only the terminal dimensions and renders a static three-region
// shell: a placeholder status bar, an empty transcript area, and an empty input
// line. Downstream nodes grow it into the real transcript/tool-card/status-bar
// composition (see tasks/spec-tui-agent.md Section 2.2); the Init/Update/View
// contract and the alt-screen + quit-key handling established here stay stable.
type Model struct {
	opts Options

	// width and height track the terminal size reported by tea.WindowSizeMsg.
	// They are zero until the first size message arrives; View degrades to a
	// minimal render in that window.
	width  int
	height int

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
	return Model{
		opts:      opts,
		cwd:       cwd,
		statusBar: newStatusBar(DefaultTheme(), opts, cwd),
	}
}

// Init implements tea.Model. It kicks off the async git probe so the status bar
// can show the branch/dirty state as soon as it resolves; the alt-screen is
// requested declaratively via the AltScreen field on the View returned by View.
func (m Model) Init() tea.Cmd {
	return fetchGitCmd(m.cwd)
}

// Update implements tea.Model. It tracks the terminal size and quits on the
// standard exit keys (Ctrl+C / Ctrl+D). Returning tea.Quit lets Bubble Tea tear
// down the program and restore the terminal (leaving the alt-screen) cleanly.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case gitInfoMsg:
		m.statusBar.SetGit(msg)
		return m, nil
	case telemetryMsg:
		m.statusBar.SetTelemetry(telemetryEventView{
			util:   msg.ev.ContextUtilization,
			window: msg.ev.ContextWindow,
		})
		return m, nil
	case runEndMsg:
		// A run may have changed the working tree (edits, new files); re-probe git
		// so the status bar reflects the post-run dirty/ahead state.
		return m, fetchGitCmd(m.cwd)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// View implements tea.Model. It renders the shell on the alt-screen: an empty
// transcript area that grows to fill the available height, the persistent status
// bar (#386) on the second-to-last row, and the input line at the bottom.
// Setting AltScreen on the returned View is how Bubble Tea v2 enters/leaves the
// alternate screen buffer, so the user's scrollback is restored on quit.
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
		Render("> ")

	// Reserve one row for the status bar and one for the input line; the rest is
	// the (currently empty) transcript area. Guard against tiny terminals so the
	// filler count never goes negative.
	transcriptRows := height - 2
	if transcriptRows < 0 {
		transcriptRows = 0
	}

	var b strings.Builder
	for i := 0; i < transcriptRows; i++ {
		b.WriteByte('\n')
	}
	b.WriteString(status)
	b.WriteByte('\n')
	b.WriteString(input)

	return tea.View{Content: b.String(), AltScreen: true}
}
