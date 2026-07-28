// This file implements slash-commands and their autocomplete popup for the
// full-screen TUI (US-008, FR-15). It is the TUI counterpart to the REPL's
// slash handling (internal/cli/repl/repl.go): both front-ends consult the SAME
// shared registry assembled by internal/cli/prompts.BuildSlashRegistry (#383),
// so /model, /help, user-declared templates (~/.pigo/{commands,prompts}),
// config/CLI prompt templates, plugin commands and ~/.agents/skills /skill-name
// commands are identical across the two surfaces.
//
// tui deliberately imports prompts/runtime/cli (the shared lower layers), never
// repl: prompts sits below both front-ends, so there is no import cycle.
//
// The autocomplete popup (slashMenu) activates while the input buffer is a
// "/name" being typed (a leading "/" with no whitespace yet). It filters the
// registry by the typed prefix, is navigated with the arrow keys, completed with
// Tab, and run with Enter — the model intercepts those keys before delegating to
// the textarea (see model.handleKey).
package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/prompts"
	"github.com/smallnest/pigo/internal/runtime"
)

// maxMenuRows caps how many candidate rows the popup shows at once; a longer
// filtered list scrolls a window around the selection so the overlay stays a few
// lines tall regardless of how many commands are registered.
const maxMenuRows = 8

// newSlashRegistry assembles the shared slash-command registry for the TUI the
// same way the REPL does: built-ins seeded from runtime, the live-state /model
// and /help commands bound to live (so a /model switch mutates the very config
// the run loop reads), user/plugin/skill/template commands from disk. A load
// error is non-fatal — BuildSlashRegistry still returns a registry with the
// built-ins, so the TUI stays usable and the failure is surfaced on stderr.
func newSlashRegistry(opts Options, live *cli.LiveConfig) *runtime.SlashRegistry {
	reg, err := prompts.BuildSlashRegistry(live, opts.Skills, opts.Plugins, prompts.PromptTemplateSources{
		Settings: opts.ConfigPrompts,
		CLI:      opts.CliPrompts,
		Disable:  opts.NoPromptTemplates,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigo: slash-commands: %v\n", err)
	}
	return reg
}

// slashMenu is the autocomplete popup state. It holds the candidates matching
// the current "/prefix" and the highlighted row; it is inactive (rendered as
// nothing) whenever the buffer is not a slash-command being typed or no command
// matches the prefix.
type slashMenu struct {
	theme    Theme
	active   bool
	filtered []runtime.SlashCommand
	selected int
}

// newSlashMenu builds an inactive menu bound to the theme used for its rows.
func newSlashMenu(theme Theme) slashMenu { return slashMenu{theme: theme} }

// slashToken reports whether buffer is a slash-command name still being typed
// and returns the text after the leading "/". It is true only for a leading "/"
// with no whitespace yet: once the user types a space the name is complete and
// the buffer has moved on to arguments, so name-completion stops.
func slashToken(buffer string) (token string, ok bool) {
	trimmed := strings.TrimLeft(buffer, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	rest := trimmed[1:]
	if strings.ContainsAny(rest, " \t\n") {
		return "", false
	}
	return rest, true
}

// refresh recomputes the menu from the current buffer and registry. It activates
// only when the buffer is a "/name" prefix that matches at least one command;
// otherwise it deactivates and clears its candidates. The selection is clamped
// so it stays in range as the filtered set shrinks.
func (mn *slashMenu) refresh(buffer string, reg *runtime.SlashRegistry) {
	token, ok := slashToken(buffer)
	if !ok || reg == nil {
		mn.close()
		return
	}
	var out []runtime.SlashCommand
	for _, c := range reg.List() {
		if strings.HasPrefix(c.Name, token) {
			out = append(out, c)
		}
	}
	mn.filtered = out
	mn.active = len(out) > 0
	if mn.selected >= len(out) || mn.selected < 0 {
		mn.selected = 0
	}
}

// close deactivates the menu and drops its candidates.
func (mn *slashMenu) close() {
	mn.active = false
	mn.filtered = nil
	mn.selected = 0
}

// moveUp / moveDown cycle the highlighted candidate, wrapping at the ends so
// arrow navigation is continuous.
func (mn *slashMenu) moveUp() {
	if len(mn.filtered) == 0 {
		return
	}
	mn.selected--
	if mn.selected < 0 {
		mn.selected = len(mn.filtered) - 1
	}
}

func (mn *slashMenu) moveDown() {
	if len(mn.filtered) == 0 {
		return
	}
	mn.selected++
	if mn.selected >= len(mn.filtered) {
		mn.selected = 0
	}
}

// current returns the highlighted candidate, or ok=false when the menu is
// inactive / empty.
func (mn slashMenu) current() (runtime.SlashCommand, bool) {
	if !mn.active || mn.selected < 0 || mn.selected >= len(mn.filtered) {
		return runtime.SlashCommand{}, false
	}
	return mn.filtered[mn.selected], true
}

// view renders the popup as a block of up to maxMenuRows lines, the highlighted
// row marked with a "›" caret and accented. Each row is "/name  description",
// truncated to the width so it never wraps. Returns "" when inactive so the
// model omits the overlay entirely (and its row) while idle.
func (mn slashMenu) view(width int) string {
	if !mn.active || len(mn.filtered) == 0 {
		return ""
	}
	start, end := mn.window()
	rowWidth := width - 2 // reserve the caret / indent column
	if rowWidth < 1 {
		rowWidth = width
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		c := mn.filtered[i]
		line := "/" + c.Name
		if c.Description != "" {
			line += "  " + c.Description
		}
		line = TruncateToWidth(line, rowWidth)
		if i == mn.selected {
			b.WriteString(mn.theme.Accent.Render("› " + line))
		} else {
			b.WriteString(mn.theme.System.Render("  " + line))
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// window returns the [start,end) slice of filtered candidates to display,
// scrolled to keep the selection visible when the list is taller than
// maxMenuRows.
func (mn slashMenu) window() (int, int) {
	n := len(mn.filtered)
	if n <= maxMenuRows {
		return 0, n
	}
	start := mn.selected - maxMenuRows + 1
	if start < 0 {
		start = 0
	}
	if start > n-maxMenuRows {
		start = n - maxMenuRows
	}
	return start, start + maxMenuRows
}
