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
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
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
}

// runREPL runs the read → run → stream-print loop until EOF, /exit or /quit. It
// reads from in (os.Stdin in production) and writes prompts, streamed replies
// and status lines to out (os.Stdout). It is the interactive replacement for the
// bubbletea program.
func runREPL(in io.Reader, out io.Writer, deps replDeps) error {
	scanner := bufio.NewScanner(in)
	// Allow long pasted lines (default bufio.Scanner caps at 64KiB).
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

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

		// Persist the session after each settled run.
		deps.header.UpdatedAt = time.Now().UTC()
		deps.header.Model = deps.live.model
		deps.header.Provider = deps.live.providerName
		if err := deps.store.Save(deps.header, deps.agentCtx.Messages); err != nil {
			fmt.Fprintf(out, "pigo: session save failed: %v\n", err)
		}
	}
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
			Model:     deps.live.model,
			Provider:  deps.live.providerName,
			Stream:    provider.StreamFnFromProvider(deps.live.provider),
			GetAPIKey: deps.creds.GetAPIKey,
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
				fmt.Fprintf(out, "  → tool: %s\n", c.Name)
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
				fmt.Fprintf(out, "  → tool: %s\n", c.Name)
			}
		case agentcore.ToolResultMessage:
			fmt.Fprintf(out, "  ← result: %s\n", oneLine(agentcore.ContentToText(msg.Content)))
		}
	}
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
