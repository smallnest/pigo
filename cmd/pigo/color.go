// This file provides a minimal ANSI color helper for REPL status output (e.g.
// the /help command listing). It deliberately avoids a third-party dependency:
// the palette is a handful of SGR escape codes gated behind a single
// colorEnabled decision so non-terminal output (pipes, files, NO_COLOR) stays
// plain text.
package main

import "os"

// ANSI SGR escape sequences used by the REPL. Kept unexported and small — this
// is not a general-purpose styling library.
const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"
	ansiCyan  = "\033[36m"
	ansiGreen = "\033[32m"
	ansiRed   = "\033[31m"
)

// colorEnabled reports whether ANSI color should be emitted. Color is on only
// when stdout is an interactive terminal and NO_COLOR is unset (对标 the
// https://no-color.org convention). This keeps piped/redirected output and CI
// logs free of escape codes.
func colorEnabled() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return stdoutIsTerminal()
}

// colorize wraps s in the given SGR code(s) and a reset when color is enabled,
// and returns s unchanged otherwise. Callers decide the code; an empty code
// returns s as-is so it is safe to call unconditionally.
func colorize(enabled bool, code, s string) string {
	if !enabled || code == "" {
		return s
	}
	return code + s + ansiReset
}
