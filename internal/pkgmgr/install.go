// This file is the install orchestrator (#162): it ties together the pieces the
// earlier issues built — fetch (#156), classify (#157), the per-type
// distributors (#158-#161), and the lockfile (#154) — into the single
// `pigo install npm:<name>` flow.
//
// The flow is: parse the reference, fetch+extract the package to a temp dir,
// classify it into one or more pi types, distribute each type to its target
// directory, then record everything laid down in the lockfile so list/uninstall
// /update can act on it. The temp dir is always cleaned up.
package pkgmgr

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// InstallResult reports what an install did, for the CLI to print.
type InstallResult struct {
	Name    string
	Version string
	Types   []PackageType
	// Files is every path laid down on disk across all distributed types.
	Files []string
}

// Install fetches, classifies, and distributes the package named by rawRef
// (e.g. "npm:pi-mcp-adapter"), then records it in the lockfile at lockfilePath.
// Progress is written to logw when non-nil. It returns a summary of the install.
//
// The install is type-driven: a package classified as several types (e.g.
// extension+skill) is distributed to each corresponding directory. If any
// distribution step fails, the error is returned; already-written files are left
// for the caller/uninstall to reconcile via a re-run (distribution is
// idempotent — each distributor clears its own stale target first).
func Install(rawRef, lockfilePath string, logw io.Writer) (InstallResult, error) {
	logf := func(format string, a ...any) {
		if logw != nil {
			fmt.Fprintf(logw, format, a...)
		}
	}

	ref, err := ParsePackageRef(rawRef)
	if err != nil {
		return InstallResult{}, err
	}

	logf("Fetching %s ...\n", ref.String())
	fetched, err := Fetch(ref)
	if err != nil {
		return InstallResult{}, err
	}
	defer fetched.Cleanup()

	name, version, types, err := Classify(fetched.Dir)
	if err != nil {
		return InstallResult{}, err
	}
	logf("Installing %s@%s (%s)\n", name, version, joinTypes(types))

	var files []string
	for _, t := range types {
		created, derr := distribute(t, fetched.Dir, name)
		if derr != nil {
			return InstallResult{}, derr
		}
		files = append(files, created...)
	}
	sort.Strings(files)

	lf, err := Load(lockfilePath)
	if err != nil {
		return InstallResult{}, err
	}
	lf.Set(InstalledPackage{
		Name:    name,
		Source:  ref.String(),
		Version: version,
		Types:   types,
		Files:   files,
	})
	if err := lf.Save(); err != nil {
		return InstallResult{}, err
	}

	return InstallResult{Name: name, Version: version, Types: types, Files: files}, nil
}

// distribute routes one package type to its distributor. An unknown type is an
// error (Classify should never produce one, but guard anyway).
func distribute(t PackageType, pkgDir, name string) ([]string, error) {
	switch t {
	case TypeExtension:
		return DistributeExtension(pkgDir, name)
	case TypeSkill:
		return DistributeSkill(pkgDir, name)
	case TypePrompt:
		return DistributePrompt(pkgDir, name)
	case TypeTheme:
		return DistributeTheme(pkgDir, name)
	default:
		return nil, fmt.Errorf("pkgmgr: cannot distribute unknown type %q", t)
	}
}

// joinTypes renders a type slice as a comma-separated string for logging.
func joinTypes(types []PackageType) string {
	parts := make([]string, len(types))
	for i, t := range types {
		parts[i] = string(t)
	}
	return strings.Join(parts, ", ")
}
