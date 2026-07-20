// This file fetches a pi package's contents from npm (#156). Rather than
// implement an npm registry client, pigo shells out to the user's installed
// `npm` — specifically `npm pack`, which downloads a package as a .tgz tarball
// without running install scripts. pigo then extracts that tarball into a
// temporary directory for the classify/distribute steps that follow.
//
// The fetch is deliberately side-effect-light: `npm pack` neither installs
// dependencies nor runs lifecycle scripts, so downloading a package cannot
// execute its code. Running the extracted extension is a separate, later step.
//
// npm packs every package into a top-level "package/" directory inside the
// tarball; Fetch returns the path to that extracted directory.
package pkgmgr

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FetchResult describes a package fetched to a temporary directory.
type FetchResult struct {
	// Dir is the extracted package directory (the tarball's "package/" root).
	Dir string
	// TempRoot is the temporary directory holding Dir and the tarball; the
	// caller must Cleanup it when done.
	TempRoot string
}

// Cleanup removes the temporary directory tree created by Fetch. Safe to call
// on a zero FetchResult (no-op).
func (r FetchResult) Cleanup() error {
	if r.TempRoot == "" {
		return nil
	}
	return os.RemoveAll(r.TempRoot)
}

// npmExecutable is the npm binary name; a variable so tests can stub it.
var npmExecutable = "npm"

// EnsureNPM reports an actionable error when npm is not on PATH. The install
// command calls this before doing any work so it fails fast with guidance
// rather than deep inside a fetch.
func EnsureNPM() error {
	if _, err := exec.LookPath(npmExecutable); err != nil {
		return fmt.Errorf("npm not found; install Node.js/npm to use pigo install")
	}
	return nil
}

// Fetch downloads the package named by ref using `npm pack` and extracts it into
// a fresh temporary directory. On success the caller owns the returned
// FetchResult and must call Cleanup. On any failure the temporary directory is
// removed before returning, so a failed fetch leaves nothing behind.
//
// npm's own error output (unknown package, network failure, auth) is included
// in the returned error so the user sees why the fetch failed.
func Fetch(ref PackageRef) (FetchResult, error) {
	if err := EnsureNPM(); err != nil {
		return FetchResult{}, err
	}

	tmp, err := os.MkdirTemp("", "pigo-pkg-*")
	if err != nil {
		return FetchResult{}, fmt.Errorf("pkgmgr: create temp dir: %w", err)
	}
	// From here on, remove tmp on any error path.
	fail := func(e error) (FetchResult, error) {
		_ = os.RemoveAll(tmp)
		return FetchResult{}, e
	}

	spec := ref.Name
	if ref.Version != "" {
		spec += "@" + ref.Version
	}

	// `npm pack <spec>` writes a .tgz into --pack-destination and prints its
	// filename. --ignore-scripts guards against packing-time script execution.
	cmd := exec.Command(npmExecutable, "pack", spec,
		"--pack-destination", tmp,
		"--ignore-scripts",
		"--loglevel", "error")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fail(fmt.Errorf("npm pack %s failed: %s", spec, msg))
	}

	tarball, err := locateTarball(tmp, out)
	if err != nil {
		return fail(err)
	}

	dest := filepath.Join(tmp, "extracted")
	if err := extractTarGz(tarball, dest); err != nil {
		return fail(err)
	}
	// npm packs into a top-level "package/" directory.
	pkgDir := filepath.Join(dest, "package")
	if _, err := os.Stat(pkgDir); err != nil {
		return fail(fmt.Errorf("pkgmgr: extracted tarball missing package/ dir: %w", err))
	}
	return FetchResult{Dir: pkgDir, TempRoot: tmp}, nil
}

// locateTarball resolves the .tgz path that `npm pack` produced. npm prints the
// tarball filename on stdout; when that is unhelpful we fall back to scanning
// the destination directory for a single .tgz.
func locateTarball(dir string, packStdout []byte) (string, error) {
	if name := strings.TrimSpace(string(packStdout)); name != "" {
		// npm may print just the filename; join to dir if it isn't absolute.
		cand := name
		if !filepath.IsAbs(cand) {
			cand = filepath.Join(dir, filepath.Base(name))
		}
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("pkgmgr: read pack dir: %w", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tgz") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("pkgmgr: npm pack produced no .tgz in %s", dir)
}

// extractTarGz extracts a gzip-compressed tar archive into dest, creating dest.
// It guards against path traversal (a "../" entry escaping dest) and skips any
// entry that is not a regular file or directory.
func extractTarGz(tarball, dest string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return fmt.Errorf("pkgmgr: open tarball: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("pkgmgr: gzip reader: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("pkgmgr: create extract dir: %w", err)
	}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("pkgmgr: read tar: %w", err)
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("pkgmgr: mkdir %q: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("pkgmgr: mkdir parent of %q: %w", target, err)
			}
			if err := writeFile(target, tr, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		default:
			// Skip symlinks, devices, etc. — npm packages are files + dirs.
		}
	}
	return nil
}

// safeJoin joins name onto base, rejecting any result that escapes base (a
// tarball path-traversal guard).
func safeJoin(base, name string) (string, error) {
	target := filepath.Join(base, name)
	cleanBase := filepath.Clean(base) + string(os.PathSeparator)
	if target != filepath.Clean(base) && !strings.HasPrefix(target, cleanBase) {
		return "", fmt.Errorf("pkgmgr: tarball entry %q escapes extract dir", name)
	}
	return target, nil
}

// writeFile writes the tar entry body to target with the given mode.
func writeFile(target string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("pkgmgr: create %q: %w", target, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, r); err != nil {
		return fmt.Errorf("pkgmgr: write %q: %w", target, err)
	}
	return nil
}
