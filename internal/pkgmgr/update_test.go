package pkgmgr

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeNPMVersioned writes a fake `npm` onto PATH that packs whichever tarball is
// currently pointed to by a small "which version" file, so a test can flip the
// version `npm pack` returns between Install and Update.
func fakeNPMVersioned(t *testing.T, tarballs map[string]map[string]string, whichFile string) {
	t.Helper()
	binDir := t.TempDir()
	// Build every tarball once under binDir; the script copies the one named in
	// whichFile.
	for ver, files := range tarballs {
		makeTarGz(t, filepath.Join(binDir, ver+".tgz"), files)
	}
	// Script reads whichFile -> version, then copies <version>.tgz as its pack
	// output and echoes the tarball name (mimicking `npm pack`).
	script := `#!/bin/sh
dest=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--pack-destination" ]; then dest="$a"; fi
  prev="$a"
done
ver=$(cat "` + whichFile + `")
cp "` + binDir + `/$ver.tgz" "$dest/$ver.tgz"
echo "$ver.tgz"
`
	if err := os.WriteFile(filepath.Join(binDir, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestUpdateToNewerVersion installs v1.0.0 then updates to v2.0.0, verifying the
// lockfile version is bumped and the new payload is in place.
func TestUpdateToNewerVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake npm shell script is POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	whichFile := filepath.Join(t.TempDir(), "which")

	tarballs := map[string]map[string]string{
		"1.0.0": {
			"package/package.json": `{"name":"pi-demo","version":"1.0.0","bin":"./cli.js"}`,
			"package/cli.js":       "#!/usr/bin/env node\n// v1\n",
		},
		"2.0.0": {
			"package/package.json": `{"name":"pi-demo","version":"2.0.0","bin":"./cli.js"}`,
			"package/cli.js":       "#!/usr/bin/env node\n// v2\n",
		},
	}
	fakeNPMVersioned(t, tarballs, whichFile)

	lockPath := filepath.Join(home, "packages.json")

	// Install v1.
	if err := os.WriteFile(whichFile, []byte("1.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("npm:pi-demo", lockPath, nil); err != nil {
		t.Fatalf("Install v1: %v", err)
	}

	// Flip to v2 and update.
	if err := os.WriteFile(whichFile, []byte("2.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Update("pi-demo", lockPath, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Updated || res.OldVersion != "1.0.0" || res.NewVersion != "2.0.0" {
		t.Errorf("update result = %+v, want 1.0.0->2.0.0 updated", res)
	}

	lf, err := Load(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := lf.Get("pi-demo")
	if !ok || p.Version != "2.0.0" {
		t.Errorf("lockfile version after update = %+v, want 2.0.0", p)
	}
	// v2 payload content is present.
	data, err := os.ReadFile(filepath.Join(home, "plugins", "pi-demo.pkg", "cli.js"))
	if err != nil {
		t.Fatalf("read updated payload: %v", err)
	}
	if want := "// v2"; !contains(string(data), want) {
		t.Errorf("payload = %q, want to contain %q", string(data), want)
	}
}

// TestUpdateUpToDate installs v1.0.0 and updates against the same version,
// expecting a no-op (Updated false, version unchanged).
func TestUpdateUpToDate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	whichFile := filepath.Join(t.TempDir(), "which")

	tarballs := map[string]map[string]string{
		"1.0.0": {
			"package/package.json": `{"name":"pi-demo","version":"1.0.0","bin":"./cli.js"}`,
			"package/cli.js":       "#!/usr/bin/env node\n",
		},
	}
	fakeNPMVersioned(t, tarballs, whichFile)
	if err := os.WriteFile(whichFile, []byte("1.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(home, "packages.json")
	if _, err := Install("npm:pi-demo", lockPath, nil); err != nil {
		t.Fatalf("Install: %v", err)
	}
	res, err := Update("pi-demo", lockPath, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Updated {
		t.Errorf("update result = %+v, want no-op (Updated false)", res)
	}
}

// TestUpdateNotInstalled verifies updating an unknown package errors.
func TestUpdateNotInstalled(t *testing.T) {
	if _, err := Update("nope", filepath.Join(t.TempDir(), "packages.json"), nil); err == nil {
		t.Fatal("Update of missing package = nil error, want error")
	}
}
