// Package pkgmgr implements pigo's pi-package installer state and layout
// (#154). A pi package is an add-on published to npm — an extension (often an
// MCP adapter), a skill, a prompt/command template, or a theme — installed with
// `pigo install npm:<name>`. This package owns two concerns that the install /
// list / uninstall / update commands all build on:
//
//   - The lockfile: a JSON record at $PIGO_HOME/packages.json of every installed
//     package (name, source, version, types, and the exact files laid down on
//     disk). It is the source of truth for list/uninstall/update, so removal and
//     upgrade can find and clean up precisely what an install created.
//   - The directory layout: where each package type is placed so pigo's existing
//     discovery mechanisms pick it up without extra configuration — extensions
//     under $PIGO_HOME/plugins, skills under the skills dir, prompts under
//     $PIGO_HOME/commands, themes under $PIGO_HOME/themes.
//
// This file defines the lockfile wire types and their load/save, mirroring the
// conventions already used by internal/trust: a missing file is an empty
// lockfile (not an error), while a present-but-malformed file is a hard error so
// a corrupted store is surfaced rather than silently overwritten.
package pkgmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// PackageType is one of the pi package kinds pigo can install. A single package
// may declare several (the npm catalog has combined "extensionskill" entries),
// so an InstalledPackage carries a slice of these.
type PackageType string

const (
	// TypeExtension is an executable extension (including MCP adapters); it is
	// laid down under $PIGO_HOME/plugins and discovered by internal/plugin.
	TypeExtension PackageType = "extension"
	// TypeSkill is a skill bundle placed under the skills directory.
	TypeSkill PackageType = "skill"
	// TypePrompt is a prompt/command template placed under $PIGO_HOME/commands.
	TypePrompt PackageType = "prompt"
	// TypeTheme is a theme; pigo has no theme runtime yet, so it is only stored
	// under $PIGO_HOME/themes for a future consumer.
	TypeTheme PackageType = "theme"
)

// InstalledPackage is one entry in the lockfile: everything needed to describe,
// upgrade, or remove a package that was installed.
type InstalledPackage struct {
	// Name is the package's identifier (the npm package name).
	Name string `json:"name"`
	// Source is the original install reference, e.g. "npm:pi-mcp-adapter".
	Source string `json:"source"`
	// Version is the resolved, installed version string.
	Version string `json:"version"`
	// Types are the pi package kinds this package was classified as (one or more).
	Types []PackageType `json:"types"`
	// Files are the absolute paths of every file laid down on disk for this
	// package, so uninstall/update can remove exactly what was created.
	Files []string `json:"files"`
}

// Lockfile is the on-disk record of all installed packages, keyed by package
// name. The zero value is not usable; obtain one via Load.
type Lockfile struct {
	// Version is the lockfile schema version, for forward migration.
	Version int `json:"version"`
	// Packages maps package name to its installed record.
	Packages map[string]InstalledPackage `json:"packages"`

	// path is where Save writes; not serialized.
	path string `json:"-"`
}

// lockfileVersion is the current schema version written by Save.
const lockfileVersion = 1

// DefaultLockfilePath returns the lockfile location: $PIGO_HOME/packages.json,
// or ~/.pigo/packages.json when PIGO_HOME is unset. It returns "" when the home
// directory cannot be resolved and no override is set, mirroring
// trust.DefaultPath so the caller can treat the store as unavailable rather than
// guessing a path.
func DefaultLockfilePath() string {
	if dir := os.Getenv("PIGO_HOME"); dir != "" {
		return filepath.Join(dir, "packages.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pigo", "packages.json")
}

// Load reads the lockfile at path. A missing file is not an error: it yields an
// empty lockfile whose Save will create the file. A present-but-malformed file
// is a hard error so a corrupted store is surfaced rather than silently
// overwritten. An empty path yields an in-memory-only lockfile (Save is a no-op).
func Load(path string) (*Lockfile, error) {
	lf := &Lockfile{
		Version:  lockfileVersion,
		Packages: make(map[string]InstalledPackage),
		path:     path,
	}
	if path == "" {
		return lf, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return lf, nil // no lockfile yet → empty
		}
		return nil, fmt.Errorf("pkgmgr: read lockfile %q: %w", path, err)
	}
	if err := json.Unmarshal(data, lf); err != nil {
		return nil, fmt.Errorf("pkgmgr: parse lockfile %q: %w", path, err)
	}
	if lf.Packages == nil {
		lf.Packages = make(map[string]InstalledPackage)
	}
	lf.path = path
	return lf, nil
}

// Save writes the lockfile to its path as human-readable, indented JSON, with
// package keys in sorted order for a stable diff. Save creates the parent
// directory if needed. It is a no-op when the lockfile has no path (empty-path
// Load), so in-memory use never touches disk.
func (lf *Lockfile) Save() error {
	if lf.path == "" {
		return nil
	}
	if lf.Version == 0 {
		lf.Version = lockfileVersion
	}
	if err := os.MkdirAll(filepath.Dir(lf.path), 0o755); err != nil {
		return fmt.Errorf("pkgmgr: create lockfile dir: %w", err)
	}
	data, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return fmt.Errorf("pkgmgr: encode lockfile: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(lf.path, data, 0o644); err != nil {
		return fmt.Errorf("pkgmgr: write lockfile %q: %w", lf.path, err)
	}
	return nil
}

// Get returns the installed record for name and whether it exists.
func (lf *Lockfile) Get(name string) (InstalledPackage, bool) {
	p, ok := lf.Packages[name]
	return p, ok
}

// Set records (or replaces) a package entry in memory. Call Save to persist.
func (lf *Lockfile) Set(p InstalledPackage) {
	if lf.Packages == nil {
		lf.Packages = make(map[string]InstalledPackage)
	}
	lf.Packages[p.Name] = p
}

// Remove deletes the entry for name in memory, reporting whether it existed.
// Call Save to persist.
func (lf *Lockfile) Remove(name string) bool {
	if _, ok := lf.Packages[name]; !ok {
		return false
	}
	delete(lf.Packages, name)
	return true
}

// List returns all installed packages sorted by name, for stable `pigo list`
// output.
func (lf *Lockfile) List() []InstalledPackage {
	out := make([]InstalledPackage, 0, len(lf.Packages))
	for _, p := range lf.Packages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
