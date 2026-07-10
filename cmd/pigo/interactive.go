// This file wires the interactive TUI (US-022) and session persistence
// (US-024, #43) into the pigo command. When invoked without a prompt on a
// terminal, pigo starts the bubbletea interactive loop; each run's messages are
// persisted to a local JSONL session so the conversation can be listed, resumed
// and replayed later.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agent"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/tui"
)

// sessionStore returns the session store rooted at ~/.pigo/sessions (or under
// PIGO_HOME when set), creating the directory on first use.
func sessionStore() (*session.Store, error) {
	dir := os.Getenv("PIGO_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		dir = filepath.Join(home, ".pigo")
	}
	return session.NewStore(filepath.Join(dir, "sessions"))
}

// interactiveOptions carries the resolved run configuration plus optional
// resume state into runInteractive.
type interactiveOptions struct {
	model        string
	providerName string
	provider     agent.Provider
	tools        []agent.AgentTool
	sysPrompt    string

	// resumeID, when non-empty, resumes an existing session: its messages seed
	// the context and TUI transcript. Otherwise a fresh session is created.
	resumeID string
}

// runInteractive starts the bubbletea TUI over a persisted session. It keeps a
// single growing AgentContext across prompts (so turns share history) and saves
// the session's messages after each run completes.
func runInteractive(opts interactiveOptions) error {
	creds := agent.NewCredentialStore(nil)
	reg := toolRegistry(opts.tools)

	store, err := sessionStore()
	if err != nil {
		return err
	}

	// Establish the session: resume an existing one or create a fresh header.
	now := time.Now().UTC()
	var (
		agentCtx *agent.AgentContext
		header   session.SessionHeader
		history  []agent.AgentMessage
	)
	if opts.resumeID != "" {
		// Interactive resume differs from headless continue: the user re-prompts,
		// so a new user message is always appended before the loop runs. A session
		// that ended normally (trailing assistant reply) is therefore resumable
		// here, unlike store.Resume (which guards agentLoopContinue). So load the
		// raw session and rebuild the context directly.
		h, msgs, err := store.Load(opts.resumeID)
		if err != nil {
			return err
		}
		header = h
		agentCtx = &agent.AgentContext{SystemPrompt: h.SystemPrompt, Messages: msgs, Tools: opts.tools}
		history = msgs
		if agentCtx.SystemPrompt == "" {
			agentCtx.SystemPrompt = opts.sysPrompt
		}
	} else {
		agentCtx = &agent.AgentContext{SystemPrompt: opts.sysPrompt, Tools: opts.tools}
		header = session.SessionHeader{
			ID:           session.NewID(now),
			CreatedAt:    now,
			UpdatedAt:    now,
			Model:        opts.model,
			Provider:     opts.providerName,
			SystemPrompt: opts.sysPrompt,
		}
	}

	// run appends the new prompt to the shared context, launches a loop run over
	// the whole context, and returns its event stream. Steering is bridged into
	// the loop's per-turn hook. The context grows in place as the loop appends
	// assistant/tool messages, so the next prompt continues the conversation.
	run := func(ctx context.Context, prompt string, steering func() []string) (*agent.LoopEventStream, context.CancelFunc) {
		runCtx, cancel := context.WithCancel(ctx)
		agentCtx.Messages = append(agentCtx.Messages, agent.UserMessage{
			RoleField: agent.RoleUser,
			Content:   agent.ContentList{agent.NewTextContent(prompt)},
		})
		cfg := agent.RunConfig{
			LoopConfig: agent.LoopConfig{
				Model:     opts.model,
				Provider:  opts.providerName,
				Stream:    agent.StreamFnFromProvider(opts.provider),
				GetAPIKey: creds.GetAPIKey,
			},
			Batch: agent.BatchConfig{
				ToolExecutorConfig: agent.ToolExecutorConfig{Registry: reg},
			},
			GetSteeringMessages: func(context.Context) []agent.AgentMessage {
				texts := steering()
				if len(texts) == 0 {
					return nil
				}
				msgs := make([]agent.AgentMessage, 0, len(texts))
				for _, t := range texts {
					msgs = append(msgs, agent.UserMessage{RoleField: agent.RoleUser, Content: agent.ContentList{agent.NewTextContent(t)}})
				}
				return msgs
			},
		}
		stream := agent.StartRun(runCtx, agentCtx, cfg)
		// Persist the session once this run settles. Result blocks until the loop
		// ends; calling it here (in addition to the TUI's drain) is safe — Result
		// resolves once and can be read by multiple goroutines.
		go func() {
			_, _ = stream.Result(context.Background())
			header.UpdatedAt = time.Now().UTC()
			if err := store.Save(header, agentCtx.Messages); err != nil {
				fmt.Fprintf(os.Stderr, "pigo: session save failed: %v\n", err)
			}
		}()
		return stream, cancel
	}

	var m *tui.Model
	if len(history) > 0 {
		m = tui.NewModelWithHistory(run, history)
	} else {
		m = tui.NewModel(run)
	}
	p := tea.NewProgram(m)
	m.SetProgram(p)
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// printSessions prints the stored sessions, most-recent first, to stdout.
func printSessions() error {
	store, err := sessionStore()
	if err != nil {
		return err
	}
	headers, err := store.List()
	if err != nil {
		return err
	}
	if len(headers) == 0 {
		fmt.Println("no sessions")
		return nil
	}
	for _, h := range headers {
		fmt.Printf("%s\t%s\t%s\n", h.ID, h.UpdatedAt.Local().Format("2006-01-02 15:04"), h.Model)
	}
	return nil
}

// mostRecentSessionID returns the id of the most recently updated session, or
// "" if there are none.
func mostRecentSessionID() (string, error) {
	store, err := sessionStore()
	if err != nil {
		return "", err
	}
	headers, err := store.List()
	if err != nil {
		return "", err
	}
	if len(headers) == 0 {
		return "", nil
	}
	return headers[0].ID, nil
}

// stdoutIsTerminal reports whether stdout is an interactive terminal (not a
// pipe/file), used to decide print vs interactive mode.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
