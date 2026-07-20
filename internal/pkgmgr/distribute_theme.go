// This file distributes a classified pi theme into pigo's themes directory
// (#161). Unlike extensions/skills/prompts, pigo has no theme runtime yet, so a
// theme is simply *stored* under $PIGO_HOME/themes/<name>/ for a future
// consumer — no launcher, no discovery wiring. Storing it (rather than dropping
// it) keeps the install/list/uninstall/update lifecycle uniform: the theme has
// a home, the lockfile records its files, and uninstall can remove it cleanly.
package pkgmgr

import (
	"fmt"
	"os"
	"path/filepath"
)

// DistributeTheme copies the theme package at pkgDir into the themes directory
// under a "<name>/" subdirectory. Because pigo has no theme runtime yet, this
// only stores the theme; there is no discovery step. It returns the absolute
// paths of every file (and the theme dir) it created, for the lockfile. An
// unresolvable themes dir is an error.
func DistributeTheme(pkgDir, name string) ([]string, error) {
	themesDir := ThemesDir()
	if themesDir == "" {
		return nil, fmt.Errorf("pkgmgr: cannot resolve themes dir (PIGO_HOME/home unavailable)")
	}
	dest := filepath.Join(themesDir, name)
	// A stale theme from a prior install must not linger alongside the new one.
	if err := os.RemoveAll(dest); err != nil {
		return nil, fmt.Errorf("pkgmgr: clear old theme %q: %w", dest, err)
	}
	if err := os.MkdirAll(themesDir, 0o755); err != nil {
		return nil, fmt.Errorf("pkgmgr: create themes dir: %w", err)
	}
	files, err := copyTree(pkgDir, dest)
	if err != nil {
		return nil, err
	}
	return append(files, dest), nil
}
