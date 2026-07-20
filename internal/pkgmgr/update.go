// This file implements the update operation (#164): bring an installed package
// up to the latest version published on npm. Update builds on the pieces the
// earlier issues provided — the lockfile (#154) records what is installed and
// from which source, Fetch (#156) pulls the latest tarball, and the shared
// installFetched path (#162) re-classifies and re-distributes.
//
// The flow per package is: look up its recorded source, fetch the latest
// version, and compare against the installed version. If unchanged, it is a
// no-op ("up to date"). Otherwise the old files are removed and the freshly
// fetched version is distributed and recorded. Fetch/classify happen before any
// removal, so a fetch failure leaves the existing install untouched.
package pkgmgr

import (
	"fmt"
	"io"
)

// UpdateResult reports what Update did for one package.
type UpdateResult struct {
	Name string
	// OldVersion is the version that was installed before the update.
	OldVersion string
	// NewVersion is the version after the update (== OldVersion when no-op).
	NewVersion string
	// Updated is true when a newer version was fetched and installed.
	Updated bool
}

// Update brings the single installed package name up to the latest version from
// its recorded source. Progress is written to logw when non-nil. Updating a
// package that is not installed is an error. When the installed version is
// already latest, it is a no-op and Updated is false.
//
// The latest tarball is fetched and classified before the old files are
// removed, so a fetch or classify failure leaves the prior install intact.
func Update(name, lockfilePath string, logw io.Writer) (UpdateResult, error) {
	logf := func(format string, a ...any) {
		if logw != nil {
			fmt.Fprintf(logw, format, a...)
		}
	}

	lf, err := Load(lockfilePath)
	if err != nil {
		return UpdateResult{}, err
	}
	p, ok := lf.Get(name)
	if !ok {
		return UpdateResult{}, fmt.Errorf("package not installed: %s", name)
	}

	// Resolve the source to its latest version by dropping any pinned version,
	// so `update` always targets the newest published release.
	ref, err := ParsePackageRef(p.Source)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("pkgmgr: bad recorded source for %s: %w", name, err)
	}
	ref.Version = ""

	logf("Fetching %s ...\n", ref.String())
	fetched, err := Fetch(ref)
	if err != nil {
		return UpdateResult{}, err // old install untouched
	}
	defer fetched.Cleanup()

	_, newVersion, _, err := Classify(fetched.Dir)
	if err != nil {
		return UpdateResult{}, err // old install untouched
	}
	if newVersion == p.Version {
		logf("%s is up to date\n", name)
		return UpdateResult{Name: name, OldVersion: p.Version, NewVersion: p.Version, Updated: false}, nil
	}

	// Newer version fetched: remove the old files, then distribute the new one.
	if err := Uninstall(name, lockfilePath, logw); err != nil {
		return UpdateResult{}, err
	}
	if _, err := installFetched(fetched.Dir, ref, lockfilePath, logf); err != nil {
		return UpdateResult{}, err
	}
	logf("Updated %s %s -> %s\n", name, p.Version, newVersion)
	return UpdateResult{Name: name, OldVersion: p.Version, NewVersion: newVersion, Updated: true}, nil
}

// UpdateAll updates every installed package in the lockfile, in sorted order.
// It continues past a per-package failure, collecting results; the returned
// error is non-nil (a joined summary) when any package failed to update.
func UpdateAll(lockfilePath string, logw io.Writer) ([]UpdateResult, error) {
	pkgs, err := ListInstalled(lockfilePath)
	if err != nil {
		return nil, err
	}
	var results []UpdateResult
	var failures []string
	for _, p := range pkgs {
		res, uerr := Update(p.Name, lockfilePath, logw)
		if uerr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", p.Name, uerr))
			continue
		}
		results = append(results, res)
	}
	if len(failures) > 0 {
		return results, fmt.Errorf("pkgmgr: %d package(s) failed to update: %v", len(failures), failures)
	}
	return results, nil
}
