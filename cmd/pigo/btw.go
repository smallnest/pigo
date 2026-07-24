// This file implements the /btw command (对标 Claude Code's /btw and the pi
// agent extension @narumitw/pi-btw): a throwaway "side thread" for asking the
// model a quick side question that must NOT pollute the main conversation.
//
// /btw is intercepted in the REPL loop (see repl.go) rather than routed through
// a slash Action closure because it must run an agent stream and read the live
// main context — none of which a pure string→string Action can do, exactly like
// /compact and /goal.
//
// Isolation contract (the whole point of the feature): a side thread runs on a
// COPY of the main conversation as background, and its question/answer are only
// ever appended to that copy — never to deps.agentCtx.Messages. Nothing is
// persisted: no store.Save, no change to deps.persisted / deps.curLeaf /
// deps.header.UpdatedAt. Closing the side thread, switching sessions or
// restarting pigo discards everything.
//
// Scope: /btw is intercepted in the REPL loop and runs a side question against a
// copy of the main context (#279); it supports multi-turn follow-ups in the same
// ephemeral thread (#280) and bare-/btw reopen of the most recent side thread
// this process (#281). A model/thinking override config lands in #282.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// btwHeader is the fixed banner shown when entering a side thread, so the user
// always knows the current input is a throwaway side question, not the main
// conversation (对标 pi-btw's "btw · side thread" header).
const btwHeader = "btw · side thread"

// btwPrompt is the input prompt shown for follow-up questions inside a side
// thread, distinguishing it from the main "pigo(model)>" prompt.
const btwPrompt = "btw> "

// runBtw handles a /btw invocation. With an argument it starts a fresh side
// thread, asks that question, then enters a follow-up loop so the user can keep
// asking in the same ephemeral thread. Bare "/btw" reopens the most recent side
// thread from this process — replaying its Q&A history — and drops back into the
// follow-up loop; if none exists yet it guides the user to supply a question
// (US-004, #281). setCancel publishes the active run's cancel func so the REPL's
// SIGINT handler can interrupt the side run, reusing the same plumbing as a
// normal turn.
//
// The main context is never mutated: runBtw builds a private side AgentContext
// seeded with a copy of the main messages, runs every turn against that copy,
// and returns without touching deps.agentCtx or persisting anything. The side
// thread is retained in-process (deps.lastBtw) so a later bare /btw can reopen
// it, but it is never written to disk — restarting pigo discards it.
func runBtw(setCancel func(context.CancelFunc), out io.Writer, deps *replDeps, editor *replLineEditor, line string) {
	question := strings.TrimSpace(strings.TrimPrefix(line, "/btw"))
	if question == "" {
		// Bare /btw: reopen the most recent side thread if one exists this process,
		// replaying its history; otherwise guide the user to supply a question.
		if deps.lastBtw == nil {
			fmt.Fprintln(out, "usage: /btw <question> — ask a quick side question without touching the main conversation")
			return
		}
		printBtwHeader(out)
		replaySideHistory(out, deps.lastBtw, deps.lastBtwBase)
		if editor != nil {
			btwFollowUpLoop(setCancel, out, deps, editor, deps.lastBtw)
		}
		return
	}

	side := newSideContext(deps.agentCtx)
	// Remember this thread so a later bare /btw can reopen it. lastBtwBase marks
	// where the copied background ends and the side Q&A begins, so a reopen only
	// replays the side turns, not the whole main transcript.
	deps.lastBtw = side
	deps.lastBtwBase = len(side.Messages)
	printBtwHeader(out)
	askSide(setCancel, out, deps, side, question)
	// Follow-up loop: keep answering in the same ephemeral thread until the user
	// exits. A nil editor (direct test callers that only ask one question) skips
	// the loop entirely, so a single /btw asks exactly one question and returns.
	if editor != nil {
		btwFollowUpLoop(setCancel, out, deps, editor, side)
	}
}

// replaySideHistory prints the side thread's own Q&A (everything after the
// copied main-conversation background at index base) when a bare /btw reopens a
// prior thread, so the user can browse earlier answers before continuing. Only
// user questions and assistant text are shown; tool activity is omitted to keep
// the recap compact.
func replaySideHistory(out io.Writer, side *agentcore.AgentContext, base int) {
	if base > len(side.Messages) {
		base = len(side.Messages)
	}
	for _, msg := range side.Messages[base:] {
		switch m := msg.(type) {
		case agentcore.UserMessage:
			fmt.Fprintf(out, "%s %s\n", colorize(colorEnabled(), ansiDim, "you:"), agentcore.ContentToText(m.Content))
		case agentcore.AssistantMessage:
			if text := agentcore.ContentToText(m.Content); text != "" {
				rendered := renderMarkdown(text)
				fmt.Fprint(out, rendered)
				if !strings.HasSuffix(rendered, "\n") {
					fmt.Fprintln(out)
				}
			}
		}
	}
}

// btwFollowUpLoop reads follow-up questions and answers them in the same side
// context, so each answer sees the prior side Q&A (FR-4). It exits on /exit,
// /quit, EOF, or an idle Ctrl+C (errLineInterrupted) — the same exit affordances
// as the main REPL, but confined to the side thread (FR-5). A blank line is
// ignored (stays in the thread). Nothing here touches the main context.
func btwFollowUpLoop(setCancel func(context.CancelFunc), out io.Writer, deps *replDeps, editor *replLineEditor, side *agentcore.AgentContext) {
	for {
		raw, err := editor.readLine(btwPrompt)
		if errors.Is(err, errLineInterrupted) {
			// Idle Ctrl+C at the side prompt leaves the thread (a Ctrl+C during a
			// run is handled inside askSide via the SIGINT cancel plumbing).
			fmt.Fprintln(out, "left side thread")
			return
		}
		q := strings.TrimSpace(raw)
		if err != nil && q == "" {
			// EOF or read error with no partial line: leave the thread.
			fmt.Fprintln(out, "left side thread")
			return
		}
		if q == "/exit" || q == "/quit" {
			fmt.Fprintln(out, "left side thread")
			return
		}
		if q == "" {
			continue
		}
		askSide(setCancel, out, deps, side, q)
	}
}

// newSideContext builds the side thread's private AgentContext. Its Messages are
// a fresh slice seeded with a shallow COPY of the main messages (the elements
// are immutable value/interface messages, so a copied slice header is enough to
// guarantee appends to the side thread never reach deps.agentCtx.Messages). The
// system prompt and tools are shared by value; only Messages diverges.
func newSideContext(main *agentcore.AgentContext) *agentcore.AgentContext {
	msgs := make(agentcore.MessageList, len(main.Messages))
	copy(msgs, main.Messages)
	return &agentcore.AgentContext{
		SystemPrompt: main.SystemPrompt,
		Messages:     msgs,
		Tools:        main.Tools,
	}
}

// printBtwHeader prints the side-thread banner.
func printBtwHeader(out io.Writer) {
	fmt.Fprintln(out, colorize(colorEnabled(), ansiDim, btwHeader))
}

// askSide appends the question to the side context and streams one answer,
// mirroring streamRun's rendering but targeting the side context so nothing is
// written back to the main conversation or to disk. It reuses the REPL's SIGINT
// cancel plumbing via setCancel.
func askSide(setCancel func(context.CancelFunc), out io.Writer, deps *replDeps, side *agentcore.AgentContext, question string) {
	content, err := buildUserContent(question)
	if err != nil {
		fmt.Fprintf(out, "pigo: %v\n", err)
		return
	}
	side.Messages = append(side.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   content,
	})

	runCtx, cancel := context.WithCancel(context.Background())
	setCancel(cancel)
	defer func() {
		cancel()
		setCancel(nil)
	}()

	// Show a transient status while the model works (FR-9). It is printed on its
	// own line; the streamed answer follows below it.
	fmt.Fprintln(out, colorize(colorEnabled(), ansiDim, "Answering…"))

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
	stream := runtime.StartRun(runCtx, side, cfg)
	drainSideStream(runCtx, out, deps, stream)
}

// drainSideStream prints the streamed assistant text and tool activity of a side
// run, mirroring streamRun/drainGoalStream. It blocks until the run ends. Unlike
// the main loop it persists nothing.
func drainSideStream(ctx context.Context, out io.Writer, deps *replDeps, stream *runtime.LoopEventStream) {
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
	_, err := runtime.DrainStream(ctx, stream, runtime.StreamHandler{
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
	flushReply()
	if err != nil {
		if ctx.Err() != nil {
			// A Ctrl+C during the run cancels just this answer; the follow-up loop
			// then returns to the btw prompt so the user can ask again or exit with
			// another Ctrl+C (FR-5).
			fmt.Fprintln(out, "^C interrupted — answer cancelled")
		} else {
			fmt.Fprintf(out, "error: %v\n", err)
		}
	}
}
