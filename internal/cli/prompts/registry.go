// Package prompts holds the slash-command registry assembly shared by the REPL
// (internal/cli/repl) and the forthcoming TUI (internal/cli/tui). It was sunk
// out of the repl package (#383) so both front-ends wire the same built-in,
// live-state, plugin-declared, prompt-template and skill commands from one
// owner, avoiding drift between the two command surfaces.
//
// The logic here is a verbatim move of repl's former private
// buildSlashRegistry/loadPromptPaths/promptTemplateSources (plus their
// register helpers), exported unchanged so REPL behavior is identical.
package prompts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// PromptTemplateSources carries the prompt-template discovery sources that
// BuildSlashRegistry loads beyond the global ~/.pigo/{commands,prompts} dirs.
// Settings is the config.toml `prompts` array (TierSettings); CLI is the
// --prompt-template flag list (TierCLI, wired in #339). Each entry is a file or
// directory (loaded non-recursively). Missing paths are warned and skipped.
type PromptTemplateSources struct {
	Settings []string
	CLI      []string
	// Disable (--no-prompt-templates) turns off all prompt-template discovery
	// (global, project, settings, CLI); built-ins and skills are unaffected.
	Disable bool
	// ProjectDir is the project-local prompts dir (.pigo/prompts in the working
	// dir), loaded at the project tier only when ProjectTrusted is true.
	ProjectDir string
	// ProjectTrusted reports whether the working directory is trusted; project
	// templates load only then (mirrors pi: project prompts after the project is
	// trusted).
	ProjectTrusted bool
}

// LoadPromptPaths loads prompt templates from each path (file or dir), skipping
// and warning on paths that don't exist or fail to read. It is tier-agnostic;
// the caller registers each result at the desired tier (AddSettings/AddCLI).
func LoadPromptPaths(paths []string) []runtime.SlashCommand {
	var out []runtime.SlashCommand
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: prompts path %q not found, skipping\n", p)
			continue
		}
		var cmds []runtime.SlashCommand
		if info.IsDir() {
			cmds, err = runtime.LoadUserCommandsDir(p)
		} else {
			c, e := runtime.LoadPromptFile(p)
			if e != nil {
				err = e
			} else {
				cmds = []runtime.SlashCommand{c}
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "pigo: prompts path %q: %v\n", p, err)
			continue
		}
		out = append(out, cmds...)
	}
	return out
}

// BuildSlashRegistry assembles the slash-command registry: compile-time
// built-ins seeded by runtime.NewSlashRegistry, the live-state action commands
// (/model, /help) bound to live, user declarative templates loaded from
// ~/.pigo/commands (or $PIGO_HOME/commands), plugin-declared commands from the
// loaded Manager, plus the pre-loaded skills — each surfaced as a "/skill-name"
// command (mirrors Claude Code's /skill invocation). A missing directory is not an
// error. Names that collide with a built-in are shadowed (the built-in wins) and
// reported on stderr. The skills slice is loaded once by setupAgentEnv (empty
// under --no-skills), so no /skill-name commands are registered when it is
// empty. mgr may be nil (no plugins loaded).
func BuildSlashRegistry(live *cli.LiveConfig, skills []*runtime.Skill, mgr *plugin.Manager, srcs PromptTemplateSources) (*runtime.SlashRegistry, error) {
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live)
	RegisterPluginCommands(reg, mgr)
	// --no-prompt-templates disables all prompt-template discovery (global,
	// settings, CLI); built-in slash commands and skills are unaffected.
	if !srcs.Disable {
		dir := os.Getenv("PIGO_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return reg, nil // built-ins only
			}
			dir = filepath.Join(home, ".pigo")
		}
		// Load user prompt templates from both the legacy ~/.pigo/commands and
		// the pi-aligned ~/.pigo/prompts (both non-recursive, global tier).
		// Loading commands first means a same-named template in prompts/
		// overrides the legacy one (last-write-wins within the global tier). A
		// missing directory is not an error (LoadUserCommandsDir returns nil,
		// nil for IsNotExist).
		for _, sub := range []string{"commands", "prompts"} {
			cmds, err := runtime.LoadUserCommandsDir(filepath.Join(dir, sub))
			if err != nil {
				return reg, err
			}
			for _, c := range cmds {
				reg.AddUser(c)
			}
		}
		// Settings-tier templates from the config.toml `prompts` array, then
		// CLI-tier templates from --prompt-template. Each entry is a file or
		// dir; missing paths are warned and skipped.
		for _, c := range LoadPromptPaths(srcs.Settings) {
			reg.AddSettings(c)
		}
		for _, c := range LoadPromptPaths(srcs.CLI) {
			reg.AddCLI(c)
		}
		// Project-tier templates from .pigo/prompts in the working directory,
		// loaded only when the project is trusted (mirrors pi). A missing dir is
		// not an error. Overrides global/settings/CLI (project tier is higher).
		if srcs.ProjectTrusted && srcs.ProjectDir != "" {
			cmds, err := runtime.LoadUserCommandsDir(srcs.ProjectDir)
			if err != nil {
				return reg, err
			}
			for _, c := range cmds {
				reg.AddProject(c)
			}
		}
	}
	// Register skills as /skill-name commands from the pre-loaded set (shared with
	// prompt injection in setupAgentEnv, so the directory is read once). All
	// skills — including disable-model-invocation ones — get a slash command; the
	// prompt-injection side filters the disabled ones. Under --no-skills the set
	// is empty, so nothing is registered.
	for _, s := range skills {
		reg.AddSkill(s.SlashCommand())
	}
	if sh := reg.Shadowed(); len(sh) > 0 {
		parts := make([]string, len(sh))
		for i, e := range sh {
			parts[i] = e.String()
		}
		fmt.Fprintf(os.Stderr, "pigo: commands shadowed by higher-priority source (rename to use): %v\n", parts)
	}
	return reg, nil
}

// RegisterPluginCommands installs each plugin-declared slash command
// (Manager.Commands()) into the registry as a hybrid (Run) command. Invoking it
// RPCs the owning plugin (Plugin.CallCommand), returns the plugin's
// notifications as the outcome Message, and returns the plugin's Prompt to run
// as the next turn. Plugin commands are registered with AddPlugin so a same-named
// built-in still wins (existing precedence preserved) and a collision is
// reported as shadowed. mgr may be nil (no plugins), in which case this is a
// no-op.
//
// The args passed to CallCommand are the invocation's raw argument text encoded
// as a JSON string (json.RawMessage of a quoted string), never null: the host
// (node #263) expects a JSON string for a no-arg command, so a bare "/cmd"
// sends `""` rather than nil. Each command captures its own plugin and spec name
// (loop variables copied per-iteration).
func RegisterPluginCommands(reg *runtime.SlashRegistry, mgr *plugin.Manager) {
	if mgr == nil {
		return
	}
	for _, pc := range mgr.Commands() {
		pc := pc // capture per iteration
		reg.AddPlugin(runtime.SlashCommand{
			Name:        pc.Spec.Name,
			Description: pc.Spec.Description,
			Run: func(args string) (message, prompt string) {
				// Encode the raw arg text as a JSON string ("" for no args), matching
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

// RegisterLiveCommands installs the built-in action commands that need live
// runtime state. /model views or switches the active model; /help lists the
// available commands. These are instance built-ins (AddBuiltin) because their
// closures must capture live and the registry — state unreachable from an
// init()-time global registration.
func RegisterLiveCommands(reg *runtime.SlashRegistry, live *cli.LiveConfig) {
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "model",
		Description: "view or switch the active model: /model [model-id] (see /models for presets)",
		Action: func(args string) string {
			id := strings.TrimSpace(args)
			if id == "" {
				return fmt.Sprintf("model: %s (provider: %s)\nrun /models to see presets, or /model <id> to switch", live.Model, live.ProviderName)
			}
			prov, providerName, err := provider.ResolveProvider(id, live.BaseURL, live.Protocol, "", os.Getenv)
			if err != nil {
				return fmt.Sprintf("model: cannot switch to %q: %v", id, err)
			}
			live.Model = id
			live.ProviderName = providerName
			live.Provider = prov
			return fmt.Sprintf("model switched to %s (provider: %s)", id, providerName)
		},
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "models",
		Description: "list preset providers and models you can switch to",
		Action:      func(args string) string { return presetListing(strings.TrimSpace(args)) },
	})
	// thinkAction views or switches the reasoning-effort level. It backs both
	// /think and its alias /effect, so the two commands share identical behavior.
	thinkAction := func(args string) string {
		lvl := strings.TrimSpace(args)
		if lvl == "" {
			cur := live.ThinkingLevel
			if cur == "" {
				cur = agentcore.ThinkingOff
			}
			return fmt.Sprintf("think: %s\nswitch with /think <off|minimal|low|medium|high|xhigh>", cur)
		}
		v, ok := validThinkingLevel(lvl)
		if !ok {
			return fmt.Sprintf("think: invalid level %q (want off|minimal|low|medium|high|xhigh)", lvl)
		}
		live.ThinkingLevel = v
		return fmt.Sprintf("think level set to %s (applies to the next turn)", v)
	}
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "think",
		ArgumentHint: "[off|minimal|low|medium|high|xhigh]",
		Description:  "view or switch the reasoning-effort level; takes effect on the next turn",
		Action:       thinkAction,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "effect",
		ArgumentHint: "[off|minimal|low|medium|high|xhigh]",
		Description:  "alias of /think: view or switch the reasoning-effort level",
		Action:       thinkAction,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "help",
		Description: "list available slash commands",
		Action: func(string) string {
			color := ui.Enabled()
			var b strings.Builder
			b.WriteString(ui.Colorize(color, ui.Bold, "available commands:"))
			for _, c := range reg.List() {
				b.WriteString("\n  ")
				b.WriteString(ui.Colorize(color, ui.Cyan, "/"+c.Name))
				rest := ""
				if c.ArgumentHint != "" {
					rest += " " + c.ArgumentHint
				}
				if c.Description != "" {
					rest += " - " + c.Description
				}
				rest += " (source: " + c.Tier.String() + ")"
				b.WriteString(ui.Colorize(color, ui.Dim, rest))
			}
			return b.String()
		},
	})
	// /exit, /quit, /compact, /fork, /clone, /tree, /export, /import, /copy,
	// /session and /status are intercepted by the REPL loop before slash resolution
	// (they must return from the loop, run an agent stream, or read/swap the active
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
		{"status", "show session status: runtime config, context, telemetry, credentials, environment"},
		{"goal", "run autonomously toward a goal: /goal [--tokens N] <objective> | pause | resume | clear"},
		{"btw", "ask a quick side question without touching the main conversation: /btw <question> (bare /btw reopens the last one)"},
		{"dream", "consolidate memory now (dedupe, merge, prune, distill); /dream --dry-run previews without writing"},
		{"remote-control", "mirror this session to a phone/browser on your LAN: /remote-control [stop|status]"},
	} {
		reg.AddBuiltin(runtime.SlashCommand{
			Name:        c.name,
			Description: c.desc,
			Action:      func(string) string { return "" },
		})
	}
}

// validThinkingLevel reports whether s is one of the known reasoning-effort
// levels and returns the typed value. It mirrors the enum in agentcore so a
// /think argument can be validated without importing the config layer.
func validThinkingLevel(s string) (agentcore.ThinkingLevel, bool) {
	switch agentcore.ThinkingLevel(s) {
	case agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh:
		return agentcore.ThinkingLevel(s), true
	default:
		return "", false
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
