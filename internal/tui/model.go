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

	"charm.land/bubbles/v2/spinner"
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

	// spinner animates while a run is in flight (#93). It is driven by its own
	// tick command (started when a run begins, ignored once idle) so the running
	// indicator animates without blocking Update. Its style comes from the theme's
	// accent color.
	spinner spinner.Model
	// theme holds the resolved lipgloss styles (#89) applied to transcript
	// entries, tool cards, and the spinner/running status.
	theme tuiTheme
	// md renders assistant Markdown into styled, width-wrapped terminal output
	// (#94). It is bound to the viewport width and NO_COLOR mode, re-created only
	// when those change.
	md *mdRenderer

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

	// pickerItems supplies the selectable model catalog when the user opens the
	// interactive picker (mouse-navigable /models). onPickModel switches the live
	// model to the chosen id and returns a status line to echo. Both are injected
	// by the cmd layer so the tui package stays free of provider data; when unset,
	// the picker is unavailable and /models falls back to its text listing.
	pickerItems func() []PickerItem
	onPickModel func(id string) string
}

// SetModelPicker wires the interactive model picker (对标 pi agent's picker).
// items supplies the selectable catalog and onSelect switches the live model to
// the chosen id, returning a status line to echo in the transcript. Optional:
// when unset, the picker cannot open and /models keeps its text listing.
func (m *Model) SetModelPicker(items func() []PickerItem, onSelect func(id string) string) {
	m.pickerItems = items
	m.onPickModel = onSelect
}

// OpenModelPicker enters picker mode over the injected catalog. It is a no-op
// when no picker was wired or the catalog is empty; callers (the TUI's /models
// interception) should fall back to the text listing in that case. Returns true
// when the picker opened.
func (m *Model) OpenModelPicker() bool {
	if m.pickerItems == nil {
		return false
	}
	items := m.pickerItems()
	if len(items) == 0 {
		return false
	}
	m.state.openPicker(items)
	return true
}

// SetSlashRegistry wires a slash-command registry so typed "/name" input is
// expanded before a run starts. Optional; when unset, input runs verbatim.
func (m *Model) SetSlashRegistry(r *runtime.SlashRegistry) { m.slash = r }

// slashMenuLimit caps how many rows the autocomplete popup shows at once so a
// large command/skill catalog does not push the transcript off-screen.
const slashMenuLimit = 8

// menuMatches returns the slash commands (and skills) whose names start with
// the given prefix (the text after "/"), sorted by name and capped at
// slashMenuLimit. An empty prefix matches every command. Returns nil when no
// registry is wired or nothing matches.
func (m *Model) menuMatches(prefix string) []menuItem {
	if m.slash == nil {
		return nil
	}
	prefix = strings.ToLower(prefix)
	var items []menuItem
	for _, c := range m.slash.List() {
		if prefix == "" || strings.HasPrefix(strings.ToLower(c.Name), prefix) {
			items = append(items, menuItem{Name: c.Name, Desc: c.Description})
		}
	}
	if len(items) > slashMenuLimit {
		items = items[:slashMenuLimit]
	}
	return items
}

// refreshMenu recomputes the autocomplete menu from the current composer value:
// it opens when the input is a "/prefix" with no space yet (still naming a
// command) and closes otherwise. Called after every composer edit so the menu
// tracks what the user types.
func (m *Model) refreshMenu() {
	v := m.input.Value()
	if name, ok := slashMenuPrefix(v); ok {
		m.state.setMenu(m.menuMatches(name))
		return
	}
	m.state.closeMenu()
}

// slashMenuPrefix reports whether the composer value is in "naming a slash
// command" state — it begins with "/" and has not yet reached a space (after
// which the rest is arguments, not a command name) — and returns the partial
// name typed so far. "/mod" → ("mod", true); "/model x" → ("", false); "hi" →
// ("", false).
func slashMenuPrefix(v string) (string, bool) {
	if !strings.HasPrefix(v, "/") {
		return "", false
	}
	rest := v[1:]
	if strings.ContainsAny(rest, " \t") {
		return "", false
	}
	return rest, true
}

// newComposer builds the textinput widget used for the composer line. It is
// focused so it accepts keystrokes, with an empty prompt (the View draws its
// own "> " prefix). A real cursor is used so the terminal shows it natively.
func newComposer() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Focus()
	return ti
}

// newSpinner builds the running-indicator spinner (#93), styled with the
// theme's accent color so it matches the rest of the palette.
func newSpinner(th tuiTheme) spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(th.accent))
}

// NewModel builds a Model driven by run.
func NewModel(run RunFn) *Model {
	th := buildTheme(themeDark)
	return &Model{state: newUIState(), run: run, input: newComposer(), viewport: viewport.New(), follow: true, spinner: newSpinner(th), theme: th, md: newMDRenderer()}
}

// NewModelWithHistory builds a Model whose transcript is pre-seeded from a
// resumed session's messages (US-024). The replayed transcript renders the
// prior conversation before any new input; the model starts idle so the next
// submit continues the session via the injected run func.
func NewModelWithHistory(run RunFn, history []agentcore.AgentMessage) *Model {
	th := buildTheme(themeDark)
	m := &Model{state: newUIState(), run: run, input: newComposer(), viewport: viewport.New(), follow: true, spinner: newSpinner(th), theme: th, md: newMDRenderer()}
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
		// In picker mode, keys navigate/select the model list instead of editing
		// the composer or scrolling the transcript.
		if m.state.pick.active {
			return m.handlePickerKey(msg)
		}
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		// Wheel scrolls the picker selection when it is open; otherwise it falls
		// through to the transcript viewport so history scrolls with the wheel too.
		mouse := msg.Mouse()
		if m.state.pick.active {
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.state.pickerMoveBy(-1)
			case tea.MouseWheelDown:
				m.state.pickerMoveBy(1)
			}
			return m, nil
		}
		if m.vpReady {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			m.follow = m.viewport.AtBottom()
			return m, cmd
		}
		return m, nil

	case tea.MouseClickMsg:
		// A left click on a picker row selects that row and confirms the switch.
		if m.state.pick.active && msg.Mouse().Button == tea.MouseLeft {
			if row := m.pickerRowAt(msg.Mouse().Y); row >= 0 {
				m.state.pick.cursor = row
				return m, m.confirmPick()
			}
		}
		return m, nil

	case spinner.TickMsg:
		// Advance the spinner only while a run is in flight; once idle we stop
		// re-issuing the tick so the animation halts (AC: stop after run ends).
		if !m.state.running {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

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
		b.WriteString(m.renderTranscriptEntry(e, m.viewport.Width()))
	}
	m.viewport.SetContent(b.String())
	if m.follow {
		m.viewport.GotoBottom()
	}
}

// renderTranscriptEntry produces the final styled, width-folded text for one
// entry at the given width. Assistant text (#94) is rendered as Markdown via
// glamour, which owns both its word-wrapping (bound to width so CJK stays
// aligned) and coloring — so it is NOT run through wrapWidth/styleEntry. A
// theme-accented role glyph (#95) is prepended to the first rendered line so
// the assistant is distinguishable by prefix even under NO_COLOR, matching the
// prefix treatment of every other entry kind. Every other kind keeps the
// #91/#93 path: fold to width on rune boundaries, then apply the theme style
// after wrapping so display-width math stays on raw runes.
func (m *Model) renderTranscriptEntry(e transcriptEntry, width int) string {
	if e.Kind == entryAssistant {
		// The role marker sits on its own line above the rendered Markdown body so
		// glamour's width-bound output is never widened by an inline prefix (which
		// would push the first line past the wrap width). Under NO_COLOR the glyph
		// still marks the source; with color it carries the accent style.
		glyph := m.theme.accent.Render(strings.TrimRight(rolePrefix(entryAssistant), " "))
		return glyph + "\n" + m.md.render(fenceBuffer(e.Text), width, noColor())
	}
	line := wrapWidth(renderEntry(e, width), width)
	return m.styleEntry(e.Kind, line)
}

// styleEntry applies the theme style for an entry kind to its already-rendered,
// width-folded text (#89/#93). Tool calls and tool results are colorized as
// visually distinct "cards" (a colored header glyph line and a greyed result
// body), separating them from plain assistant/user text. Styling is applied
// after wrapping so display-width math (#91) stays on the raw runes, not ANSI
// escapes. Under NO_COLOR every theme style is a no-op, so this returns text
// unchanged and meaning rides on the prefix glyphs instead.
func (m *Model) styleEntry(kind entryKind, text string) string {
	switch kind {
	case entryUser:
		return m.theme.user.Render(text)
	case entryAssistant:
		return m.theme.assistant.Render(text)
	case entryToolCall:
		return m.theme.toolCall.Render(text)
	case entryToolResult:
		return m.theme.toolResult.Render(text)
	case entrySystem:
		return m.theme.system.Render(text)
	default:
		return text
	}
}

// handleKey processes a key press. Ctrl+C and Enter are handled explicitly
// (two-phase quit/interrupt and submit); every other key is delegated to the
// textinput widget, which handles multi-byte UTF-8 (CJK/emoji) input and
// rune-level cursor movement/deletion. Any such key also disarms the two-phase
// Ctrl+C.
func (m *Model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When the slash-command autocomplete menu is open, it captures the
	// navigation/completion keys before they reach the composer or transcript.
	// Tab always completes the highlighted command into the composer. Enter
	// completes it too UNLESS the composer already spells a complete command name
	// (the user typed it in full), in which case Enter submits — so a fully-typed
	// "/model" runs without a second keystroke, while Enter on a partial "/mod"
	// completes to "/model " rather than submitting an unknown command.
	if m.state.menu.active {
		switch k.String() {
		case "up", "ctrl+p":
			m.state.menuMoveBy(-1)
			return m, nil
		case "down", "ctrl+n":
			m.state.menuMoveBy(1)
			return m, nil
		case "tab":
			return m.completeMenu()
		case "esc":
			m.state.closeMenu()
			return m, nil
		case "enter":
			if !m.composerNamesCommand() {
				return m.completeMenu()
			}
			m.state.closeMenu()
			// fall through to the outer switch's "enter" case to submit.
		}
	}
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
		m.state.closeMenu()
		prompt, start := m.state.submit(m.input.Value())
		m.input.Reset()
		if start {
			// "/models" (no args) opens the interactive picker if one is wired,
			// instead of echoing the static text listing. With an argument (e.g.
			// "/models nvidia") it falls through to the slash action so the filtered
			// text listing still works.
			if isBareModelsCommand(prompt) && m.OpenModelPicker() {
				// submit() bumped runID and echoed the "/models" line; undo both so
				// the picker opens cleanly with no stray transcript entry.
				m.state.cancelStartedRun()
				return m, nil
			}
			// Expand a slash-command into its prompt text before running. An
			// unknown "/name" is surfaced as a local system line and no run
			// starts. An action command (e.g. /model) runs its side effect here
			// and shows a status line without starting an agent run. A
			// non-command prompt passes through unchanged.
			if m.slash != nil {
				out, err := m.slash.ResolveOutcome(prompt)
				if err != nil {
					m.state.abortStartedRun(err.Error())
					m.follow = true
					m.refreshViewport()
					return m, nil
				}
				if out.Handled && out.Kind == runtime.SlashAction {
					// Action already ran; don't start a run. Undo the run submit()
					// began and echo the action's status as a local system line.
					m.state.abortStartedRun(out.Message)
					m.follow = true
					m.refreshViewport()
					return m, nil
				}
				if out.Handled {
					prompt = out.Prompt
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
		// After the edit, recompute the slash-command autocomplete menu so it
		// tracks the "/prefix" being typed (opening, filtering, or closing it).
		m.refreshMenu()
		return m, cmd
	}
}

// composerNamesCommand reports whether the composer value is exactly "/name"
// where name is a command the registry knows (no arguments yet). It is the
// signal that Enter should submit the fully-typed command rather than complete
// the highlighted menu row. Returns false when no registry is wired.
func (m *Model) composerNamesCommand() bool {
	if m.slash == nil {
		return false
	}
	name, naming := slashMenuPrefix(m.input.Value())
	if !naming || name == "" {
		return false
	}
	_, ok := m.slash.Lookup(name)
	return ok
}

// completeMenu completes the highlighted autocomplete row into the composer:
// the input becomes "/name " (trailing space so the user types arguments next)
// and the menu closes. A no-op returning nil when the menu has no selection.
func (m *Model) completeMenu() (tea.Model, tea.Cmd) {
	item, ok := m.state.menuCurrent()
	if !ok {
		return m, nil
	}
	m.input.SetValue("/" + item.Name + " ")
	m.input.CursorEnd()
	m.state.closeMenu()
	return m, nil
}

// pickerHeaderRows is the number of lines the picker draws above its first
// selectable row (a title line plus a blank spacer). Click-to-select maps a
// mouse Y back to an item index by subtracting these rows, so it must match the
// header the View renders.
const pickerHeaderRows = 2

// handlePickerKey processes a key press while the model picker is open. Up/down
// (and j/k, ctrl+p/ctrl+n) move the selection, Enter confirms the switch, and
// Esc/ctrl+c/q closes the picker without changing the model. All other keys are
// swallowed so stray typing does not leak into the composer behind the picker.
func (m *Model) handlePickerKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "ctrl+p", "k":
		m.state.pickerMoveBy(-1)
		return m, nil
	case "down", "ctrl+n", "j":
		m.state.pickerMoveBy(1)
		return m, nil
	case "pgup":
		m.state.pickerMoveBy(-10)
		return m, nil
	case "pgdown":
		m.state.pickerMoveBy(10)
		return m, nil
	case "home":
		m.state.pickerMoveBy(-len(m.state.pick.items))
		return m, nil
	case "end":
		m.state.pickerMoveBy(len(m.state.pick.items))
		return m, nil
	case "enter":
		return m, m.confirmPick()
	case "esc", "ctrl+c", "q":
		m.state.closePicker()
		m.refreshViewport()
		return m, nil
	default:
		return m, nil
	}
}

// confirmPick applies the model under the cursor via the injected onPickModel
// callback, echoes its status line into the transcript, and closes the picker.
// It returns no command; the switch takes effect on the next prompt (the live
// run config is mutated by the callback).
func (m *Model) confirmPick() tea.Cmd {
	item, ok := m.state.pickerCurrent()
	m.state.closePicker()
	if ok && m.onPickModel != nil {
		m.state.pushSystem(m.onPickModel(item.ID))
	}
	m.follow = true
	m.refreshViewport()
	return nil
}

// pickerRowAt maps a terminal row (mouse Y) to an item index in the open
// picker, or -1 when the row is outside the selectable list. It mirrors the
// layout View draws: pickerHeaderRows lines precede row 0.
func (m *Model) pickerRowAt(y int) int {
	idx := y - pickerHeaderRows
	if idx < 0 || idx >= len(m.state.pick.items) {
		return -1
	}
	return idx
}

// isBareModelsCommand reports whether prompt is the "/models" slash command
// with no argument (so it should open the interactive picker). "/models nvidia"
// keeps the filtered text-listing path, and any non-command returns false.
func isBareModelsCommand(prompt string) bool {
	return strings.TrimSpace(prompt) == "/models"
}

// startRun launches the agent run for prompt and returns a command that drains
// its event stream into Update. The drain runs in a goroutine spawned by the
// command; each event is Sent back tagged with the current runID. The spinner's
// tick is started alongside so the running indicator animates (#93); it halts
// itself once the run ends (Update ignores ticks while idle).
func (m *Model) startRun(prompt string) tea.Cmd {
	runID := m.state.runID
	stream, cancel := m.run(context.Background(), prompt, m.state.drainSteering)
	m.cancel = cancel
	return tea.Batch(
		func() tea.Msg {
			go m.drain(runID, stream)
			return nil
		},
		m.spinner.Tick,
	)
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
	// Picker mode takes over the whole view: the transcript is replaced by the
	// selectable model list so mouse/keyboard navigation is unambiguous.
	if m.state.pick.active {
		v := tea.NewView(m.renderPicker())
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}
	var b strings.Builder
	if m.vpReady {
		b.WriteString(m.viewport.View())
		b.WriteByte('\n')
	} else {
		for _, e := range m.state.transcript {
			b.WriteString(m.renderTranscriptEntry(e, m.width))
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n")
	// The slash-command autocomplete popup renders just above the composer when
	// active, so the user sees the matching commands/skills while typing "/".
	if m.state.menu.active {
		b.WriteString(m.renderMenu())
		b.WriteByte('\n')
	}
	b.WriteString("> ")
	b.WriteString(m.input.View())
	if m.state.running {
		// Animated spinner + running status (#93), styled with the theme accent.
		b.WriteString("  ")
		b.WriteString(m.spinner.View())
		b.WriteString(m.theme.accent.Render(" (running; type to steer, Ctrl+C to interrupt)"))
	} else if m.state.ctrlCArmed {
		b.WriteString(m.theme.system.Render("  (press Ctrl+C again to quit)"))
	}
	// Enable the mouse wheel so the transcript can be scrolled with it (the
	// picker uses the same mode for wheel-selection).
	v := tea.NewView(b.String())
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// renderPicker draws the interactive model picker: a title line, a blank
// spacer, then one row per item with the cursor row highlighted via the theme
// accent. The row layout matches pickerHeaderRows / pickerRowAt so click-to-
// select maps a mouse Y back to the right item. Each row is folded to width on
// rune boundaries so long CJK labels do not overflow (#91).
func (m *Model) renderPicker() string {
	width := m.width
	var b strings.Builder
	b.WriteString(m.theme.accent.Render("select a model  (↑/↓ or wheel to move, Enter/click to switch, Esc to cancel)"))
	b.WriteByte('\n')
	for i, it := range m.state.pick.items {
		b.WriteByte('\n')
		marker := "  "
		line := it.Label
		if line == "" {
			line = it.ID
		}
		if i == m.state.pick.cursor {
			marker = "▸ "
		}
		row := truncateWidth(marker+line, width)
		if i == m.state.pick.cursor {
			b.WriteString(m.theme.accent.Render(row))
		} else {
			b.WriteString(row)
		}
	}
	return b.String()
}

// renderMenu draws the slash-command autocomplete popup: one row per matching
// command, "/name — description", with the highlighted row marked and accented.
// Rows are folded to width on rune boundaries so long CJK descriptions do not
// overflow. It is drawn above the composer and does not take over the view, so
// the user keeps typing to filter.
func (m *Model) renderMenu() string {
	width := m.width
	var b strings.Builder
	for i, it := range m.state.menu.items {
		if i > 0 {
			b.WriteByte('\n')
		}
		marker := "  "
		if i == m.state.menu.cursor {
			marker = "▸ "
		}
		line := "/" + it.Name
		if it.Desc != "" {
			line += " — " + it.Desc
		}
		row := truncateWidth(marker+line, width)
		if i == m.state.menu.cursor {
			b.WriteString(m.theme.accent.Render(row))
		} else {
			b.WriteString(m.theme.system.Render(row))
		}
	}
	return b.String()
}

// rolePrefix returns the leading role marker (glyph + trailing space) for an
// entry kind (#95). The prefix is what distinguishes conversation sources when
// color is stripped (NO_COLOR), so every kind carries a distinct glyph:
//
//	you>  user input       ● assistant reply
//	⚙     tool invocation   ↳ tool result      · local system line
//
// renderEntry prepends it to plain entries before styling; the assistant path
// (Markdown) prepends the accented glyph itself since its body is rendered by
// glamour rather than the plain wrap path.
func rolePrefix(kind entryKind) string {
	switch kind {
	case entryUser:
		return "you> "
	case entryAssistant:
		return "● "
	case entryToolCall:
		return "⚙ "
	case entryToolResult:
		return "  ↳ "
	case entrySystem:
		return "· "
	default:
		return ""
	}
}

// renderEntry formats one transcript entry with a role prefix. Assistant text
// is passed through fenceBuffer so an unterminated code fence renders cleanly
// (no half-open ``` breaking the layout mid-stream). width is the terminal
// width used to truncate a long tool-result summary on rune boundaries; a
// non-positive width disables that truncation.
func renderEntry(e transcriptEntry, width int) string {
	switch e.Kind {
	case entryUser:
		return rolePrefix(entryUser) + e.Text
	case entryAssistant:
		return fenceBuffer(e.Text)
	case entryToolCall:
		return rolePrefix(entryToolCall) + e.Text
	case entryToolResult:
		prefix := rolePrefix(entryToolResult)
		gist := firstLine(e.Text)
		if width > 0 {
			// Reserve the prefix's display columns so the whole line fits.
			gist = truncateWidth(gist, width-displayWidth(prefix))
		}
		return prefix + gist
	case entrySystem:
		return rolePrefix(entrySystem) + e.Text
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
