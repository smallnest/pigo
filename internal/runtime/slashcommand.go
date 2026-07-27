// This file implements slash-commands (US-029, #45): typed "/name" shortcuts a
// user invokes in the REPL. There are two sources, resolved with a fixed
// priority:
//
//   - Built-in commands are registered at compile time via RegisterBuiltin
//     (from init() in the fork's own code). They are always available.
//   - User commands are declarative markdown templates loaded from a directory
//     (对标 the .../commands/*.md convention): the file name is the command
//     name and the body is a prompt template that may reference $ARGUMENTS.
//
// Conflict rule: same-name commands resolve by priority tier (built-in >
// project > global > package > settings > CLI); the higher tier wins and the
// loser is reported via Shadowed. Built-ins are load-bearing and always win.
// Within a tier, the last-added command overrides earlier ones (a re-load).
//
// There is deliberately no standalone plugin mechanism: a fork adds built-ins
// via init() registration, and external extensions go through MCP (deferred).
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SlashCommandSource identifies where a command came from, used for the
// conflict/priority rule and for display.
type SlashCommandSource int

const (
	// SourceBuiltin is a compile-time registered command (highest priority).
	SourceBuiltin SlashCommandSource = iota
	// SourceUser is a declarative markdown command template loaded from disk
	// (e.g. ~/.pigo/commands/*.md).
	SourceUser
	// SourceSkill is a skill loaded from ~/.agents/skills and surfaced as a
	// /skill-name command. It behaves like SourceUser for the built-in-wins
	// conflict rule; the finer tag is for display only (e.g. /status).
	SourceSkill
	// SourcePlugin is a command declared by a loaded plugin. It behaves like
	// SourceUser for the built-in-wins conflict rule; the finer tag is for
	// display only.
	SourcePlugin
)

func (s SlashCommandSource) String() string {
	switch s {
	case SourceBuiltin:
		return "builtin"
	case SourceSkill:
		return "skill"
	case SourcePlugin:
		return "plugin"
	default:
		return "user"
	}
}

// Tier is the priority tier of a command, used to resolve same-name conflicts
// across sources (对标 pi prompt-templates discovery priority). Higher tiers
// win; the loser is recorded in Shadowed. Within the same tier the last-added
// command wins (a re-load overrides). Skills and plugins are treated as
// Global-tier for priority - their finer Source label is for display only.
type Tier int

const (
	// Tier values are ordered lowest-to-highest priority: in a same-name
	// conflict the higher Tier value wins, so TierBuiltin always wins and
	// TierCLI always loses. Declared ascending so the natural > comparison
	// matches "higher priority wins".
	TierCLI Tier = iota
	// TierSettings is a prompt template referenced by the config.toml prompts array.
	TierSettings
	// TierPackage is a prompt template discovered from an installed package
	// source (distinct from one copied into the global dir).
	TierPackage
	// TierGlobal is a global user prompt template (e.g. ~/.pigo/prompts or the
	// legacy ~/.pigo/commands); also the tier used for skills and plugins.
	TierGlobal
	// TierProject is a project-local prompt template (e.g. .pigo/prompts).
	TierProject
	// TierBuiltin is a compile-time or instance built-in command (highest).
	TierBuiltin
)

func (t Tier) String() string {
	switch t {
	case TierBuiltin:
		return "builtin"
	case TierProject:
		return "project"
	case TierGlobal:
		return "global"
	case TierPackage:
		return "package"
	case TierSettings:
		return "settings"
	case TierCLI:
		return "cli"
	default:
		return "unknown"
	}
}

// ShadowedEntry records a command that lost a same-name conflict to a higher-
// tier command, for diagnostics. It carries the loser's name, tier, and source
// label so /help and the startup warning can say which source was shadowed.
type ShadowedEntry struct {
	Name   string
	Tier   Tier
	Source SlashCommandSource
}

// String renders a shadowed entry as "name (tier)" for log lines.
func (e ShadowedEntry) String() string { return fmt.Sprintf("%s (%s)", e.Name, e.Tier) }

// SlashCommand is a resolved command: its name (without the leading "/"), a
// short description for the command palette, and its source. A command is one
// of three kinds, distinguished by which callback is set:
//
//   - A prompt command sets Expand: it turns the invocation arguments into the
//     prompt text fed to the agent (the original slash-command behavior).
//   - An action command sets Action instead: it performs a side effect (e.g.
//     switching the runtime model) and returns a status line to show the user,
//     rather than producing a prompt. No agent run is started.
//   - A hybrid command sets Run: it performs a side effect AND may return prompt
//     text to run — used by plugin commands, which RPC their plugin, surface the
//     returned notifications, then inject the returned prompt as the next turn.
//
// Exactly one of Expand/Action/Run should be set. Precedence when more than one
// is set: Action wins over Run, which wins over Expand. This split is what lets
// a control command like "/model" change runtime state — the old design could
// only emit prompt text.
type SlashCommand struct {
	Name        string
	Description string
	// ArgumentHint is an optional frontmatter hint shown before the description
	// in autocomplete (e.g. "<PR-URL>"). Convention: <angle> for required args,
	// [square] for optional. Empty when not set; display-only, not enforced.
	ArgumentHint string
	Source       SlashCommandSource
	// Tier is the priority tier used to resolve same-name conflicts across
	// sources (built-in > project > global > package > settings > CLI). It is
	// set by the AddX method matching the command's source; callers should not
	// set it directly.
	Tier Tier
	// Expand maps the argument string (everything after "/name ") to the prompt
	// text the command produces. For a built-in it may be arbitrary Go; for a
	// user template it substitutes $ARGUMENTS into the markdown body. Nil for an
	// action command.
	Expand func(args string) string
	// Action performs a side effect for the invocation and returns a status
	// message to display (may be empty). Set instead of Expand for a control
	// command like "/model". Because it is an arbitrary Go closure it can capture
	// and mutate live runtime state, which Expand (a pure prompt producer)
	// cannot. Nil for a prompt command.
	Action func(args string) string
	// Run is the hybrid of Action and Expand: it performs a side effect AND may
	// produce prompt text to run as the next agent turn. It returns
	// (message, prompt): message is shown to the user immediately (like an
	// Action's status, e.g. plugin notifications), and prompt, when non-empty, is
	// run as a normal turn (like Expand's output). This is what a plugin command
	// needs — it RPCs its plugin (side effect), surfaces the returned
	// notifications (message), then injects the returned prompt (prompt). Set
	// instead of Expand/Action for such a command; nil otherwise. When Run is set
	// it takes precedence over Expand (but Action still wins over Run).
	Run func(args string) (message, prompt string)
}

// SlashKind classifies how a resolved invocation should be handled by the
// caller: run its prompt through the agent, or treat it as a completed action.
type SlashKind int

const (
	// SlashPrompt means the outcome carries prompt text to run (or, when not a
	// command at all, the verbatim input).
	SlashPrompt SlashKind = iota
	// SlashAction means an action command already ran; the outcome carries only
	// a status Message and no agent run should start.
	SlashAction
)

// SlashOutcome is the structured result of resolving one input line. Handled is
// false when the input was not a slash command (Prompt holds the verbatim input
// to run). When Handled is true, Kind says whether Prompt should be run
// (SlashPrompt) or an action already ran and Message should be shown without
// starting a run (SlashAction).
//
// A hybrid (Run) command resolves to Kind SlashPrompt with BOTH fields set: its
// side effect already ran, Message carries the text to show the user first
// (e.g. plugin notifications), and Prompt, when non-empty, is the turn to run
// after. The caller shows Message (if any) then runs Prompt (if non-empty).
type SlashOutcome struct {
	Handled bool
	Kind    SlashKind
	Prompt  string
	Message string
}

// builtinCommands holds compile-time registered commands, keyed by name. It is
// populated by RegisterBuiltin from init() and read when building a registry.
//
// Concurrency contract: this global is written only by RegisterBuiltin, which
// must be called from init() (single-threaded, before main), and read only
// afterwards by NewSlashRegistry. It carries no lock because that init-only
// discipline means there is never a concurrent write; do not call
// RegisterBuiltin after startup.
var builtinCommands = map[string]SlashCommand{}

// RegisterBuiltin registers a built-in slash command at compile time. It is
// intended to be called from init(); a duplicate name panics, since two
// built-ins claiming the same name is a programming error in the fork.
func RegisterBuiltin(cmd SlashCommand) {
	if cmd.Name == "" {
		panic("agent: RegisterBuiltin with empty name")
	}
	if _, exists := builtinCommands[cmd.Name]; exists {
		panic(fmt.Sprintf("agent: duplicate built-in slash command %q", cmd.Name))
	}
	cmd.Source = SourceBuiltin
	cmd.Tier = TierBuiltin
	builtinCommands[cmd.Name] = cmd
}

// SlashRegistry resolves "/name" invocations against built-in and user
// commands, applying the built-in-wins priority rule.
type SlashRegistry struct {
	commands map[string]SlashCommand
	// shadowed records commands that lost a same-name conflict to a higher-tier
	// command, with their tier and source for diagnostics. Same-tier overrides
	// (last-write-wins) are not recorded.
	shadowed []ShadowedEntry
}

// NewSlashRegistry builds a registry seeded with all registered built-ins.
func NewSlashRegistry() *SlashRegistry {
	r := &SlashRegistry{commands: make(map[string]SlashCommand, len(builtinCommands))}
	for name, cmd := range builtinCommands {
		r.commands[name] = cmd
	}
	return r
}

// AddBuiltin installs a built-in command directly on this registry instance,
// bypassing the compile-time global. It exists for action commands whose
// closure must capture live, per-run state (e.g. a model controller created in
// main) — such state cannot be reached from an init()-time RegisterBuiltin. The
// command is marked SourceBuiltin so it wins over a same-named user command,
// exactly like a globally registered built-in. A duplicate name panics, since
// two built-ins claiming one name is a programming error.
func (r *SlashRegistry) AddBuiltin(cmd SlashCommand) {
	if cmd.Name == "" {
		panic("agent: AddBuiltin with empty name")
	}
	if existing, ok := r.commands[cmd.Name]; ok && existing.Source == SourceBuiltin {
		panic(fmt.Sprintf("agent: duplicate built-in slash command %q", cmd.Name))
	}
	cmd.Source = SourceBuiltin
	cmd.Tier = TierBuiltin
	r.add(cmd)
}

// AddUser installs a user command (TierGlobal), e.g. a prompt template from
// ~/.pigo/prompts or the legacy ~/.pigo/commands. A same-named built-in or
// project-tier command wins; same-tier (global) adds override silently.
func (r *SlashRegistry) AddUser(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierGlobal
	r.add(cmd)
}

// AddSkill installs a skill command (loaded from ~/.agents/skills) at TierGlobal.
// It follows the same tier rule as AddUser - a built-in or project-tier command
// wins - only the source tag differs, so /status can report skills separately.
func (r *SlashRegistry) AddSkill(cmd SlashCommand) {
	cmd.Source = SourceSkill
	cmd.Tier = TierGlobal
	r.add(cmd)
}

// AddPlugin installs a plugin-declared command at TierGlobal, mirroring AddUser
// with a SourcePlugin tag for display.
func (r *SlashRegistry) AddPlugin(cmd SlashCommand) {
	cmd.Source = SourcePlugin
	cmd.Tier = TierGlobal
	r.add(cmd)
}

// Shadowed returns the commands that lost a same-name conflict to a higher-tier
// command, with their tier and source for diagnostics. Same-tier overrides
// (last-write-wins) are not recorded here.
func (r *SlashRegistry) Shadowed() []ShadowedEntry { return r.shadowed }

// AddProject installs a project-local prompt template (TierProject), which
// overrides a same-named global/package/settings/CLI template but loses to a
// built-in.
func (r *SlashRegistry) AddProject(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierProject
	r.add(cmd)
}

// AddPackage installs a package-discovered prompt template (TierPackage).
func (r *SlashRegistry) AddPackage(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierPackage
	r.add(cmd)
}

// AddSettings installs a prompt template referenced by config.toml (TierSettings).
func (r *SlashRegistry) AddSettings(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierSettings
	r.add(cmd)
}

// AddCLI installs a prompt template referenced by --prompt-template (TierCLI,
// the lowest priority).
func (r *SlashRegistry) AddCLI(cmd SlashCommand) {
	cmd.Source = SourceUser
	cmd.Tier = TierCLI
	r.add(cmd)
}

// add installs cmd with tier-based conflict resolution. If a same-named command
// already exists, the higher tier wins and the loser is appended to shadowed;
// within the same tier the new command replaces the old (last-write-wins, no
// shadow entry). A built-in always wins because TierBuiltin is highest.
func (r *SlashRegistry) add(cmd SlashCommand) {
	existing, ok := r.commands[cmd.Name]
	if !ok {
		r.commands[cmd.Name] = cmd
		return
	}
	switch {
	case existing.Tier > cmd.Tier:
		// New command is lower tier: it loses and is shadowed.
		r.shadowed = append(r.shadowed, ShadowedEntry{Name: cmd.Name, Tier: cmd.Tier, Source: cmd.Source})
	case existing.Tier < cmd.Tier:
		// New command is higher tier: it wins; the old one is shadowed.
		r.shadowed = append(r.shadowed, ShadowedEntry{Name: existing.Name, Tier: existing.Tier, Source: existing.Source})
		r.commands[cmd.Name] = cmd
	default:
		// Same tier: last-write-wins (a re-load), no shadow entry.
		r.commands[cmd.Name] = cmd
	}
}

// Lookup returns the command bound to name (without the leading "/").
func (r *SlashRegistry) Lookup(name string) (SlashCommand, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

// List returns all commands sorted by name.
func (r *SlashRegistry) List() []SlashCommand {
	out := make([]SlashCommand, 0, len(r.commands))
	for _, c := range r.commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Resolve parses a raw input line and, if it is a slash-command invocation,
// expands it to the prompt text the agent should run. It returns (prompt, true)
// when input begins with "/" and names a known PROMPT command; (input, false)
// when the input is not a slash command (the caller runs it verbatim); and an
// error when input is a "/name" for an unknown command.
//
// This is the legacy string API, kept for callers that only handle prompt
// commands. It reports an action command as handled with an empty prompt (the
// action does NOT run here) — callers that want action commands to execute must
// use ResolveOutcome instead.
func (r *SlashRegistry) Resolve(input string) (prompt string, handled bool, err error) {
	out, err := r.ResolveOutcome(input)
	if err != nil {
		return "", false, err
	}
	return out.Prompt, out.Handled, nil
}

// ResolveOutcome parses a raw input line into a structured SlashOutcome. For a
// non-command it returns {Handled:false, Prompt:input}. For a known prompt
// command it returns {Handled:true, Kind:SlashPrompt, Prompt:<expanded>}. For a
// known action command it RUNS the action and returns {Handled:true,
// Kind:SlashAction, Message:<status>} — no prompt to run. For a known hybrid
// (Run) command it RUNS the side effect and returns {Handled:true,
// Kind:SlashPrompt, Message:<status>, Prompt:<text>} — the caller shows Message
// then runs Prompt when non-empty. An unknown "/name" yields an error.
func (r *SlashRegistry) ResolveOutcome(input string) (SlashOutcome, error) {
	trimmed := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return SlashOutcome{Handled: false, Kind: SlashPrompt, Prompt: input}, nil
	}
	rest := trimmed[1:]
	name := rest
	args := ""
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		name = rest[:i]
		args = strings.TrimSpace(rest[i+1:])
	}
	cmd, ok := r.commands[name]
	if !ok {
		return SlashOutcome{}, fmt.Errorf("unknown command %q", "/"+name)
	}
	if cmd.Action != nil {
		return SlashOutcome{Handled: true, Kind: SlashAction, Message: cmd.Action(args)}, nil
	}
	if cmd.Run != nil {
		// A hybrid command runs its side effect now and may yield prompt text.
		// The outcome is a prompt (SlashPrompt) that also carries a Message to
		// surface first; the caller shows Message then runs Prompt if non-empty.
		message, prompt := cmd.Run(args)
		return SlashOutcome{Handled: true, Kind: SlashPrompt, Message: message, Prompt: prompt}, nil
	}
	return SlashOutcome{Handled: true, Kind: SlashPrompt, Prompt: cmd.Expand(args)}, nil
}

// firstNonEmptyLine returns the first line of s whose trimmed form is non-empty,
// itself trimmed. It is the description fallback for templates whose frontmatter
// omits a description (对标 pi: "If missing, the first non-empty line is used").
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// LoadUserCommandsDir loads declarative markdown command templates from dir
// (non-recursively). Each "*.md" file defines a command named after the file
// (without extension). The file may carry an optional YAML frontmatter block
// with a "description" (对标 skills); the remaining body is the prompt template,
// expanded via ExpandTemplate at invoke time. A missing directory yields no
// commands and no error.
func LoadUserCommandsDir(dir string) ([]SlashCommand, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read commands dir %s: %w", dir, err)
	}
	var cmds []SlashCommand
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read command %s: %w", path, readErr)
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		cmd, parseErr := ParseUserCommand(name, content)
		if parseErr != nil {
			return nil, parseErr
		}
		cmds = append(cmds, cmd)
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	return cmds, nil
}

// ParseUserCommand parses a declarative command template. An optional YAML
// frontmatter block supplies a description; the body is the prompt template,
// expanded at invoke time via ExpandTemplate (positional $N, $@/$ARGUMENTS,
// ${1:-default}, ${@:N}). If arg tokenization fails (e.g. an unterminated
// quote) the raw arg string is used as $ARGUMENTS so the invocation still works.
func ParseUserCommand(name string, content []byte) (SlashCommand, error) {
	body := string(content)
	description := ""
	hint := ""
	// Reuse the skills frontmatter splitter when a fence is present; otherwise
	// treat the whole file as the template body.
	if strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(body, "\ufeff"), "\r\n"), "---") {
		fm, rest, splitErr := splitFrontmatter(content)
		if splitErr != nil {
			return SlashCommand{}, fmt.Errorf("command %s: %w", name, splitErr)
		}
		var meta struct {
			Description  string `yaml:"description"`
			Name         string `yaml:"name"`
			ArgumentHint string `yaml:"argument-hint"`
		}
		if err := yaml.Unmarshal(fm, &meta); err != nil {
			return SlashCommand{}, fmt.Errorf("command %s: parse frontmatter: %w", name, err)
		}
		description = meta.Description
		hint = meta.ArgumentHint
		if meta.Name != "" {
			name = meta.Name
		}
		body = string(rest)
	}
	// When the frontmatter omits a description, fall back to the first non-empty
	// line of the body (\u5bf9\u6807 pi: "If missing, the first non-empty line is used").
	if description == "" {
		description = firstNonEmptyLine(body)
	}
	template := strings.TrimSpace(body)
	return SlashCommand{
		Name:         name,
		Description:  description,
		ArgumentHint: hint,
		Source:       SourceUser,
		Expand: func(args string) string {
			tokens, err := SplitArgs(args)
			if err != nil {
				// Split failure (e.g. an unterminated quote): treat the raw arg
				// string as a single $ARGUMENTS rather than feeding a malformed
				// arg list to the engine, so a bad invocation stays usable.
				tokens = []string{args}
			}
			return ExpandTemplate(template, tokens)
		},
	}, nil
}
