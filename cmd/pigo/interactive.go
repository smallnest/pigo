// This file wires the line-based REPL (US-003) and session persistence
// (US-024, #43) into the pigo command. When invoked without a prompt on a
// terminal, pigo starts the REPL loop (see repl.go); each run's messages are
// persisted to a local JSONL session so the conversation can be listed, resumed
// and replayed later.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/builtinskills"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
	"github.com/smallnest/pigo/internal/trust"
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
	apiKey       string
	protocol     string
	// thinkingLevel is the resolved reasoning-effort level (US-023): it seeds the
	// live run config so every REPL turn requests it, until a control command
	// changes it.
	thinkingLevel agentcore.ThinkingLevel
	tools         []agentcore.AgentTool
	sysPrompt     string

	// resumeID, when non-empty, resumes an existing session: its messages seed
	// the context and replayed transcript. Otherwise a fresh session is created.
	resumeID string

	// approve, when true, grants the launch directory session trust before the
	// run so the first-launch trust prompt is skipped and side-effect tools run
	// without per-call confirmation (对标 pi 的 --approve/-a).
	approve bool
	// noSkills, when true, skips skill discovery so no /skill-name commands are
	// registered from ~/.agents/skills (对标 pi 的 --no-skills).
	noSkills bool

	// plugins holds the loaded plugin manager so the REPL can deliver lifecycle
	// events to subscribed plugins (US-017, #133). It may be nil (no plugins).
	plugins *plugin.Manager
}

// runInteractive starts the line-based REPL over a persisted session. It keeps
// a single growing AgentContext across prompts (so turns share history) and
// saves the session's messages after each run completes (see runREPL/streamRun
// in repl.go).
func runInteractive(opts interactiveOptions) error {
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(opts.providerName, opts.apiKey)
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
		curLeaf  string // active leaf id on resume; "" for a fresh session
	)
	if opts.resumeID != "" {
		// Interactive resume always appends a fresh user message before running,
		// so a session that ended normally (trailing assistant reply) is resumable
		// here. Load the raw session and rebuild the context directly.
		h, entries, err := store.LoadEntries(opts.resumeID)
		if err != nil {
			return err
		}
		msgs := make(agentcore.MessageList, len(entries))
		for i, e := range entries {
			msgs[i] = e.Message
		}
		if len(entries) > 0 {
			curLeaf = entries[len(entries)-1].ID
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
	// mutate mid-session. streamRun reads it on each prompt so a model switch
	// takes effect on the next turn; header is updated so the switch is persisted
	// with the session.
	live := &liveRunConfig{
		model:         opts.model,
		providerName:  opts.providerName,
		provider:      opts.provider,
		baseURL:       opts.baseURL,
		protocol:      opts.protocol,
		thinkingLevel: opts.thinkingLevel,
		contextWindow: defaultContextWindow,
	}

	// Project trust (US-018, #134): load the persisted trust store for the
	// launch directory. A load failure (e.g. a corrupted trust.json) is
	// non-fatal: trust is disabled (mgr stays nil) and the REPL still runs -
	// the store is surfaced rather than silently overwritten. cwd is captured
	// once since pigo does not cd during a session; if it cannot be resolved
	// trust is disabled too, since an empty cwd would silently never match.
	cwd, cwdErr := os.Getwd()
	mgr, mgrErr := trust.NewManager(trust.DefaultPath())
	if mgrErr != nil {
		fmt.Fprintf(os.Stderr, "pigo: trust store unavailable, trust disabled: %v\n", mgrErr)
		mgr = nil
	}
	if cwdErr != nil && mgr != nil {
		fmt.Fprintf(os.Stderr, "pigo: cannot resolve working directory, trust disabled: %v\n", cwdErr)
		mgr = nil
	}
	// in is the shared input reader for the main loop and the tool-call
	// confirmation prompt (see repl.go). Wrapping os.Stdin once here means both
	// read from the same buffer.
	reader := bufio.NewReaderSize(os.Stdin, replScanBufInit)

	// Wire slash-commands: built-ins (compile-time) plus any user templates under
	// ~/.pigo/commands (对标 the commands/*.md convention) plus skills under
	// ~/.agents/skills. A load error is non-fatal — the REPL still runs with the
	// built-ins. Instance built-ins that need live state (/model, /help) are
	// registered against `live`.
	slash, err := buildSlashRegistry(live, opts.noSkills, opts.plugins)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: slash-commands: %v\n", err)
	}
	registerTrustCommand(slash, mgr, cwd)

	// --approve grants the launch directory session trust up front (对标 pi 的
	// --approve/-a), so the first-launch prompt is skipped and side-effect tools
	// run without per-call confirmation. Otherwise, on the first launch in an
	// undecided directory, ask the user how much to trust it before any tool
	// runs. This happens before replay so the trust question is the first thing
	// the user sees, not their prior history.
	establishTrust(os.Stdout, reader, mgr, cwd, opts.approve)

	// Replay the resumed conversation so the user sees history before re-prompting.
	if len(history) > 0 {
		replayTranscript(os.Stdout, history)
	}

	return runREPL(os.Stdin, os.Stdout, replDeps{
		store:     store,
		header:    header,
		agentCtx:  agentCtx,
		live:      live,
		reg:       reg,
		reminders: todoReminders(opts.tools),
		slash:     slash,
		creds:     creds,
		trust:     mgr,
		cwd:       cwd,
		in:        reader,
		confirmMu: &sync.Mutex{},
		curLeaf:   curLeaf,
		persisted: len(history),
		notifier:  plugin.NewEventNotifier(opts.plugins, os.Stderr),
		goal:      agenttool.NewGoalState(),
	})
}

// printSessions prints the stored sessions, most-recent first, to out.
func printSessions(out io.Writer) error {
	store, err := sessionStore()
	if err != nil {
		return err
	}
	headers, err := store.List()
	if err != nil {
		return err
	}
	if len(headers) == 0 {
		fmt.Fprintln(out, "no sessions")
		return nil
	}
	for _, h := range headers {
		fmt.Fprintf(out, "%s\t%s\t%s\n", h.ID, h.UpdatedAt.Local().Format("2006-01-02 15:04"), h.Model)
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

// buildSlashRegistry assembles the REPL slash-command registry: compile-time
// built-ins seeded by runtime.NewSlashRegistry, the live-state action commands
// (/model, /help) bound to live, user declarative templates loaded from
// ~/.pigo/commands (or $PIGO_HOME/commands), plugin-declared commands from the
// loaded Manager, plus skills loaded from ~/.agents/skills — each surfaced as a
// "/skill-name" command (对标 Claude Code's /skill invocation). A missing
// directory is not an error. Names that collide with a built-in are shadowed
// (the built-in wins) and reported on stderr. When noSkills is true, skill
// discovery is skipped entirely (对标 pi 的 --no-skills): user command
// templates and plugin commands still load, but no /skill-name commands are
// registered. mgr may be nil (no plugins loaded).
func buildSlashRegistry(live *liveRunConfig, noSkills bool, mgr *plugin.Manager) (*runtime.SlashRegistry, error) {
	reg := runtime.NewSlashRegistry()
	registerLiveCommands(reg, live)
	registerPluginCommands(reg, mgr)
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
	// Load skills from ~/.agents/skills and register each as a /skill-name
	// command, unless discovery is disabled. A skill invocation expands to the
	// skill's instructions as the next prompt. A partial parse error is
	// non-fatal: the skills that DID load are still registered, and the error is
	// only reported on stderr — so one malformed skill file cannot hide every
	// other skill.
	if !noSkills {
		// First-run bootstrap: copy the built-in skill collections into the
		// skills directory so they load as /skill-name commands out of the box.
		// It is silent and best-effort — a failure never blocks the REPL — and
		// runs before loadSkillCommands so freshly installed skills are picked up
		// this launch. Skipped entirely under --no-skills, matching the "don't
		// load ⇒ don't install" expectation. Debug output goes to stderr only
		// when PIGO_DEBUG is set, keeping normal launches quiet.
		var blog io.Writer
		if os.Getenv("PIGO_DEBUG") != "" {
			blog = os.Stderr
		}
		builtinskills.Bootstrap(configDir(), skillsDir(), blog)

		skillCmds, serr := loadSkillCommands()
		for _, c := range skillCmds {
			reg.AddUser(c)
		}
		if serr != nil {
			fmt.Fprintf(os.Stderr, "pigo: skills: some skills failed to load: %v\n", serr)
		}
	}
	if shadowed := reg.Shadowed(); len(shadowed) > 0 {
		fmt.Fprintf(os.Stderr, "pigo: user commands shadowed by built-ins (rename to use): %v\n", shadowed)
	}
	return reg, nil
}

// skillsDir returns the directory skills are loaded from. It defaults to
// ~/.agents/skills and can be overridden with PIGO_SKILLS_DIR (useful for tests
// and non-standard layouts). An empty string is returned when the home
// directory cannot be resolved and no override is set.
func skillsDir() string {
	if dir := os.Getenv("PIGO_SKILLS_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agents", "skills")
}

// loadSkillCommands loads skills from skillsDir() and returns each as a
// /skill-name slash command. A missing directory yields no commands and no
// error (skills are optional). A partial parse error is returned alongside the
// skills that DID load — callers should register the returned commands and
// treat the error as a non-fatal warning, so one malformed skill file does not
// suppress every other skill.
func loadSkillCommands() ([]runtime.SlashCommand, error) {
	dir := skillsDir()
	if dir == "" {
		return nil, nil
	}
	skills, err := runtime.LoadSkillsDir(dir)
	cmds := make([]runtime.SlashCommand, 0, len(skills))
	for _, s := range skills {
		cmds = append(cmds, s.SlashCommand())
	}
	return cmds, err
}

// liveRunConfig is the mutable run configuration a control command may change
// mid-session. The run closure reads it on every prompt, so a /model switch
// takes effect on the next turn. It carries no lock: it is read and written
// only on the REPL's single main goroutine (slash actions and the run are both
// invoked synchronously from runREPL's loop, never concurrently).
type liveRunConfig struct {
	model        string
	providerName string
	provider     provider.Provider
	baseURL      string
	protocol     string
	// thinkingLevel is the reasoning-effort level applied to each turn. It is
	// seeded from the resolved config chain and read by streamRun on every prompt.
	thinkingLevel agentcore.ThinkingLevel
	// contextWindow is the model's total context-token budget, used to gate
	// automatic compaction. When 0 the window is unknown and auto-compaction is
	// disabled; the REPL seeds it with a conservative default so long sessions
	// still compact rather than overflow.
	contextWindow int
}

// defaultContextWindow is the fallback context-token budget used when a model's
// true window is unknown. It is deliberately large so auto-compaction only fires
// on genuinely long sessions (threshold = window - ReserveTokens), never on
// ordinary short exchanges.
const defaultContextWindow = 128000

// registerPluginCommands installs each plugin-declared slash command
// (Manager.Commands()) into the registry as a hybrid (Run) command. Invoking it
// RPCs the owning plugin (Plugin.CallCommand), returns the plugin's
// notifications as the outcome Message, and returns the plugin's Prompt to run
// as the next turn. Plugin commands are registered with AddUser so a same-named
// built-in still wins (existing precedence preserved) and a collision is
// reported as shadowed. mgr may be nil (no plugins), in which case this is a
// no-op.
//
// The args passed to CallCommand are the invocation's raw argument text encoded
// as a JSON string (json.RawMessage of a quoted string), never null: the host
// (node #263) expects a JSON string for a no-arg command, so a bare "/cmd"
// sends `""` rather than nil. Each command captures its own plugin and spec name
// (loop variables copied per-iteration).
func registerPluginCommands(reg *runtime.SlashRegistry, mgr *plugin.Manager) {
	if mgr == nil {
		return
	}
	for _, pc := range mgr.Commands() {
		pc := pc // capture per iteration
		reg.AddUser(runtime.SlashCommand{
			Name:        pc.Spec.Name,
			Description: pc.Spec.Description,
			Run: func(args string) (message, prompt string) {
				// Encode the raw arg text as a JSON string (""for no args), matching
				// the host's CommandCallParams.Args contract (a JSON string, never
				// null). json.Marshal of a Go string always succeeds.
				raw, _ := json.Marshal(args)
				res, err := pc.Plugin.CallCommand(context.Background(), pc.Spec.Name, json.RawMessage(raw))
				if err != nil {
					return fmt.Sprintf("plugin command %q failed: %v", pc.Spec.Name, err), ""
				}
				return formatNotifications(res.Notifications), res.Prompt
			},
		})
	}
}

// formatNotifications renders a plugin command's notifications into a single
// block to surface to the user, one per line, prefixed by their type (when set)
// so severity is visible. Returns "" when there are none.
func formatNotifications(notes []plugin.CommandNotification) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, n := range notes {
		if i > 0 {
			b.WriteString("\n")
		}
		if n.Type != "" {
			b.WriteString("[")
			b.WriteString(n.Type)
			b.WriteString("] ")
		}
		b.WriteString(n.Message)
	}
	return b.String()
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
			prov, providerName, err := resolveProvider(id, live.baseURL, live.protocol, "")
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
			color := colorEnabled()
			var b strings.Builder
			b.WriteString(colorize(color, ansiBold, "available commands:"))
			for _, c := range reg.List() {
				b.WriteString("\n  ")
				b.WriteString(colorize(color, ansiCyan, "/"+c.Name))
				if c.Description != "" {
					b.WriteString(" ")
					b.WriteString(colorize(color, ansiDim, "— "+c.Description))
				}
			}
			return b.String()
		},
	})
	// /exit, /quit, /compact, /fork, /clone, /tree, /export, /import, /copy and
	// /session are intercepted by the REPL loop before slash resolution (they must
	// return from the loop, run an agent stream, or read/swap the active
	// session/leaf — none of which an Action closure can do). They are registered
	// here only so /help lists them; their Action is never actually reached.
	for _, c := range []struct{ name, desc string }{
		{"exit", "exit the REPL"},
		{"quit", "exit the REPL"},
		{"compact", "summarize and compact the conversation context now"},
		{"fork", "branch from a historical message into a new session: /fork [n]"},
		{"clone", "duplicate the current session into an independent branch"},
		{"tree", "show the session branch tree; switch active branch: /tree [n]"},
		{"export", "export the session to a file: /export [path.jsonl|path.html]"},
		{"import", "import a JSONL export as a new session: /import <path.jsonl>"},
		{"copy", "copy the most recent assistant reply to the clipboard"},
		{"session", "show session stats: messages, tokens, model, compactions"},
		{"goal", "run autonomously toward a goal: /goal [--tokens N] <objective> | pause | resume | clear"},
	} {
		reg.AddBuiltin(runtime.SlashCommand{
			Name:        c.name,
			Description: c.desc,
			Action:      func(string) string { return "" },
		})
	}
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
