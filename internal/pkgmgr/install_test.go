package pkgmgr

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeNPMForInstall writes a fake `npm` onto PATH that packs a prebuilt tarball
// (built from files) into --pack-destination, mimicking `npm pack`.
func fakeNPMForInstall(t *testing.T, tarballFiles map[string]string, packName string) {
	t.Helper()
	binDir := t.TempDir()
	srcTarball := filepath.Join(binDir, "src.tgz")
	makeTarGz(t, srcTarball, tarballFiles)
	script := `#!/bin/sh
dest=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--pack-destination" ]; then dest="$a"; fi
  prev="$a"
done
cp "` + srcTarball + `" "$dest/` + packName + `"
echo "` + packName + `"
`
	if err := os.WriteFile(filepath.Join(binDir, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestInstallExtensionEndToEnd drives Install for an extension package through
// fetch (fake npm) → classify → distribute → lockfile.
func TestInstallExtensionEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake npm shell script + extension install are POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	fakeNPMForInstall(t, map[string]string{
		"package/package.json": `{"name":"pi-demo","version":"1.2.0","bin":"./cli.js"}`,
		"package/cli.js":       "#!/usr/bin/env node\nconsole.log('hi')\n",
	}, "pi-demo-1.2.0.tgz")

	lockPath := filepath.Join(home, "packages.json")
	res, err := Install("npm:pi-demo", lockPath, nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Name != "pi-demo" || res.Version != "1.2.0" {
		t.Errorf("result = %+v, want pi-demo@1.2.0", res)
	}
	if len(res.Types) != 1 || res.Types[0] != TypeExtension {
		t.Errorf("types = %v, want [extension]", res.Types)
	}

	// Launcher exists in plugins.
	if _, err := os.Stat(filepath.Join(home, "plugins", "pi-demo")); err != nil {
		t.Errorf("launcher not installed: %v", err)
	}

	// Lockfile records the package.
	lf, err := Load(lockPath)
	if err != nil {
		t.Fatalf("Load lockfile: %v", err)
	}
	p, ok := lf.Get("pi-demo")
	if !ok {
		t.Fatal("lockfile missing pi-demo")
	}
	if p.Source != "npm:pi-demo" || p.Version != "1.2.0" {
		t.Errorf("lockfile entry = %+v", p)
	}
	if len(p.Files) == 0 {
		t.Error("lockfile entry has no files")
	}
}

// TestInstallMultiType drives Install for a package that is both extension and
// skill, verifying both distributions happen and both types are recorded.
func TestInstallMultiType(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only")
	}
	home := t.TempDir()
	skills := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	t.Setenv("PIGO_SKILLS_DIR", skills)

	fakeNPMForInstall(t, map[string]string{
		"package/package.json": `{"name":"combo","version":"1.0.0","bin":"./x.js","pi":{"types":["extension","skill"]}}`,
		"package/x.js":         "#!/usr/bin/env node\n",
		"package/SKILL.md":     "---\nname: combo\ndescription: d\n---\nbody",
	}, "combo-1.0.0.tgz")

	res, err := Install("npm:combo", filepath.Join(home, "packages.json"), nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.Types) != 2 {
		t.Errorf("types = %v, want extension+skill", res.Types)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "combo")); err != nil {
		t.Errorf("extension launcher missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skills, "combo", "SKILL.md")); err != nil {
		t.Errorf("skill not installed: %v", err)
	}
}

// TestInstallUnrecognized verifies a non-pi package fails install with a clear
// error and writes no lockfile entry.
func TestInstallUnrecognized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	fakeNPMForInstall(t, map[string]string{
		"package/package.json": `{"name":"lodash","version":"4.0.0"}`,
	}, "lodash-4.0.0.tgz")

	lockPath := filepath.Join(home, "packages.json")
	if _, err := Install("npm:lodash", lockPath, nil); err == nil {
		t.Fatal("Install of non-pi package = nil error, want error")
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("lockfile written for failed install: %v", err)
	}
}

// TestInstallBadRef verifies an invalid reference is rejected before any fetch.
func TestInstallBadRef(t *testing.T) {
	if _, err := Install("github:owner/repo", filepath.Join(t.TempDir(), "packages.json"), nil); err == nil {
		t.Fatal("Install with non-npm ref = nil error, want error")
	}
}
