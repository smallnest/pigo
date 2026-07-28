package tui

import (
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
}

// NewModel builds the root model from the assembled Options. It performs no I/O;
// session/live/slash/trust assembly is deferred to downstream nodes.
func NewModel(opts Options) Model {
	return Model{opts: opts}
}

// Init implements tea.Model. The skeleton has no startup command; the alt-screen
// is requested declaratively via the AltScreen field on the View returned by
// View, so there is nothing to kick off here.
func (m Model) Init() tea.Cmd {
	return nil
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
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// View implements tea.Model. It renders the empty shell on the alt-screen: a
// placeholder status bar at the top, an empty transcript area that grows to fill
// the available height, and an empty input line at the bottom. Setting
// AltScreen on the returned View is how Bubble Tea v2 enters/leaves the
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

	status := lipgloss.NewStyle().
		Width(width).
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("62")).
		Render("pigo — TUI (骨架) · Ctrl+C/Ctrl+D 退出")

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
	b.WriteString(status)
	b.WriteByte('\n')
	for i := 0; i < transcriptRows; i++ {
		b.WriteByte('\n')
	}
	b.WriteString(input)

	return tea.View{Content: b.String(), AltScreen: true}
}
