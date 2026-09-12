// Package sessioncmd implements the `pigo session` subcommand family
// (issue #570): sharing persisted sessions from the command line, without
// entering the REPL. It dispatches early in cmd/pigo, the same way the
// package-management subcommands do.
//
//	pigo session export <id> [--format md|json|html] [--output path]
//	                         [--no-redact] [--include-system-prompt]
//
// The Markdown format is the shareable default: credential-shaped strings are
// redacted (default on) and the system prompt is dropped (opt-in with
// --include-system-prompt). JSONL stays the lossless resumable archive and
// rejects redaction; HTML is the self-contained transcript. Upload or
// publication of the produced file (e.g. to a dataset host) is deliberately
// out of scope — pipe the file to the tool of your choice.
package sessioncmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/smallnest/pigo/internal/cli/headless"
	"github.com/smallnest/pigo/internal/session"
)

// Run executes a session subcommand (cmd) with arguments (args), writing
// output to out and errors to errOut. It returns a process exit code.
func Run(cmd string, args []string, out, errOut io.Writer) int {
	switch cmd {
	case "export":
		return runExport(args, out, errOut)
	case "list":
		return runList(out, errOut)
	default:
		fmt.Fprintf(errOut, "pigo session: unknown command %q (want export or list)\n", cmd)
		return 2
	}
}

// runList mirrors pigo --list-sessions under the session subcommand.
func runList(out, errOut io.Writer) int {
	if err := headless.PrintSessions(out); err != nil {
		fmt.Fprintf(errOut, "pigo session list: %v\n", err)
		return 1
	}
	return 0
}

// runExport implements `pigo session export <id> [flags]`. Flags are parsed
// by hand (the top-level pflag set is not engaged for early-dispatch
// subcommands): --format, --output, --no-redact, --include-system-prompt.
func runExport(args []string, out, errOut io.Writer) int {
	var id, format, outputPath string
	noRedact := false
	includeSys := false
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format", "-f":
			if i+1 >= len(args) {
				fmt.Fprintln(errOut, "pigo session export: --format needs a value (md, json, html)")
				return 2
			}
			i++
			format = args[i]
		case "--output", "-o":
			if i+1 >= len(args) {
				fmt.Fprintln(errOut, "pigo session export: --output needs a path")
				return 2
			}
			i++
			outputPath = args[i]
		case "--no-redact":
			noRedact = true
		case "--include-system-prompt":
			includeSys = true
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(errOut, "pigo session export: unknown flag %q\n", args[i])
				return 2
			}
			rest = append(rest, args[i])
		}
	}
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintln(errOut, "usage: pigo session export <session-id> [--format md|json|html] [--output path] [--no-redact] [--include-system-prompt]")
		return 2
	}
	id = rest[0]

	store, err := headless.SessionStore()
	if err != nil {
		fmt.Fprintf(errOut, "pigo session export: %v\n", err)
		return 1
	}

	opts := session.ShareOptions{Redact: !noRedact, IncludeSystemPrompt: includeSys}
	// No explicit --output: stream the document to stdout so the command
	// composes in pipelines (pigo session export <id> | less).
	if outputPath == "" {
		if format == "" {
			format = "md"
		}
		if (strings.ToLower(strings.TrimPrefix(format, ".")) == "json" || strings.ToLower(strings.TrimPrefix(format, ".")) == "jsonl") && opts.Redact {
			fmt.Fprintln(errOut, "pigo session export: redaction does not apply to the lossless JSONL export; use --no-redact or the md format")
			return 2
		}
		header, entries, err := store.LoadEntries(id)
		if err != nil {
			fmt.Fprintf(errOut, "pigo session export: %v\n", err)
			return 1
		}
		if err := writeShareFormat(out, header, entries, format, opts); err != nil {
			fmt.Fprintf(errOut, "pigo session export: %v\n", err)
			return 1
		}
		return 0
	}

	if format == "" {
		format = session.ExportFormatFromPath(outputPath)
	}
	n, err := store.ExportShare(id, outputPath, format, opts)
	if err != nil {
		fmt.Fprintf(errOut, "pigo session export: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "exported %d entries to %s (redaction: %v)\n", n, outputPath, !noRedact)
	return 0
}

// writeShareFormat dispatches one in-memory rendering for the stdout path.
// The html format is file-oriented (self-contained transcript) and is not
// offered on the stdout path; use --output.
func writeShareFormat(w io.Writer, header session.SessionHeader, entries []session.Entry, format string, opts session.ShareOptions) error {
	switch strings.ToLower(strings.TrimPrefix(format, ".")) {
	case "json", "jsonl":
		return session.WriteJSONL(w, header, entries)
	default:
		return session.WriteMarkdown(w, header, entries, opts)
	}
}
