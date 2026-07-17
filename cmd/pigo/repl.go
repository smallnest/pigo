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
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
)

// replDeps bundles the collaborators a REPL run needs. They are assembled once
// by runInteractive and reused across every prompt in the session.
type replDeps struct {
	store    *session.Store
	header   session.SessionHeader
	agentCtx *agentcore.AgentContext
	live     *liveRunConfig
	reg      *agenttool.ToolRegistry
	slash    *runtime.SlashRegistry
	creds    *provider.CredentialStore

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
}

// replScanBufInit / replScanBufMax bound the line scanner. bufio.Scanner caps
// lines at 64KiB by default; a REPL user may paste a long single line (a big
// prompt or a pasted file), so the max is raised to 4MiB. The initial buffer
// stays at 64KiB and grows on demand.
const (
	replScanBufInit = 64 * 1024
	replScanBufMax  = 4 * 1024 * 1024
)

// runREPL runs the read → run → stream-print loop until EOF, /exit or /quit. It
// reads from in (os.Stdin in production) and writes prompts, streamed replies
// and status lines to out (os.Stdout). It is the interactive replacement for the
// bubbletea program.
func runREPL(in io.Reader, out io.Writer, deps replDeps) error {
	scanner := bufio.NewScanner(in)
	// Allow long pasted lines (default bufio.Scanner caps at 64KiB).
	scanner.Buffer(make([]byte, 0, replScanBufInit), replScanBufMax)

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
		fmt.Fprintf(out, "\npigo(%s)> ", deps.live.model)
		if !scanner.Scan() {
			// EOF (Ctrl+D) or read error: exit cleanly.
			fmt.Fprintln(out)
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
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

		// Resolve slash-commands: an action command runs and prints its message
		// (no agent run); a prompt command or skill expands to the text we run; an
		// unknown command prints an error and returns to the prompt.
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
	deps.agentCtx.Messages = append(deps.agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(prompt)},
	})
	cfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         deps.live.model,
			Provider:      deps.live.providerName,
			Stream:        provider.StreamFnFromProvider(deps.live.provider),
			GetAPIKey:     deps.creds.GetAPIKey,
			ContextWindow: deps.live.contextWindow,
			Compaction:    compaction.DefaultCompactionSettings,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: deps.reg},
		},
	}
	stream := runtime.StartRun(ctx, deps.agentCtx, cfg)

	// atLineStart tracks whether the cursor sits at the start of a line, so the
	// turn-end flush adds a newline only after streamed text (and tool activity
	// is surfaced on its own lines below the reply).
	atLineStart := true
	_, err := runtime.DrainStream(ctx, stream, runtime.StreamHandler{
		OnText: func(delta string) {
			fmt.Fprint(out, delta)
			if delta != "" {
				atLineStart = strings.HasSuffix(delta, "\n")
			}
		},
		OnTurnEnd: func(msg agentcore.AssistantMessage, results []agentcore.ToolResultMessage) {
			if !atLineStart {
				fmt.Fprintln(out)
				atLineStart = true
			}
			for _, c := range msg.ToolCalls() {
				fmt.Fprintf(out, "  → tool: %s\n", toolCallLabel(c))
			}
			for _, tr := range results {
				fmt.Fprintf(out, "  ← result: %s\n", oneLine(agentcore.ContentToText(tr.Content)))
			}
		},
	})
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
				fmt.Fprintf(out, "  → tool: %s\n", toolCallLabel(c))
			}
		case agentcore.ToolResultMessage:
			fmt.Fprintf(out, "  ← result: %s\n", oneLine(agentcore.ContentToText(msg.Content)))
		}
	}
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
