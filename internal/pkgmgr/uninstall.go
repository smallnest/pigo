// This file implements the list and uninstall operations (#163) on top of the
// lockfile (#154). Both are lightweight lockfile operations that complement the
// install flow (#162):
//
//   - Listing just reads the lockfile and returns its entries (sorted by name).
//   - Uninstalling removes every file the install laid down (recorded in the
//     lockfile entry's Files), then drops the entry and saves. A file that is
//     already gone is skipped so a partial prior removal still converges, and
//     directory entries are removed with RemoveAll so a package's payload dir
//     (e.g. plugins/<name>.pkg) comes out whole.
package pkgmgr

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// ListInstalled returns every package recorded in the lockfile at lockfilePath,
// sorted by name. A missing lockfile yields an empty slice (no packages yet),
// mirroring Load's missing-is-empty convention.
func ListInstalled(lockfilePath string) ([]InstalledPackage, error) {
	lf, err := Load(lockfilePath)
	if err != nil {
		return nil, err
	}
	return lf.List(), nil
}

// Uninstall removes the package named name: it deletes every file the install
// recorded, then removes the lockfile entry and saves. Progress is written to
// logw when non-nil. Removing a package that is not installed is an error.
//
// Files are removed longest-path-first so a directory entry is deleted after
// its contents; each path is removed with RemoveAll so both plain files and
// payload directories are handled, and an already-absent path is not an error
// (the goal state is "gone"). The lockfile entry is dropped even if some file
// removals were no-ops, so uninstall always converges the record.
func Uninstall(name, lockfilePath string, logw io.Writer) error {
	logf := func(format string, a ...any) {
		if logw != nil {
			fmt.Fprintf(logw, format, a...)
		}
	}

	lf, err := Load(lockfilePath)
	if err != nil {
		return err
	}
	p, ok := lf.Get(name)
	if !ok {
		return fmt.Errorf("package not installed: %s", name)
	}

	// Remove deepest paths first so directory entries are cleared after any
	// nested file entries recorded alongside them.
	files := append([]string(nil), p.Files...)
	sort.Slice(files, func(i, j int) bool { return len(files[i]) > len(files[j]) })
	for _, f := range files {
		if err := os.RemoveAll(f); err != nil {
			return fmt.Errorf("pkgmgr: remove %q: %w", f, err)
		}
	}
	logf("Removed %d path(s) for %s\n", len(files), name)

	lf.Remove(name)
	if err := lf.Save(); err != nil {
		return err
	}
	return nil
}
