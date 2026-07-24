// This file implements the line-based interactive REPL (US-003/#106) that
// replaces the former full-screen bubbletea TUI. When pigo is invoked without a
// prompt on a terminal it runs a simple read → run → stream-print loop over a
// persisted session: no full-screen rendering, no popup menus, no viewport — a
// prompt, the agent's streamed reply, and a new prompt.
//
// The REPL is a synchronous loop on the main goroutine: it reads one line,
// resolves slash-commands, launches an agent run, drains the run's event stream
// to stdout as it streams, persists the session, and returns to the prompt. A
// SIGINT during a run cancels that run's context and returns to the prompt; when
// idle, the reader sees EOF/interrupt and exits cleanly.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/clipboard"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/trust"
)

// replDeps bundles the collaborators a REPL run needs. They are assembled once
// by runInteractive and reused across every prompt in the session.
type replDeps struct {
	store    *session.Store
	header   session.SessionHeader
	agentCtx *agentcore.AgentContext
	live     *liveRunConfig
	reg      *agenttool.ToolRegistry
	// reminders holds the per-turn system-reminder providers (US-002). It is nil
	// when no todo tool is present; when set, streamRun wires it so ephemeral
	// <system-reminder> context is injected into each turn's request.
	reminders *runtime.ReminderRegistry
	slash     *runtime.SlashRegistry
	creds     *provider.CredentialStore

	// notifier delivers agent lifecycle events to subscribed plugins (US-017,
	// #133). It is nil when no plugin subscribes; DrainStream's OnEvent stays
	// unset in that case.
	notifier *plugin.EventNotifier

	// trust persists project-trust decisions (US-018, #134). It is nil when
	// trust is disabled (e.g. the store could not be loaded); when nil the
	// BeforeToolCall hook is not installed and the first-run prompt is skipped.
	trust *trust.Manager
	// cwd is the directory pigo was launched in, used as the trust key and as
	// the directory side-effect tools are gated against. It does not change
	// during a session (pigo does not cd).
	cwd string
	// in is the shared buffered input reader. The main loop and the tool-call
	// confirmation prompt both read from it so input typed ahead is never split
	// between them. It is created by runInteractive (wrapping os.Stdin) or, for
	// direct test callers, lazily from the in argument at the top of runREPL.
	in *bufio.Reader
	// confirmMu serializes tool-call confirmation prompts so concurrent
	// parallel side-effect tool calls do not interleave on the shared
	// stdin/stdout. It is a pointer so every value copy of replDeps (passed to
	// streamRun per prompt) shares one mutex.
	confirmMu *sync.Mutex

	// curLeaf is the id of the entry the conversation currently descends from —
	// the active leaf of the on-disk session tree (US-007, #123). A fresh session
	// starts empty (""); each persisted turn advances it to the newly written
	// leaf. /tree can move it to a historical entry so the next turn branches from
	// there rather than the tip.
	curLeaf string
	// persisted is the number of agentCtx.Messages already written to disk. The
	// per-turn persist appends only Messages[persisted:] as a branch descending
	// from curLeaf, so switching leaves and continuing grows a real tree instead
	// of rewriting the file as a single linear chain.
	persisted int

	// goal holds the session's autonomous-goal state (对标 pi-goal), driven by
	// the /goal command. It is always non-nil (runInteractive seeds an idle
	// state); the GoalReminderProvider and the goal tools share this handle so
	// the objective is injected each turn and goal_complete/goal_blocked can end
	// the run.
	goal *agenttool.GoalState
}

// replScanBufInit is the initial size of the shared input reader. A REPL user
// may paste a long single line (a big prompt or a pasted file); bufio.Reader
// grows its returned string beyond this buffer on demand, so a long line is
// still read whole (just accumulated rather than capped). This drops the hard
// 4MiB cap the previous bufio.Scanner had: ReadString is unbounded, so a
// pathological paste can grow the line buffer. That is an acceptable tradeoff
// for a terminal REPL (where OS line buffering bounds normal input) in exchange
// for correct buffer sharing with the tool-call confirmation prompt.
const replScanBufInit = 64 * 1024

// notifierHandle returns the plugin event-delivery callback for this session's
// runs, or nil when no plugin subscribed. Returning nil (rather than a func that
// dispatches to a nil notifier) keeps DrainStream's OnEvent unset in the common
// no-plugin case, so the drain loop skips the per-event call entirely.
func (deps replDeps) notifierHandle() func(agentcore.AgentEvent) {
	if deps.notifier == nil {
		return nil
	}
	return deps.notifier.Handle
}

// runREPL runs the read → run → stream-print loop until EOF, /exit or /quit. It
// reads from in (os.Stdin in production) and writes prompts, streamed replies
// and status lines to out (os.Stdout). It is the interactive replacement for the
// bubbletea program.
func runREPL(in io.Reader, out io.Writer, deps replDeps) error {
	// Use the shared buffered reader so the main loop and the tool-call
	// confirmation prompt read from the same buffer: input is never split
	// between them the way it would be if the loop used a bufio.Scanner with
	// its own private buffer (a Scanner can read past the current line into its
	// internal buffer, trapping bytes the confirmation prompt would then never
	// see). deps.in is set by runInteractive (wrapping os.Stdin); the in
	// parameter is only a fallback for direct callers (tests) that do not set
	// deps.in, and is ignored once deps.in is populated.
	if deps.in == nil {
		deps.in = bufio.NewReaderSize(in, replScanBufInit)
	}
	var priorInputs []string
	for _, msg := range deps.agentCtx.Messages {
		if user, ok := msg.(agentcore.UserMessage); ok {
			priorInputs = append(priorInputs, agentcore.ContentToText(user.Content))
		}
	}
	editor := newREPLLineEditor(in, deps.in, out, deps.slash, priorInputs)
	editor.models = append([]string{deps.live.model}, editor.models...)

	// A SIGINT during a run cancels only that run; the handler is installed for
	// the whole REPL and targets whichever run is active via runCancel. runCancel
	// is read on the signal goroutine and written on the main loop, so a mutex
	// guards it against a data race.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	var (
		mu        sync.Mutex
		runCancel context.CancelFunc
	)
	setCancel := func(c context.CancelFunc) {
		mu.Lock()
		runCancel = c
		mu.Unlock()
	}
	go func() {
		for range sigCh {
			mu.Lock()
			c := runCancel
			mu.Unlock()
			if c != nil {
				c()
			}
		}
	}()

	for {
		fmt.Fprintln(out)
		raw, err := editor.readLine(fmt.Sprintf("pigo(%s)> ", deps.live.model))
		if errors.Is(err, errLineInterrupted) {
			continue
		}
		if err != nil && raw == "" {
			// EOF (Ctrl+D) or read error with no partial line: exit cleanly.
			fmt.Fprintln(out)
			if err == io.EOF {
				return nil
			}
			return err
		}
		line := strings.TrimSpace(raw)
		editor.remember(line)
		if line == "" {
			if err != nil {
				// A trailing partial line at EOF that trims to empty: exit.
				fmt.Fprintln(out)
				return nil
			}
			continue
		}
		if line == "/exit" || line == "/quit" {
			return nil
		}
		if line == "/compact" {
			// /compact is intercepted here (like /exit) because compaction must run
			// an agent stream and mutate the shared context — neither of which a
			// slash Action closure (string in, string out) can do. Compaction
			// replaces the whole message list with a summary + tail, so the session
			// is rewritten linearly (Save) and the branch-tracking state is reset to
			// the new flattened leaf.
			runManualCompact(out, deps)
			deps.header.UpdatedAt = time.Now().UTC()
			if err := deps.store.Save(deps.header, deps.agentCtx.Messages); err != nil {
				fmt.Fprintf(out, "pigo: session save failed: %v\n", err)
			}
			deps.persisted = len(deps.agentCtx.Messages)
			deps.curLeaf = ""
			if _, entries, err := deps.store.LoadEntries(deps.header.ID); err == nil && len(entries) > 0 {
				deps.curLeaf = entries[len(entries)-1].ID
			}
			continue
		}
		if line == "/clone" || line == "/fork" || strings.HasPrefix(line, "/fork ") {
			// /fork and /clone are intercepted here (like /compact) because they
			// switch the active session — replacing the header and the shared
			// context in place — which a slash Action closure (pure string→string)
			// cannot do. runForkClone saves the current session, forks it, and
			// swaps deps.header / deps.agentCtx to the new branch on success.
			runForkClone(out, &deps, line)
			continue
		}
		if line == "/tree" || strings.HasPrefix(line, "/tree ") {
			// /tree is intercepted here (like /fork) because "/tree <n>" moves the
			// active leaf and rebuilds the shared context in place — mutating
			// per-run state a slash Action closure cannot reach. With no argument
			// it just prints the tree.
			runTree(out, &deps, line)
			continue
		}
		if line == "/export" || strings.HasPrefix(line, "/export ") {
			// /export writes the current session to a file. It is intercepted here
			// (not a slash Action) because it must first persist the live turn so the
			// export reflects unsaved messages. The exact-or-space-prefix guard keeps
			// "/exporter" from being mistaken for "/export".
			runExport(out, &deps, line)
			continue
		}
		if line == "/import" || strings.HasPrefix(line, "/import ") {
			// /import loads a JSONL export as a fresh session and switches to it,
			// swapping deps.header / deps.agentCtx in place — which a slash Action
			// closure cannot do. The guard keeps "/important" from matching.
			runImport(out, &deps, line)
			continue
		}
		if line == "/copy" {
			// /copy writes the most recent assistant text to the clipboard. It is
			// intercepted here (not a slash Action) because it must read the live
			// message list, which an Action closure cannot reach.
			runCopy(out, &deps)
			continue
		}
		if line == "/session" {
			// /session prints live session stats (message count, tokens, compactions)
			// derived from deps.header + the in-memory context — state a pure
			// string→string Action closure cannot see.
			runSession(out, &deps)
			continue
		}
		if line == "/goal" || strings.HasPrefix(line, "/goal ") {
			// /goal is intercepted here (like /compact) because it must run one or
			// more agent streams and mutate the shared context/goal state — none of
			// which a slash Action closure (string→string) can do. It drives the
			// autonomous goal loop, reusing the same SIGINT cancel plumbing as a
			// normal turn via setCancel.
			runGoal(setCancel, out, &deps, line)
			continue
		}
		if line == "/btw" || strings.HasPrefix(line, "/btw ") {
			// /btw is intercepted here (like /goal) because it must run an agent
			// stream against a COPY of the main context and must NOT mutate or
			// persist the main conversation — none of which a slash Action closure
			// can express. The exact-or-space-prefix guard keeps "/btweak" from
			// being mistaken for "/btw". It reuses the same SIGINT cancel plumbing
			// as a normal turn via setCancel.
			runBtw(setCancel, out, &deps, line)
			continue
		}

		// Resolve slash-commands: an action command runs and prints its message
		// (no agent run); a prompt command or skill expands to the text we run; a
		// hybrid (plugin) command runs its side effect, prints its message
		// (notifications), then runs the returned prompt if non-empty; an unknown
		// command prints an error and returns to the prompt.
		prompt := line
		if strings.HasPrefix(line, "/") {
			outcome, err := deps.slash.ResolveOutcome(line)
			if err != nil {
				fmt.Fprintf(out, "%v\n", err)
				continue
			}
			if outcome.Kind == runtime.SlashAction {
				if outcome.Message != "" {
					fmt.Fprintln(out, outcome.Message)
				}
				continue
			}
			// SlashPrompt. A hybrid command may carry a Message to surface first
			// (e.g. plugin notifications) alongside the prompt to run.
			if outcome.Message != "" {
				fmt.Fprintln(out, outcome.Message)
			}
			// A hybrid command with no prompt (only notifications) has nothing to
			// run; return to the prompt without starting a turn.
			if outcome.Prompt == "" {
				continue
			}
			prompt = outcome.Prompt
		}

		// Launch the run and stream it to out. runCancel is published so the SIGINT
		// handler can interrupt this run; it is cleared when the run settles.
		runCtx, cancel := context.WithCancel(context.Background())
		setCancel(cancel)
		streamRun(runCtx, out, deps, prompt)
		cancel()
		setCancel(nil)

		// Persist the turn as a branch descending from the active leaf, so a
		// prior /tree leaf-switch produces a real sibling branch on disk rather
		// than a truncated linear rewrite.
		deps.header.Model = deps.live.model
		deps.header.Provider = deps.live.providerName
		persistTurn(out, &deps)
	}
}

// persistTurn writes the messages produced since the last persist as a new
// branch descending from deps.curLeaf, advancing curLeaf to the new leaf and
// persisted to the current message count (US-007, #123). Growing the tree with
// AppendBranch (rather than rewriting the whole file linearly with Save) is what
// lets a /tree leaf-switch fork the on-disk history instead of clobbering it. If
// nothing new was produced it is a no-op: the on-disk tree is already current, so
// the file is left untouched (rewriting it would regenerate ids and flatten the
// tree).
func persistTurn(out io.Writer, deps *replDeps) {
	tail := deps.agentCtx.Messages[deps.persisted:]
	if len(tail) == 0 {
		// Nothing new to persist. Do NOT rewrite the file: Save regenerates entry
		// ids and flattens the tree, which would invalidate curLeaf and drop any
		// sibling branches. The on-disk tree is already current.
		return
	}
	deps.header.UpdatedAt = time.Now().UTC()
	leaf, err := deps.store.AppendBranch(deps.header, deps.curLeaf, tail)
	if err != nil {
		fmt.Fprintf(out, "pigo: session save failed: %v\n", err)
		return
	}
	deps.curLeaf = leaf
	deps.persisted = len(deps.agentCtx.Messages)
}

// streamRun appends the prompt to the shared context, starts an agent run, and
// prints the streamed assistant text and tool activity to out. It blocks until
// the run ends. The context grows in place so the next prompt continues the
// conversation.
func streamRun(ctx context.Context, out io.Writer, deps replDeps, prompt string) {
	content, err := buildUserContent(prompt)
	if err != nil {
		fmt.Fprintf(out, "pigo: %v\n", err)
		return
	}
	deps.agentCtx.Messages = append(deps.agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   content,
	})
	cfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         deps.live.model,
			Provider:      deps.live.providerName,
			ThinkingLevel: deps.live.thinkingLevel,
			Stream:        provider.StreamFnFromProvider(deps.live.provider),
			GetAPIKey:     deps.creds.GetAPIKey,
			ContextWindow: deps.live.contextWindow,
			Compaction:    compaction.DefaultCompactionSettings,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{
				Registry:       deps.reg,
				BeforeToolCall: trustBeforeToolCall(deps.trust, deps.cwd, deps.in, out, deps.confirmMu),
			},
		},
		Reminders: deps.reminders,
	}
	stream := runtime.StartRun(ctx, deps.agentCtx, cfg)

	// The assistant reply is Markdown, which can only be laid out once the whole
	// block is known, so streamed text is buffered here and rendered at turn end
	// (renderMarkdown). On non-terminal output renderMarkdown returns the raw
	// source, so pipes/tests are unchanged. flushReply guarantees the rendered
	// block ends on a fresh line so tool activity below it starts cleanly.
	var reply strings.Builder
	flushReply := func() {
		if reply.Len() == 0 {
			return
		}
		rendered := renderMarkdown(reply.String())
		fmt.Fprint(out, rendered)
		if !strings.HasSuffix(rendered, "\n") {
			fmt.Fprintln(out)
		}
		reply.Reset()
	}
	_, err = runtime.DrainStream(ctx, stream, runtime.StreamHandler{
		OnEvent: deps.notifierHandle(),
		OnText: func(delta string) {
			reply.WriteString(delta)
		},
		OnTurnEnd: func(msg agentcore.AssistantMessage, results []agentcore.ToolResultMessage) {
			flushReply()
			for _, c := range msg.ToolCalls() {
				fmt.Fprintf(out, "  %s %s\n", colorize(colorEnabled(), ansiGreen, "→ tool:"), toolCallLabel(c))
			}
			for _, tr := range results {
				renderToolResult(out, tr)
			}
		},
	})
	// A run can end (error or interrupt) with buffered text from a final turn
	// that never fired OnTurnEnd; flush it so no reply is silently dropped.
	flushReply()
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(out, "^C interrupted")
		} else {
			fmt.Fprintf(out, "error: %v\n", err)
		}
	}
}

// runForkClone handles the /fork and /clone commands (US-006, #122), which
// branch the conversation tree into a new, independent session.
//
//   - "/clone" duplicates the entire current conversation at its current leaf
//     into a fresh session and switches to it. Appending to the clone never
//     affects the original (they are separate files).
//   - "/fork" with no argument lists the historical user messages, numbered, so
//     the user can pick one.
//   - "/fork N" branches from BEFORE the N-th listed user message: the new
//     session holds everything up to (but excluding) that message, so the user
//     re-prompts from that point on an independent branch.
//
// Both first persist the current session (so the fork copies a saved tree),
// then call store.Fork to write the new branch, and finally swap deps.header and
// deps.agentCtx to the new session in place so subsequent prompts continue on
// the branch. resume of either session id later walks to its own leaf.
func runForkClone(out io.Writer, deps *replDeps, line string) {
	// Persist the current turn as a branch so Fork copies an up-to-date tree
	// without flattening any existing branches (a plain Save would rewrite the
	// file linearly and drop siblings).
	persistTurn(out, deps)
	_, entries, err := deps.store.LoadEntries(deps.header.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(out, "pigo: cannot read session tree: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "nothing to fork yet — send a message first")
		return
	}

	fields := strings.Fields(line)
	cmd := fields[0]

	var leafID string
	if cmd == "/clone" {
		// Clone the whole conversation at the current active leaf.
		leafID = deps.curLeaf
		if leafID == "" {
			leafID = entries[len(entries)-1].ID
		}
	} else { // /fork
		// Collect the historical user messages, in order, with their entry index.
		type userMsg struct {
			idx  int // index into entries
			text string
		}
		var users []userMsg
		for i, e := range entries {
			if u, ok := e.Message.(agentcore.UserMessage); ok {
				users = append(users, userMsg{idx: i, text: agentcore.ContentToText(u.Content)})
			}
		}
		if len(users) == 0 {
			fmt.Fprintln(out, "no user messages to fork from")
			return
		}
		if len(fields) < 2 {
			// List the user messages for selection.
			fmt.Fprintln(out, "fork from which message? run /fork <n>:")
			for n, u := range users {
				fmt.Fprintf(out, "  %d. %s\n", n+1, oneLine(u.text))
			}
			return
		}
		n, convErr := strconv.Atoi(fields[1])
		if convErr != nil || n < 1 || n > len(users) {
			fmt.Fprintf(out, "invalid selection %q — run /fork to list messages (1..%d)\n", fields[1], len(users))
			return
		}
		// Fork BEFORE the chosen user message: its parent becomes the new leaf, so
		// the copied branch excludes that message and everything after it.
		leafID = entries[users[n-1].idx].ParentID
	}

	newHeader, path, err := deps.store.Fork(deps.header.ID, leafID, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(out, "pigo: fork failed: %v\n", err)
		return
	}

	// Swap the live session to the new branch: rebuild the flat message list from
	// the copied path and point the REPL at the new header. Subsequent prompts
	// grow the branch, never the original.
	msgs := make(agentcore.MessageList, len(path))
	for i, e := range path {
		msgs[i] = e.Message
	}
	deps.header = newHeader
	deps.agentCtx.Messages = msgs
	// The new file already holds the copied path verbatim, so mark it all
	// persisted and set the active leaf to the copied tip.
	deps.persisted = len(path)
	deps.curLeaf = ""
	if len(path) > 0 {
		deps.curLeaf = path[len(path)-1].ID
	}

	action := "cloned"
	if cmd == "/fork" {
		action = "forked"
	}
	fmt.Fprintf(out, "%s session %s → %s (%d messages)\n", action, newHeader.ParentSession, newHeader.ID, len(msgs))
}

// runTree handles the /tree command (US-007, #123): the branch-navigation view
// for a pure line REPL.
//
//   - "/tree" with no argument persists the current turn, then prints the
//     session's entry tree with ├─/└─ connectors, tagging the active leaf with
//     "← current". Each printed row is numbered so it can be selected.
//   - "/tree N" switches the active leaf to the N-th printed entry: it rebuilds
//     the shared context to the root→leaf path feeding that entry, so the next
//     prompt continues from there. Because persistTurn appends new turns as a
//     branch descending from the (moved) leaf, continuing after a switch grows a
//     real sibling branch on disk rather than truncating history.
func runTree(out io.Writer, deps *replDeps, line string) {
	// Persist any un-saved turn first so the tree reflects the live conversation.
	persistTurn(out, deps)
	_, entries, err := deps.store.LoadEntries(deps.header.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(out, "pigo: cannot read session tree: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "session tree is empty — send a message first")
		return
	}

	lines := session.RenderTreeLines(entries, deps.curLeaf)
	fields := strings.Fields(line)
	if len(fields) < 2 {
		// Print the numbered tree for selection.
		fmt.Fprintln(out, "session tree (run /tree <n> to switch the active branch):")
		for i, l := range lines {
			fmt.Fprintf(out, "  %d. %s\n", i+1, l.Text)
		}
		return
	}

	n, convErr := strconv.Atoi(fields[1])
	if convErr != nil || n < 1 || n > len(lines) {
		fmt.Fprintf(out, "invalid selection %q — run /tree to list nodes (1..%d)\n", fields[1], len(lines))
		return
	}

	// Switch the active leaf to the chosen node and rebuild the context from its
	// root→leaf path. The chosen entry is already persisted, so persisted stays at
	// the rebuilt length; the next turn branches from curLeaf.
	target := lines[n-1].Entry
	path := session.PathToLeaf(entries, target.ID)
	msgs := make(agentcore.MessageList, len(path))
	for i, e := range path {
		msgs[i] = e.Message
	}
	deps.agentCtx.Messages = msgs
	deps.curLeaf = target.ID
	deps.persisted = len(msgs)
	fmt.Fprintf(out, "switched to branch at node %d (%d messages) — next prompt continues from here\n", n, len(msgs))
}

// pathCommandArg extracts the path argument for a "/cmd <path>" line, enforcing a
// command-token boundary (so "/exporter x" is NOT "/export" with arg "x") and
// stripping a single layer of surrounding double quotes (so a path with spaces
// can be given as /export "my session.html"). It returns "" when line is not the
// given command or carries no argument.
func pathCommandArg(line, cmd string) string {
	if line != cmd && !strings.HasPrefix(line, cmd+" ") {
		return ""
	}
	arg := strings.TrimSpace(strings.TrimPrefix(line, cmd))
	if len(arg) >= 2 && strings.HasPrefix(arg, `"`) && strings.HasSuffix(arg, `"`) {
		arg = arg[1 : len(arg)-1]
	}
	return arg
}

// runExport handles the /export command (US-008, #124): it persists the live
// turn, then writes the current session to a file. "/export" with no path
// defaults to "<session-id>.jsonl" in the working directory; "/export path.html"
// (or .htm) writes a self-contained HTML transcript; any other extension writes
// JSONL. The JSONL form round-trips losslessly through /import.
func runExport(out io.Writer, deps *replDeps, line string) {
	persistTurn(out, deps)
	path := pathCommandArg(line, "/export")
	if path == "" {
		path = deps.header.ID + ".jsonl"
	}
	n, err := deps.store.Export(deps.header.ID, path)
	if err != nil {
		fmt.Fprintf(out, "pigo: export failed: %v\n", err)
		return
	}
	fmt.Fprintf(out, "exported %d entries to %s\n", n, path)
}

// runImport handles the /import command (US-008, #124): it reads a JSONL export
// and materializes it as a fresh, independent session, then switches the live
// REPL to it (swapping header + shared context in place) so the next prompt
// continues the imported conversation. The original file is untouched; the new
// session records the source id as its ParentSession.
func runImport(out io.Writer, deps *replDeps, line string) {
	path := pathCommandArg(line, "/import")
	if path == "" {
		fmt.Fprintln(out, "usage: /import <path.jsonl>")
		return
	}
	newHeader, entries, err := deps.store.Import(path, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(out, "pigo: import failed: %v\n", err)
		return
	}
	// Swap the live session to the imported one: rebuild the flat message list and
	// point the REPL at the new header. The imported file already holds the entries
	// verbatim, so mark them all persisted and set the active leaf to the tip.
	msgs := make(agentcore.MessageList, len(entries))
	for i, e := range entries {
		msgs[i] = e.Message
	}
	deps.header = newHeader
	deps.agentCtx.Messages = msgs
	deps.persisted = len(entries)
	deps.curLeaf = ""
	if len(entries) > 0 {
		deps.curLeaf = entries[len(entries)-1].ID
	}
	fmt.Fprintf(out, "imported %d entries from %s → session %s\n", len(entries), path, newHeader.ID)
}

// runCopy handles the /copy command (US-009, #125): it copies the most recent
// assistant text message to the system clipboard. When no clipboard utility is
// available it degrades to printing the text with a notice, so the content is
// never lost. An empty conversation (no assistant reply yet) is reported.
func runCopy(out io.Writer, deps *replDeps) {
	text := ""
	for i := len(deps.agentCtx.Messages) - 1; i >= 0; i-- {
		if a, ok := deps.agentCtx.Messages[i].(agentcore.AssistantMessage); ok {
			if t := strings.TrimSpace(agentcore.ContentToText(a.Content)); t != "" {
				text = t
				break
			}
		}
	}
	if text == "" {
		fmt.Fprintln(out, "nothing to copy — no assistant reply yet")
		return
	}
	if err := clipboard.Copy(text); err != nil {
		if errors.Is(err, clipboard.ErrUnavailable) {
			// Degrade gracefully: print the text and tell the user why it was not
			// copied, so the content is still recoverable.
			fmt.Fprintln(out, "no clipboard utility found (install pbcopy/wl-copy/xclip/xsel); printing instead:")
			fmt.Fprintln(out, text)
			return
		}
		fmt.Fprintf(out, "pigo: copy failed: %v\n", err)
		return
	}
	fmt.Fprintf(out, "copied last reply to clipboard (%d chars)\n", len(text))
}

// runSession handles the /session command (US-009, #125): it prints a summary of
// the live session — id, message count, estimated token usage, model/provider,
// creation time, and how many compaction checkpoints it contains. Counts are
// derived from the in-memory context (the source of truth for the live turn) so
// the numbers reflect unsaved messages too.
func runSession(out io.Writer, deps *replDeps) {
	msgs := deps.agentCtx.Messages
	tokens := compaction.EstimateContextTokens(msgs).Tokens
	compactions := 0
	for _, m := range msgs {
		if _, ok := m.(agentcore.CompactionMessage); ok {
			compactions++
		}
	}
	fmt.Fprintf(out, "session:      %s\n", deps.header.ID)
	fmt.Fprintf(out, "messages:     %d\n", len(msgs))
	fmt.Fprintf(out, "tokens (est): %d\n", tokens)
	model := deps.live.model
	providerName := deps.live.providerName
	if model == "" {
		model = deps.header.Model
	}
	if providerName == "" {
		providerName = deps.header.Provider
	}
	fmt.Fprintf(out, "model:        %s (provider: %s)\n", model, providerName)
	if !deps.header.CreatedAt.IsZero() {
		fmt.Fprintf(out, "created:      %s\n", deps.header.CreatedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "compactions:  %d\n", compactions)
}

// runManualCompact compacts the shared context on an explicit /compact request:
// it runs the summarization stream, replaces the context with the checkpoint +
// retained tail, and prints the before/after token counts and retained message
// count. A failure is reported but non-fatal — the original context is kept
// unchanged (US-004). It uses the same provider/model as the live run.
func runManualCompact(out io.Writer, deps replDeps) {
	msgs := deps.agentCtx.Messages
	settings := compaction.DefaultCompactionSettings
	before := compaction.EstimateContextTokens(msgs).Tokens

	stream := provider.StreamFnFromProvider(deps.live.provider)
	model := provider.Model{Provider: deps.live.providerName, ID: deps.live.model, ContextWindow: deps.live.contextWindow}

	// Resolve the API key like a normal turn so summarization authenticates
	// against auth-requiring providers (otherwise Compact fails with
	// "missing API key" and /compact would always report a non-fatal failure).
	scfg := provider.StreamConfig{}
	if deps.creds != nil {
		scfg.APIKey = deps.creds.GetAPIKey(context.Background(), deps.live.providerName)
	}
	res, err := compaction.Compact(context.Background(), stream, model, msgs, settings, -1, nil, "", scfg)
	if err != nil {
		fmt.Fprintf(out, "compaction failed: %v (context left unchanged)\n", err)
		return
	}
	if res == nil {
		fmt.Fprintf(out, "nothing to compact (%d tokens, %d messages)\n", before, len(msgs))
		return
	}
	now := time.Now().UnixMilli()
	rebuilt := res.RebuildContext(msgs, now)
	deps.agentCtx.Messages = rebuilt
	after := compaction.EstimateContextTokens(rebuilt).Tokens
	summarized := len(msgs) - (len(rebuilt) - 1)
	fmt.Fprintf(out, "compacted: %d → %d tokens, summarized %d messages, kept %d\n",
		before, after, summarized, len(rebuilt)-1)
}

// replayTranscript prints a resumed session's prior messages to out so the user
// sees the conversation so far before the first new prompt.
func replayTranscript(out io.Writer, messages []agentcore.AgentMessage) {
	color := colorEnabled()
	for _, m := range messages {
		switch msg := m.(type) {
		case agentcore.UserMessage:
			if t := agentcore.ContentToText(msg.Content); t != "" {
				fmt.Fprintf(out, "> %s\n", t)
			}
		case agentcore.AssistantMessage:
			if t := agentcore.ContentToText(msg.Content); t != "" {
				fmt.Fprintln(out, t)
			}
			for _, c := range msg.ToolCalls() {
				fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiGreen, "→ tool:"), toolCallLabel(c))
			}
		case agentcore.ToolResultMessage:
			if msg.IsError {
				fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiRed, "← error:"), oneLine(agentcore.ContentToText(msg.Content)))
			} else {
				fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiGreen, "← result:"), oneLine(agentcore.ContentToText(msg.Content)))
			}
		}
	}
}

// renderToolResult prints a single tool result under the compact status. Most
// results collapse to a one-line "← result:" summary, but the todo tool's
// progress block is rendered in full (indented, multi-line) so the user sees the
// live task checklist after each update — that visible progress is the point of
// the tool (US-011).
func renderToolResult(out io.Writer, tr agentcore.ToolResultMessage) {
	text := agentcore.ContentToText(tr.Content)
	color := colorEnabled()
	if tr.ToolName == "todo" && !tr.IsError {
		fmt.Fprintln(out, "  "+colorize(color, ansiGreen, "← todo:"))
		for _, line := range strings.Split(text, "\n") {
			fmt.Fprintf(out, "    %s\n", line)
		}
		return
	}
	// Success results show a green "← result:" label; failures show a red
	// "← error:" label so the outcome is scannable at a glance.
	if tr.IsError {
		fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiRed, "← error:"), oneLine(text))
		return
	}
	fmt.Fprintf(out, "  %s %s\n", colorize(color, ansiGreen, "← result:"), oneLine(text))
}

// toolCallLabel renders a tool call as "name args" for the compact "→ tool:"
// status, so the user can see what a tool was actually invoked with (e.g. the
// shell command bash ran). Empty or "{}" arguments collapse to just the name.
func toolCallLabel(c agentcore.ToolCallContent) string {
	args := strings.TrimSpace(string(c.Arguments))
	if args == "" || args == "{}" {
		return c.Name
	}
	return c.Name + " " + oneLine(args)
}

// oneLine collapses a possibly multi-line tool result into a single trimmed
// line for the compact "← result:" status, truncating very long results.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	const max = 120
	if len(s) > max {
		s = s[:max] + " …"
	}
	return s
}
