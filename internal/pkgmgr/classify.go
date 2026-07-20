// This file classifies a fetched pi package into its type(s) — extension,
// skill, prompt, or theme (#157). Classification reads the package's
// package.json: pi packages carry a "pi" metadata block declaring what they
// provide, and pigo also falls back to structural signals (a bin entry, a
// SKILL.md, a commands/ dir) so a package that omits explicit metadata but
// clearly is one type is still recognized.
//
// A single package may be several types at once — the npm catalog has combined
// "extensionskill" entries — so Classify returns a set. When nothing matches,
// it returns an error rather than guessing, so `pigo install` fails clearly on
// a package that isn't a pi package.
//
// NOTE on metadata shape: the exact pi metadata field names are taken from the
// pi package conventions (a top-level "pi" object with a "type" string or
// "types" array, and/or per-capability keys). Both the explicit "pi.type(s)"
// form and structural fallbacks are honored so classification is robust to
// packages that under-declare. See docs/issue#0157.html for the assumptions.
package pkgmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// packageJSON is the subset of an npm package.json pigo reads for classification
// and versioning. Unknown fields are ignored.
type packageJSON struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	// Bin is npm's executable declaration: either a string path or a
	// {name: path} object. Its presence signals an extension.
	Bin json.RawMessage `json:"bin,omitempty"`
	// Pi is the pi-specific metadata block declaring the package's capabilities.
	Pi *piMeta `json:"pi,omitempty"`
}

// piMeta is the "pi" block of a package.json. It supports either a single
// "type" or a "types" list, plus per-capability booleans/objects so a package
// can declare, e.g., both an extension and a skill.
type piMeta struct {
	Type      string          `json:"type,omitempty"`
	Types     []string        `json:"types,omitempty"`
	Extension json.RawMessage `json:"extension,omitempty"`
	Skill     json.RawMessage `json:"skill,omitempty"`
	Prompt    json.RawMessage `json:"prompt,omitempty"`
	Theme     json.RawMessage `json:"theme,omitempty"`
}

// Classify inspects the fetched package directory and returns the set of pi
// package types it provides, along with the package name and version read from
// package.json. It returns an error when package.json is missing/unreadable or
// when no known pi type can be determined.
func Classify(pkgDir string) (name, version string, types []PackageType, err error) {
	pjPath := filepath.Join(pkgDir, "package.json")
	data, err := os.ReadFile(pjPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("pkgmgr: read package.json: %w", err)
	}
	var pj packageJSON
	if err := json.Unmarshal(data, &pj); err != nil {
		return "", "", nil, fmt.Errorf("pkgmgr: parse package.json: %w", err)
	}

	set := map[PackageType]bool{}

	// 1. Explicit pi metadata wins.
	if pj.Pi != nil {
		for _, t := range append(pj.Pi.Types, pj.Pi.Type) {
			if pt, ok := normalizeType(t); ok {
				set[pt] = true
			}
		}
		if len(pj.Pi.Extension) > 0 {
			set[TypeExtension] = true
		}
		if len(pj.Pi.Skill) > 0 {
			set[TypeSkill] = true
		}
		if len(pj.Pi.Prompt) > 0 {
			set[TypePrompt] = true
		}
		if len(pj.Pi.Theme) > 0 {
			set[TypeTheme] = true
		}
	}

	// 2. Structural fallbacks for packages that under-declare.
	if len(pj.Bin) > 0 {
		set[TypeExtension] = true
	}
	if fileExists(filepath.Join(pkgDir, "SKILL.md")) {
		set[TypeSkill] = true
	}
	if dirExists(filepath.Join(pkgDir, "commands")) {
		set[TypePrompt] = true
	}

	if len(set) == 0 {
		return "", "", nil, fmt.Errorf("unrecognized pi package: no known pi metadata")
	}

	types = make([]PackageType, 0, len(set))
	for t := range set {
		types = append(types, t)
	}
	slices.Sort(types)
	return pj.Name, pj.Version, types, nil
}

// normalizeType maps a pi metadata type string to a PackageType, reporting
// whether it is recognized.
func normalizeType(s string) (PackageType, bool) {
	switch PackageType(s) {
	case TypeExtension:
		return TypeExtension, true
	case TypeSkill:
		return TypeSkill, true
	case TypePrompt:
		return TypePrompt, true
	case TypeTheme:
		return TypeTheme, true
	default:
		return "", false
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
