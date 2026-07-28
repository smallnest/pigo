// Package cli is the contract and shared-type layer for pigo's command-line
// surface. It holds the interfaces and value types that the cmd/pigo entry
// point and the internal/cli/* subpackages share, without depending on any of
// them, so those subpackages can be assembled and tested in isolation.
//
// Subpackage layout (assembled incrementally by the CLI restructure):
//
//	internal/cli            — Host/Editor contracts, LiveConfig, TelemetryHolder
//	internal/cli/ui         — color, markdown, imageref helpers
//	internal/cli/config     — config-file loading and overrides
//	internal/cli/run        — run assembly and tool wiring
//	internal/cli/headless   — headless session and subagent RPC drivers
//	internal/cli/goal       — /goal state machine
//	internal/cli/btw        — /btw side thread
//	internal/cli/status     — /status command
//	internal/cli/repl       — interactive REPL and line editor
//	internal/cli/pkgcmd     — package-manager subcommands
//	internal/cli/testutil   — cross-subpackage test helpers
//
// The Host interface (see host.go) is the seam that lets the /goal, /btw,
// /status and REPL subpackages read and mutate the session's live state
// without importing the concrete replDeps aggregate that assembles it.
package cli
