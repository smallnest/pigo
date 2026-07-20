// This file wires pigo's package-management subcommands (#162, #163, #164) into
// the CLI: `pigo install|list|uninstall|update ...`. These are positional
// subcommands, distinct from the flag-driven agent modes, so main() peels them
// off before pflag parsing (the agent flags don't apply to package management).
//
// Each subcommand is a thin shell over internal/pkgmgr, which owns all the real
// work (fetch, classify, distribute, lockfile). This file only parses argv,
// resolves the lockfile path, calls pkgmgr, and prints human-readable results.
package main

import (
	"fmt"
	"io"

	"github.com/smallnest/pigo/internal/pkgmgr"
)

// packageSubcommands are the argv[1] values routed to runPackageCommand.
var packageSubcommands = map[string]bool{
	"install":   true,
	"list":      true,
	"uninstall": true,
	"update":    true,
}

// runPackageCommand executes a package-management subcommand (cmd) with its
// arguments (args), writing output to out and errors to errOut. It returns a
// process exit code. install is #162; list/uninstall are #163; update is #164.
func runPackageCommand(cmd string, args []string, out, errOut io.Writer) int {
	lockPath := pkgmgr.DefaultLockfilePath()
	switch cmd {
	case "install":
		return runInstall(args, lockPath, out, errOut)
	default:
		fmt.Fprintf(errOut, "pigo: %q is not yet implemented\n", cmd)
		return 2
	}
}

// runInstall handles `pigo install npm:<name>[@version] [more...]`. It requires
// npm on PATH (checked once up front for a clear early failure) and installs
// each reference in turn, continuing past a failure so one bad package does not
// abort the rest; the exit code is non-zero if any install failed.
func runInstall(refs []string, lockPath string, out, errOut io.Writer) int {
	if len(refs) == 0 {
		fmt.Fprintln(errOut, "pigo: install requires a package reference, e.g. pigo install npm:pi-mcp-adapter")
		return 2
	}
	if err := pkgmgr.EnsureNPM(); err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}

	failed := false
	for _, ref := range refs {
		res, err := pkgmgr.Install(ref, lockPath, out)
		if err != nil {
			fmt.Fprintf(errOut, "pigo: install %s failed: %v\n", ref, err)
			failed = true
			continue
		}
		fmt.Fprintf(out, "Installed %s@%s (%s) — %d file(s)\n",
			res.Name, res.Version, joinPkgTypes(res.Types), len(res.Files))
	}
	if failed {
		return 1
	}
	return 0
}

// joinPkgTypes renders package types as a comma-separated string.
func joinPkgTypes(types []pkgmgr.PackageType) string {
	out := ""
	for i, t := range types {
		if i > 0 {
			out += ", "
		}
		out += string(t)
	}
	return out
}
