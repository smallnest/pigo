// This file implements slash-commands (US-029, #45): typed "/name" shortcuts a
// user invokes in the TUI. There are two sources, resolved with a fixed
// priority:
//
//   - Built-in commands are registered at compile time via RegisterBuiltin
//     (from init() in the fork's own code). They are always available.
//   - User commands are declarative markdown templates loaded from a directory
//     (对标 the .../commands/*.md convention): the file name is the command
//     name and the body is a prompt template that may reference $ARGUMENTS.
//
// Conflict rule: a built-in command wins over a user command of the same name
// (built-ins are load-bearing and must not be silently shadowed). Loading a
// user command whose name collides with a built-in is reported so the user can
// rename it, but the built-in stays in effect.
//
// There is deliberately no standalone plugin mechanism: a fork adds built-ins
// via init() registration, and external extensions go through MCP (deferred).
package agent

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
	// SourceUser is a declarative markdown command loaded from disk.
	SourceUser
)

func (s SlashCommandSource) String() string {
	if s == SourceBuiltin {
		return "builtin"
	}
	return "user"
}

// SlashCommand is a resolved command: its name (without the leading "/"), a
// short description for the command palette, its source, and an Expand function
// that turns the invocation arguments into the prompt text fed to the agent.
type SlashCommand struct {
	Name        string
	Description string
	Source      SlashCommandSource
	// Expand maps the argument string (everything after "/name ") to the prompt
	// text the command produces. For a built-in it may be arbitrary Go; for a
	// user template it substitutes $ARGUMENTS into the markdown body.
	Expand func(args string) string
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
	builtinCommands[cmd.Name] = cmd
}

// SlashRegistry resolves "/name" invocations against built-in and user
// commands, applying the built-in-wins priority rule.
type SlashRegistry struct {
	commands map[string]SlashCommand
	// shadowed records user command names that collided with a built-in (and so
	// were not installed), for diagnostics.
	shadowed []string
}

// NewSlashRegistry builds a registry seeded with all registered built-ins.
func NewSlashRegistry() *SlashRegistry {
	r := &SlashRegistry{commands: make(map[string]SlashCommand, len(builtinCommands))}
	for name, cmd := range builtinCommands {
		r.commands[name] = cmd
	}
	return r
}

// AddUser installs a user command unless a built-in already owns the name, in
// which case the command is recorded as shadowed and the built-in is kept
// (built-in-wins). A user command may override another user command of the same
// name (last write wins), matching a re-load.
func (r *SlashRegistry) AddUser(cmd SlashCommand) {
	if existing, ok := r.commands[cmd.Name]; ok && existing.Source == SourceBuiltin {
		r.shadowed = append(r.shadowed, cmd.Name)
		return
	}
	cmd.Source = SourceUser
	r.commands[cmd.Name] = cmd
}

// Shadowed returns the names of user commands that were dropped because a
// built-in owns the name.
func (r *SlashRegistry) Shadowed() []string { return r.shadowed }

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
// when input begins with "/" and names a known command; (input, false) when the
// input is not a slash command (the caller runs it verbatim); and an error when
// input is a "/name" for an unknown command.
func (r *SlashRegistry) Resolve(input string) (prompt string, handled bool, err error) {
	trimmed := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return input, false, nil
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
		return "", false, fmt.Errorf("unknown command %q", "/"+name)
	}
	return cmd.Expand(args), true, nil
}

// LoadUserCommandsDir loads declarative markdown command templates from dir
// (non-recursively). Each "*.md" file defines a command named after the file
// (without extension). The file may carry an optional YAML frontmatter block
// with a "description" (对标 skills); the remaining body is the prompt template.
// $ARGUMENTS in the body is replaced with the invocation arguments at expand
// time. A missing directory yields no commands and no error.
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
// frontmatter block supplies a description; the body is the prompt template
// with $ARGUMENTS substitution.
func ParseUserCommand(name string, content []byte) (SlashCommand, error) {
	body := string(content)
	description := ""
	// Reuse the skills frontmatter splitter when a fence is present; otherwise
	// treat the whole file as the template body.
	if strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(body, "\ufeff"), "\r\n"), "---") {
		fm, rest, splitErr := splitFrontmatter(content)
		if splitErr != nil {
			return SlashCommand{}, fmt.Errorf("command %s: %w", name, splitErr)
		}
		var meta struct {
			Description string `yaml:"description"`
			Name        string `yaml:"name"`
		}
		if err := yaml.Unmarshal(fm, &meta); err != nil {
			return SlashCommand{}, fmt.Errorf("command %s: parse frontmatter: %w", name, err)
		}
		description = meta.Description
		if meta.Name != "" {
			name = meta.Name
		}
		body = string(rest)
	}
	template := strings.TrimSpace(body)
	return SlashCommand{
		Name:        name,
		Description: description,
		Source:      SourceUser,
		Expand: func(args string) string {
			if strings.Contains(template, "$ARGUMENTS") {
				return strings.ReplaceAll(template, "$ARGUMENTS", args)
			}
			// No placeholder: append args (if any) so a bare template still
			// receives the user's input.
			if args == "" {
				return template
			}
			return template + "\n\n" + args
		},
	}, nil
}
