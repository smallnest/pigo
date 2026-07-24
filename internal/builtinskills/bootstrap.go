// This file holds the first-run bootstrap: on the first launch (per pigo home),
// the built-in skill collections in Manifest are copied into the user's skills
// directory so they load as /skill-name commands with no manual install.
//
// The flow is designed to be silent and non-blocking (a failed bootstrap must
// never stop pigo from starting) and idempotent (skills already present are left
// untouched, so a user's edits are never clobbered). A state file under the pigo
// home records which collections+versions have been installed, so a completed
// bootstrap is skipped on later launches and a bumped collection Version
// re-triggers installation of any still-missing skills.
package builtinskills

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// stateFileName is the bootstrap record kept under the pigo home directory. It
// maps a collection name to the version last installed, so Bootstrap can decide
// per collection whether work remains.
const stateFileName = "builtin-skills.json"

// state is the on-disk bootstrap record: collection name -> installed version.
type state struct {
	Installed map[string]string `json:"installed"`
}

// Bootstrap installs any not-yet-installed built-in skill collections into
// skillsDir, recording progress under pigoHome. It is safe to call on every
// launch: collections already recorded at their current Version are skipped, and
// individual skills whose target directory already exists are left untouched.
//
// It never returns an error — bootstrap is best-effort and must not block
// startup — but writes a one-line note per failure to logw when logw is non-nil
// (callers pass a writer only in debug/verbose mode, keeping normal runs silent).
// Empty pigoHome or skillsDir disables bootstrap (home unresolved): nothing is
// installed and nothing is logged.
func Bootstrap(pigoHome, skillsDir string, logw io.Writer) {
	bootstrap(Manifest(), pigoHome, skillsDir, logw)
}

// bootstrap is the testable core of Bootstrap, parameterized on the manifest so
// tests can inject synthetic collections without touching the embedded set.
func bootstrap(sets []Set, pigoHome, skillsDir string, logw io.Writer) {
	logf := func(format string, a ...any) {
		if logw != nil {
			fmt.Fprintf(logw, format, a...)
		}
	}
	if pigoHome == "" || skillsDir == "" {
		return // home unresolved; nothing we can safely do
	}

	st := loadState(filepath.Join(pigoHome, stateFileName))

	changed := false
	for _, set := range sets {
		// Per-collection version gate: once recorded at this Version the whole
		// set is skipped, so a skill the user later *deletes* is not restored
		// (only a Version bump re-triggers install of still-missing skills).
		// This is deliberate — silently re-adding a removed skill would fight
		// the user's choice. An empty Version is never "already installed"
		// (the zero-value lookup would otherwise equal it and skip forever),
		// so a set with a blank Version always attempts install.
		if set.Version != "" && st.Installed[set.Name] == set.Version {
			continue
		}
		// installSet reports whether the collection is fully installed (every
		// skill now present on disk). Only then do we record the version, so a
		// partial failure re-runs next launch (satisfying "retry on next run").
		if installSet(set, skillsDir, logf) {
			if st.Installed == nil {
				st.Installed = map[string]string{}
			}
			st.Installed[set.Name] = set.Version
			changed = true
		}
	}

	if changed {
		if err := saveState(pigoHome, st); err != nil {
			logf("builtinskills: could not save state: %v\n", err)
		}
	}
}

// installSet copies each skill in the set into skillsDir/<name>/, skipping any
// whose target directory already exists (never clobbering a user's copy). A
// single skill's failure does not abort the rest. It returns true only when
// every named skill is present on disk afterward, so the caller can decide
// whether to record the collection as fully installed.
func installSet(set Set, skillsDir string, logf func(string, ...any)) bool {
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		logf("builtinskills: create skills dir %q: %v\n", skillsDir, err)
		return false
	}
	allPresent := true
	for _, name := range set.Skills {
		dest := filepath.Join(skillsDir, name)
		if _, err := os.Stat(dest); err == nil {
			continue // already present (user copy or prior install): leave it
		}
		src := path.Join(set.Root, name)
		if err := copyTree(set.FS, src, dest); err != nil {
			logf("builtinskills: install %q: %v\n", name, err)
			// Remove a half-written tree so a later run starts clean.
			_ = os.RemoveAll(dest)
			allPresent = false
			continue
		}
	}
	return allPresent
}

// copyTree copies the directory tree rooted at src within srcFS to the on-disk
// directory dest, creating parents as needed. Files are written 0o644 and
// directories 0o755.
func copyTree(srcFS fs.FS, src, dest string) error {
	entries, err := fs.ReadDir(srcFS, src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := path.Join(src, e.Name())
		destPath := filepath.Join(dest, e.Name())
		if e.IsDir() {
			if err := copyTree(srcFS, srcPath, destPath); err != nil {
				return err
			}
			continue
		}
		data, err := fs.ReadFile(srcFS, srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// loadState reads the bootstrap record at p. A missing or malformed file yields
// an empty state (treated as "nothing installed"), so a corrupt record just
// re-triggers a — idempotent — install rather than failing.
func loadState(p string) state {
	data, err := os.ReadFile(p)
	if err != nil {
		return state{}
	}
	var st state
	if json.Unmarshal(data, &st) != nil {
		return state{}
	}
	return st
}

// saveState writes the bootstrap record under pigoHome, creating the home
// directory if needed.
func saveState(pigoHome string, st state) error {
	if err := os.MkdirAll(pigoHome, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(pigoHome, stateFileName), data, 0o644)
}
