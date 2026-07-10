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
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
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
	provider     provider.Provider
	baseURL      string
	tools        []agentcore.AgentTool
	sysPrompt    string

	// resumeID, when non-empty, resumes an existing session: its messages seed
	// the context and TUI transcript. Otherwise a fresh session is created.
	resumeID string
}

// runInteractive starts the bubbletea TUI over a persisted session. It keeps a
// single growing AgentContext across prompts (so turns share history) and saves
// the session's messages after each run completes.
func runInteractive(opts interactiveOptions) error {
	creds := provider.NewCredentialStore(nil)
	reg := toolRegistry(opts.tools)

	store, err := sessionStore()
	if err != nil {
		return err
	}

	// Establish the session: resume an existing one or create a fresh header.
	now := time.Now().UTC()
	var (
		agentCtx *agentcore.AgentContext
		header   session.SessionHeader
		history  []agentcore.AgentMessage
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
		agentCtx = &agentcore.AgentContext{SystemPrompt: h.SystemPrompt, Messages: msgs, Tools: opts.tools}
		history = msgs
		if agentCtx.SystemPrompt == "" {
			agentCtx.SystemPrompt = opts.sysPrompt
		}
	} else {
		agentCtx = &agentcore.AgentContext{SystemPrompt: opts.sysPrompt, Tools: opts.tools}
		header = session.SessionHeader{
			ID:           session.NewID(now),
			CreatedAt:    now,
			UpdatedAt:    now,
			Model:        opts.model,
			Provider:     opts.providerName,
			SystemPrompt: opts.sysPrompt,
		}
	}

	// live holds the run configuration that a control command (e.g. /model) may
	// mutate mid-session. The run closure reads it on each prompt so a model
	// switch takes effect on the next turn; header is updated so the switch is
	// persisted with the session.
	live := &liveRunConfig{
		model:        opts.model,
		providerName: opts.providerName,
		provider:     opts.provider,
		baseURL:      opts.baseURL,
	}

	// run appends the new prompt to the shared context, launches a loop run over
	// the whole context, and returns its event stream. Steering is bridged into
	// the loop's per-turn hook. The context grows in place as the loop appends
	// assistant/tool messages, so the next prompt continues the conversation.
	run := func(ctx context.Context, prompt string, steering func() []string) (*runtime.LoopEventStream, context.CancelFunc) {
		runCtx, cancel := context.WithCancel(ctx)
		agentCtx.Messages = append(agentCtx.Messages, agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(prompt)},
		})
		cfg := runtime.RunConfig{
			LoopConfig: runtime.LoopConfig{
				Model:     live.model,
				Provider:  live.providerName,
				Stream:    provider.StreamFnFromProvider(live.provider),
				GetAPIKey: creds.GetAPIKey,
			},
			Batch: agenttool.BatchConfig{
				ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg},
			},
			GetSteeringMessages: func(context.Context) []agentcore.AgentMessage {
				texts := steering()
				if len(texts) == 0 {
					return nil
				}
				msgs := make([]agentcore.AgentMessage, 0, len(texts))
				for _, t := range texts {
					msgs = append(msgs, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(t)}})
				}
				return msgs
			},
		}
		stream := runtime.StartRun(runCtx, agentCtx, cfg)
		// Persist the session once this run settles. Result blocks until the loop
		// ends; calling it here (in addition to the TUI's drain) is safe — Result
		// resolves once and can be read by multiple goroutines.
		go func() {
			_, _ = stream.Result(context.Background())
			header.UpdatedAt = time.Now().UTC()
			header.Model = live.model
			header.Provider = live.providerName
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
	// Wire slash-commands: built-ins (compile-time) plus any user templates under
	// ~/.pigo/commands (对标 the commands/*.md convention). A load error is
	// non-fatal — the TUI still runs with the built-ins. Instance built-ins that
	// need live state (/model, /help) are registered against `live`.
	if slash, err := buildSlashRegistry(live); err == nil {
		m.SetSlashRegistry(slash)
	} else {
		fmt.Fprintf(os.Stderr, "pigo: slash-commands: %v\n", err)
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

// buildSlashRegistry assembles the TUI slash-command registry: compile-time
// built-ins seeded by runtime.NewSlashRegistry, the live-state action commands
// (/model, /help) bound to live, plus user declarative templates loaded from
// ~/.pigo/commands (or $PIGO_HOME/commands). A missing directory is not an
// error. User commands that collide with a built-in are shadowed (the built-in
// wins) and reported on stderr.
func buildSlashRegistry(live *liveRunConfig) (*runtime.SlashRegistry, error) {
	reg := runtime.NewSlashRegistry()
	registerLiveCommands(reg, live)
	dir := os.Getenv("PIGO_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return reg, nil // built-ins only
		}
		dir = filepath.Join(home, ".pigo")
	}
	cmds, err := runtime.LoadUserCommandsDir(filepath.Join(dir, "commands"))
	if err != nil {
		return reg, err
	}
	for _, c := range cmds {
		reg.AddUser(c)
	}
	if shadowed := reg.Shadowed(); len(shadowed) > 0 {
		fmt.Fprintf(os.Stderr, "pigo: user commands shadowed by built-ins (rename to use): %v\n", shadowed)
	}
	return reg, nil
}

// liveRunConfig is the mutable run configuration a control command may change
// mid-session. The run closure reads it on every prompt, so a /model switch
// takes effect on the next turn. It carries no lock: it is read and written
// only on bubbletea's single Update goroutine (slash actions run there, and the
// run closure is invoked synchronously from that goroutine's startRun).
type liveRunConfig struct {
	model        string
	providerName string
	provider     provider.Provider
	baseURL      string
}

// registerLiveCommands installs the built-in action commands that need live
// runtime state. /model views or switches the active model; /help lists the
// available commands. These are instance built-ins (AddBuiltin) because their
// closures must capture live and the registry — state unreachable from an
// init()-time global registration.
func registerLiveCommands(reg *runtime.SlashRegistry, live *liveRunConfig) {
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "model",
		Description: "view or switch the active model: /model [model-id] (see /models for presets)",
		Action: func(args string) string {
			id := strings.TrimSpace(args)
			if id == "" {
				return fmt.Sprintf("model: %s (provider: %s)\nrun /models to see presets, or /model <id> to switch", live.model, live.providerName)
			}
			prov, providerName, err := resolveProvider(id, live.baseURL)
			if err != nil {
				return fmt.Sprintf("model: cannot switch to %q: %v", id, err)
			}
			live.model = id
			live.providerName = providerName
			live.provider = prov
			return fmt.Sprintf("model switched to %s (provider: %s)", id, providerName)
		},
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "models",
		Description: "list preset providers and models you can switch to",
		Action:      func(args string) string { return presetListing(strings.TrimSpace(args)) },
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "help",
		Description: "list available slash commands",
		Action: func(string) string {
			var b strings.Builder
			b.WriteString("available commands:")
			for _, c := range reg.List() {
				b.WriteString("\n  /")
				b.WriteString(c.Name)
				if c.Description != "" {
					b.WriteString(" — ")
					b.WriteString(c.Description)
				}
			}
			return b.String()
		},
	})
}

// presetListing renders the preset provider/model catalog for /models. With an
// argument it filters to a single provider (e.g. "/models nvidia"). Providers
// are grouped and shown with the env var their API key is read from (referenced
// by name only, never a value). The output guides the user to `/model <id>`.
func presetListing(filter string) string {
	var b strings.Builder
	b.WriteString("preset providers & models (switch with /model <id>):")
	shown := 0
	for _, pv := range provider.PresetProviders {
		if filter != "" && !strings.EqualFold(filter, pv.Name) {
			continue
		}
		models := provider.PresetsByProvider(pv.Name)
		if len(models) == 0 {
			continue
		}
		shown++
		b.WriteString("\n\n")
		b.WriteString(pv.Name)
		if pv.EnvVar != "" {
			b.WriteString(" (API key: $")
			b.WriteString(pv.EnvVar)
			b.WriteString(")")
		} else {
			b.WriteString(" (local, no API key)")
		}
		for _, m := range models {
			b.WriteString("\n  ")
			b.WriteString(m.ID)
			if m.DisplayName != "" {
				b.WriteString("  — ")
				b.WriteString(m.DisplayName)
			}
		}
	}
	if shown == 0 {
		if filter != "" {
			return fmt.Sprintf("no preset provider named %q (try openrouter, nvidia, or ollama)", filter)
		}
		return "no presets configured"
	}
	return b.String()
}
