// Package ui holds the leaf terminal-UI helpers shared across the cmd/pigo and
// internal/cli subpackages: ANSI color gating (color.go), turn-end Markdown
// rendering (markdown.go), and prompt image-reference parsing (imageref.go).
// These were moved verbatim from cmd/pigo (US-002, #358) and exported so the
// repl, btw, status and goal layers style output through one owner.
package ui

import "os"

// ANSI SGR escape sequences used by the REPL. This is a handful of codes, not a
// general-purpose styling library.
const (
	Reset  = "\033[0m"
	Bold   = "\033[1m"
	Dim    = "\033[2m"
	Cyan   = "\033[36m"
	Green  = "\033[32m"
	Red    = "\033[31m"
	Yellow = "\033[33m"
)

// StdoutIsTerminal reports whether stdout is an interactive terminal (not a
// pipe/file). It gates color output and is also used to decide print vs
// interactive mode.
func StdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Enabled reports whether ANSI color should be emitted. Color is on only when
// stdout is an interactive terminal and NO_COLOR is unset (对标 the
// https://no-color.org convention). This keeps piped/redirected output and CI
// logs free of escape codes.
func Enabled() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return StdoutIsTerminal()
}

// Colorize wraps s in the given SGR code(s) and a reset when color is enabled,
// and returns s unchanged otherwise. Callers decide the code; an empty code
// returns s as-is so it is safe to call unconditionally.
func Colorize(enabled bool, code, s string) string {
	if !enabled || code == "" {
		return s
	}
	return code + s + Reset
}
