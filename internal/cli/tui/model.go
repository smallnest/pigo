package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/runtime"
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

	// input is the multi-line prompt editor (#390). It wraps a bubbles textarea
	// so CJK / emoji are edited by rune (no dropped-byte bug), Enter submits and
	// Alt+Enter inserts a newline. It is blurred while a run is in flight.
	input input

	// running is true while an agent run is draining through runCh. Input submit
	// is gated on it so a new run cannot start mid-run.
	running bool
	// runCh is the bridge channel for the in-flight run, or nil when idle. Update
	// re-issues waitForEvent(runCh) after every bridged msg except runEndMsg.
	runCh chan tea.Msg

	// startRunFn launches an agent run for the submitted prompt, returning the
	// bridge channel and the first waitForEvent Cmd (see bridge.startRun). It is
	// bound to runSession.startRun by withSession (#392): the real binding
	// constructs an AgentContext + RunConfig from opts and the live session. It is
	// nil for a session-less model (the pure constructor / tests), in which case a
	// submit records the prompt but starts no run.
	startRunFn func(prompt string) (chan tea.Msg, tea.Cmd)

	// session is the assembled run/persistence state (store, header, growing
	// context, live config). It is nil for a session-less model; when set, the
	// model persists the conversation to ~/.pigo/sessions after each turn ends.
	session *runSession

	// interruptFn cancels the in-flight run (the first stage of the two-stage
	// interrupt, FR-14): pressing Esc / Ctrl+C while running signals the run to
	// stop rather than quitting the program. It is a seam wired alongside
	// startRunFn by session assembly (#392) — typically the run ctx's cancel
	// func. Until then it may be nil, in which case an interrupt while running is
	// a safe no-op (the pump keeps draining until it ends on its own).
	interruptFn func()

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

	// slash is the shared slash-command registry (#383) the TUI consults exactly
	// as the REPL does: /model, /help, user templates, plugin commands and skills.
	// It is bound to live so a /model switch mutates the same config the run loop
	// reads. Built in NewModel (built-ins + disk templates) and rebuilt in
	// withSession against the session's live config.
	slash *runtime.SlashRegistry
	// live is the mutable run configuration the /model command switches. In a
	// session-bound model it is the SAME pointer the run loop reads (set by
	// withSession), so a switch takes effect on the next turn.
	live *cli.LiveConfig
	// menu is the autocomplete popup shown while a "/name" is being typed (#391).
	// It filters slash by the typed prefix; the model intercepts arrow/Tab/Enter
	// keys to drive it before delegating to the textarea.
	menu slashMenu

	// toolCards indexes the rich tool-call cards (#389, US-006) by tool-call id so
	// a toolEndMsg can locate the card started earlier and flip its state / attach
	// the parsed response. Each card is also appended to the transcript as an
	// ordered block (by pointer), so mutating one here re-renders it inline on the
	// next reflow.
	toolCards map[string]*toolCard
	// lastToolCard points at the most recently started card; Ctrl+O toggles its
	// expanded state and re-flows the transcript.
	lastToolCard *toolCard
}

// NewModel builds the root model from the assembled Options. It reads the
// current working directory (for the status bar's path display and git probe)
// and assembles the shared slash-command registry (#391), which reads the user
// prompt-template dirs (~/.pigo/{commands,prompts}) and the pre-loaded skills;
// missing dirs are not an error. The registry is bound here to a live config
// derived from Options; withSession rebinds it to the session's live config so a
// /model switch reaches the run loop.
func NewModel(opts Options) Model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	theme := DefaultTheme()
	live := &cli.LiveConfig{
		Model:         opts.Model,
		ProviderName:  opts.ProviderName,
		Provider:      opts.Provider,
		BaseURL:       opts.BaseURL,
		Protocol:      opts.Protocol,
		ThinkingLevel: opts.ThinkingLevel,
		ContextWindow: cli.DefaultContextWindow,
	}
	return Model{
		opts:       opts,
		theme:      theme,
		transcript: newTranscript(theme),
		input:      newInput(),
		cwd:        cwd,
		statusBar:  newStatusBar(theme, opts, cwd),
		toolCards:  make(map[string]*toolCard),
		slash:      newSlashRegistry(opts, live),
		live:       live,
		menu:       newSlashMenu(theme),
	}
}

// withSession binds the assembled run session to the model: it wires the real
// run seam (startRunFn) and, for a resumed session, replays the prior history
// into the transcript so the user sees the conversation so far before entering
// interactive mode. Run calls it right after NewModel; the session-less
// constructor path (tests, pure construction) leaves startRunFn nil.
func (m Model) withSession(s *runSession, history []agentcore.Message) Model {
	m.session = s
	m.startRunFn = s.startRun
	m.interruptFn = s.interrupt
	// Rebind the registry to the session's live config so /model mutates the very
	// config the run loop reads (buildConfig), not the throwaway one NewModel made.
	m.live = s.live
	m.slash = newSlashRegistry(m.opts, s.live)
	seedTranscript(&m.transcript, history)
	return m
}

// Init implements tea.Model. It kicks off the async git probe so the status bar
// can show the branch/dirty state as soon as it resolves; the alt-screen is
// requested declaratively via the AltScreen field on the View returned by View.
func (m Model) Init() tea.Cmd {
	return tea.Batch(fetchGitCmd(m.cwd), m.input.Focus())
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
		m.relayout()
		return m, nil

	case gitInfoMsg:
		m.statusBar.SetGit(msg)
		return m, nil

	case tea.MouseWheelMsg:
		// Mouse-wheel scrolling reaches the transcript viewport whether idle or
		// running, so history stays scrollable with the wheel — not just PgUp/PgDn.
		// The viewport (MouseWheelEnabled by default) turns the wheel event into a
		// scroll; enabling MouseModeCellMotion in View is what makes the terminal
		// deliver these events under the alt-screen at all.
		cmd := m.transcript.update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case textDeltaMsg:
		m.transcript.appendDelta(msg.delta)
		return m, m.pumpNext()

	case turnEndMsg:
		m.transcript.finalizeTurn(msg.msg)
		return m, m.pumpNext()

	case toolStartMsg:
		// Create a rich tool-call card, index it by id for the later end event, and
		// append it as an ordered transcript block so it renders inline (#389).
		card := &toolCard{id: msg.id, name: msg.name, input: msg.input, state: cardRunning}
		m.toolCards[msg.id] = card
		m.lastToolCard = card
		m.transcript.addToolCard(card)
		return m, m.pumpNext()

	case toolUpdateMsg:
		return m, m.pumpNext()

	case toolEndMsg:
		// Flip the card's state and attach the parsed response tree. The card is
		// held by pointer in the transcript, so a reflow re-renders it in place.
		if card, ok := m.toolCards[msg.id]; ok {
			if msg.ok {
				card.state = cardSuccess
			} else {
				card.state = cardWarn
			}
			card.response = parseToolResult(msg.result)
			m.transcript.reflow()
		}
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
		// Persist the turn's new messages as a branch so the conversation survives
		// exit and can be resumed (FR-16). This is race-free: the pump goroutine
		// owns agentCtx.Messages during the run and only sends runEndMsg after
		// DrainStream returns (loop done), so no goroutine is still writing the
		// context when persist reads it here on the tea goroutine. A save failure
		// is surfaced but non-fatal.
		if m.session != nil {
			if err := m.session.persist(); err != nil {
				m.transcript.addSystem("会话保存失败：" + err.Error())
			}
		}
		// The editor was blurred at submit; re-enable it so the next prompt can be
		// typed, and re-probe git since a run may have changed the working tree.
		focus := m.input.Focus()
		return m, tea.Batch(focus, fetchGitCmd(m.cwd))
	}
	return m, nil
}

// handleKey processes a key press. It resolves the keys the shell owns —
// two-stage interrupt/quit, prompt submit, transcript scrolling — and delegates
// everything else (character entry, in-buffer cursor movement, Alt+Enter
// newline) to the input editor while idle. Keys are matched via KeyPressMsg
// .String() so the mapping is terminal-independent.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// While idle with the autocomplete popup open, the arrow / Tab / Esc keys
	// drive the menu instead of the transcript or textarea (FR-15). Enter is left
	// to the main switch below, which routes through submit → runSlash so the
	// selected/typed command runs. These are matched via KeyPressMsg.String() so
	// the mapping is terminal-independent.
	if !m.running && m.menu.active {
		switch msg.String() {
		case "up":
			m.menu.moveUp()
			return m, nil
		case "down":
			m.menu.moveDown()
			return m, nil
		case "tab":
			m = m.completeSlash()
			m.relayout()
			return m, nil
		case "esc":
			m.menu.close()
			m.relayout()
			return m, nil
		case "enter":
			return m.submitSlashSelected()
		}
	}

	switch msg.String() {
	case "esc", "ctrl+c":
		// Two-stage interrupt (FR-14): while a run is in flight the first press
		// interrupts that run (cancel the run ctx / signal the pump) and stays in
		// the program; when idle it quits.
		if m.running {
			if m.interruptFn != nil {
				m.interruptFn()
			}
			m.transcript.addSystem("（正在中断当前运行…）")
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "ctrl+o":
		// Toggle the most-recent tool card between its capped preview and the full
		// response tree, then re-flow so the change shows inline (#389).
		if m.lastToolCard != nil {
			m.lastToolCard.expanded = !m.lastToolCard.expanded
			m.transcript.reflow()
		}
		return m, nil
	case "ctrl+d":
		// Ctrl+D quits only when idle; mid-run it is ignored so a run is never
		// dropped by a stray EOF key.
		if !m.running {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case "enter":
		// Enter submits the composed buffer (FR-13). Shift+Enter inserts a newline
		// (rebound in newInput) so the editor is a true multi-line composer; when
		// the slash menu is open, Enter runs the highlighted command (handled
		// above), so this branch is only reached with the menu closed.
		if !m.running {
			return m.submit()
		}
		return m, nil
	case "pgup", "pgdown":
		// Page scrolling reaches the transcript viewport whether idle or running,
		// so history stays readable while a run streams. Line-oriented keys (up /
		// down / home / end) belong to the multi-line editor and are delegated
		// below.
		cmd := m.transcript.update(msg)
		return m, cmd
	}

	// Everything else is editing input; gated on idle so keystrokes never corrupt
	// an in-flight prompt. textarea handles CJK / emoji by rune and Alt+Enter as
	// a newline. After the buffer changes, refresh the autocomplete popup so it
	// opens/filters/closes as the user types a "/name" prefix.
	if !m.running {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.menu.refresh(m.input.Value(), m.slash)
		m.relayout()
		return m, cmd
	}
	return m, nil
}

// submit starts a run for the current buffer: it appends the user block, clears
// and blurs the editor, flips to running, and — when a run starter is wired —
// returns the first pump Cmd. With no starter (pre-#392) it records the prompt
// and a system note without launching anything, and leaves the editor ready for
// the next line.
func (m Model) submit() (tea.Model, tea.Cmd) {
	prompt := strings.TrimSpace(m.input.Value())
	if prompt == "" {
		return m, nil
	}
	// A "/name ..." line is a slash-command invocation, not a prompt: resolve it
	// against the shared registry (same as the REPL) rather than sending it to the
	// agent verbatim.
	if strings.HasPrefix(prompt, "/") {
		return m.runSlash(prompt)
	}
	m.transcript.addUser(prompt)
	m.input.Clear()
	m.menu.close()
	m.relayout()
	return m.startPrompt(prompt)
}

// completeSlash fills the buffer with the highlighted candidate's "/name " so the
// user can go on to type arguments; the trailing space ends name-completion, so
// the refresh closes the popup. It is the Tab action while the menu is open.
func (m Model) completeSlash() Model {
	if c, ok := m.menu.current(); ok {
		m.input.SetValue("/" + c.Name + " ")
		m.menu.refresh(m.input.Value(), m.slash)
	}
	return m
}

// submitSlashSelected runs the command the popup highlights (Enter while the
// menu is open). Navigating with the arrows then pressing Enter runs the
// selected command even if the typed prefix is shorter; with no selection it
// falls back to the raw buffer so a fully-typed "/name" still runs.
func (m Model) submitSlashSelected() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	if c, ok := m.menu.current(); ok {
		line = "/" + c.Name
	}
	return m.runSlash(line)
}

// runSlash resolves a slash-command line against the shared registry and folds
// its outcome into the transcript, mirroring the REPL's dispatch: the invocation
// is echoed as a user block; an action command's status (e.g. /help, /model)
// renders as a system block; a prompt/skill command's expanded text starts a
// run; a hybrid (plugin) command shows its notifications then runs its prompt.
// An unknown command surfaces the resolver error as a system block.
func (m Model) runSlash(line string) (tea.Model, tea.Cmd) {
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	m.relayout()
	if m.slash == nil {
		m.transcript.addSystem("斜杠命令不可用")
		return m, nil
	}
	outcome, err := m.slash.ResolveOutcome(line)
	if err != nil {
		m.transcript.addSystem(err.Error())
		return m, nil
	}
	if outcome.Message != "" {
		m.transcript.addSystem(outcome.Message)
	}
	// An action command is complete once its status is shown; a hybrid with no
	// prompt (notifications only) likewise starts no run.
	if outcome.Kind == runtime.SlashAction || outcome.Prompt == "" {
		return m, nil
	}
	return m.startPrompt(outcome.Prompt)
}

// startPrompt launches an agent run for prompt, blurring the editor and flipping
// to running when a run starter is wired. With no starter (pre-session model /
// tests) it records the pre-#392 system note and stays idle. It is shared by a
// plain submit and by a slash prompt/skill command.
func (m Model) startPrompt(prompt string) (tea.Model, tea.Cmd) {
	if m.startRunFn == nil {
		m.transcript.addSystem("（运行未接入：会话装配见 #392）")
		return m, nil
	}
	m.input.Blur()
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
// scrolling transcript filling the top rows, then the autocomplete popup (when
// open) and the multi-line input editor, and finally the persistent status bar
// (#386) on the very bottom row — below the input, per the layout fix. Setting
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

	// The input editor renders its own prompt column and cursor across as many
	// rows as the buffer currently spans (up to maxInputRows).
	input := m.input.View()

	// Fallback transcript rows before the first size message; once sized the
	// viewport is pre-sized by relayout and pads its own content.
	rows := transcriptHeight(height)
	sized := m.width > 0 && m.height > 0

	var b strings.Builder
	if sized {
		// The viewport pads its content to exactly the rows relayout reserved.
		b.WriteString(m.transcript.view())
		b.WriteByte('\n')
	} else {
		for i := 0; i < rows; i++ {
			b.WriteByte('\n')
		}
	}
	// The autocomplete popup, when open, renders just above the input line as an
	// overlay (it contributes no rows while idle, so the empty-shell layout is
	// unchanged).
	if menu := m.menu.view(width); menu != "" {
		b.WriteString(menu)
		b.WriteByte('\n')
	}
	b.WriteString(input)
	b.WriteByte('\n')
	// The status bar is the final line, pinned to the very bottom of the shell
	// below the input editor.
	b.WriteString(status)

	// MouseModeCellMotion enables click/release/wheel events. Without it the
	// alt-screen swallows the wheel (no native scrollback), so history could only
	// be reached via PgUp/PgDn; enabling it lets the wheel scroll the transcript.
	return tea.View{Content: b.String(), AltScreen: true, MouseMode: tea.MouseModeCellMotion}
}

// relayout re-sizes the transcript to the rows left after reserving the status
// bar (1 row), the current input editor height, and any open autocomplete popup,
// and reserves one content column for the transcript scrollbar. It is called on
// every resize and after any edit that changes the input height or menu row
// count, so the transcript region always fills exactly the space above the
// input/status chrome.
func (m *Model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	rows := m.height - 1 - m.input.Height() - m.menu.rows()
	if rows < 0 {
		rows = 0
	}
	content := m.width - 1 // reserve a column for the scrollbar
	if content < 0 {
		content = 0
	}
	m.transcript.setSize(content, rows)
	m.input.SetWidth(m.width)
}

// transcriptHeight returns the fallback number of rows for the transcript before
// the first size message arrives: the total minus the status bar and a single
// input row, floored at zero so tiny terminals never produce a negative extent.
func transcriptHeight(total int) int {
	h := total - 2
	if h < 0 {
		h = 0
	}
	return h
}
