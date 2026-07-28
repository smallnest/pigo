// Package tui hosts the full-screen terminal UI for pigo's interactive mode
// (US-001). It is the alt-screen counterpart to the line-based REPL in
// internal/cli/repl: cmd/pigo's dispatch launches it via Run when there is no
// prompt, stdout is a TTY, and --no-tui is not set; otherwise the REPL path is
// used. See tasks/spec-tui-agent.md (Sections 2.1, 4.2, 5.2) for the design.
//
// This node is the skeleton: a root Model (Init/Update/View) built on Bubble
// Tea v2 (charm.land/bubbletea/v2) that renders an empty shell — a placeholder
// status bar, an empty transcript area, and an empty input line — starts on the
// alt-screen, and quits cleanly on Ctrl+C / Ctrl+D, restoring the terminal.
// Session assembly, the run bridge, tool cards, and slash-command completion
// land in downstream nodes; Options already mirrors repl.Options so those nodes
// can wire real behavior without changing the entry seam.
package tui
