package pkgmgr

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDistributeExtensionStringBin verifies a package with a string "bin"
// installs a launcher + payload tree and reports created files.
func TestDistributeExtensionStringBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("extension install not supported on windows")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-demo","version":"1.0.0","bin":"./cli.js"}`, map[string]string{
		"cli.js":  "#!/usr/bin/env node\nconsole.log('hi')\n",
		"lib/x.js": "module.exports=1\n",
	})

	files, err := DistributeExtension(pkg, "pi-demo")
	if err != nil {
		t.Fatalf("DistributeExtension: %v", err)
	}

	launcher := filepath.Join(home, "plugins", "pi-demo")
	info, err := os.Stat(launcher)
	if err != nil {
		t.Fatalf("stat launcher: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("launcher not executable: mode %v", info.Mode())
	}

	// The payload bin must exist and be executable.
	binAbs := filepath.Join(home, "plugins", "pi-demo.pkg", "cli.js")
	bi, err := os.Stat(binAbs)
	if err != nil {
		t.Fatalf("stat payload bin: %v", err)
	}
	if bi.Mode()&0o111 == 0 {
		t.Errorf("payload bin not executable: mode %v", bi.Mode())
	}

	// Sibling files copied.
	if _, err := os.Stat(filepath.Join(home, "plugins", "pi-demo.pkg", "lib", "x.js")); err != nil {
		t.Errorf("sibling file not copied: %v", err)
	}

	// created list includes launcher and payload dir.
	var sawLauncher bool
	for _, f := range files {
		if f == launcher {
			sawLauncher = true
		}
	}
	if !sawLauncher {
		t.Errorf("created files %v missing launcher", files)
	}

	// A ".js" bin is a pi extension, so the launcher runs the Node host and a
	// .pihost.mjs is dropped beside the payload.
	host := filepath.Join(home, "plugins", "pi-demo.pkg", ".pihost.mjs")
	if _, err := os.Stat(host); err != nil {
		t.Errorf("expected embedded pi host at %q: %v", host, err)
	}
	script, _ := os.ReadFile(launcher)
	if !contains(string(script), "exec node ") || !contains(string(script), host) {
		t.Errorf("launcher script = %q, want node host exec of %q", script, host)
	}
}

// TestDistributeExtensionObjectBin verifies the {command: path} bin form,
// preferring the entry keyed by package name.
func TestDistributeExtensionObjectBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("extension install not supported on windows")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-adapter","version":"2.0.0","bin":{"pi-adapter":"./main.js","other":"./other.js"}}`, map[string]string{
		"main.js":  "#!/usr/bin/env node\n",
		"other.js": "#!/usr/bin/env node\n",
	})

	if _, err := DistributeExtension(pkg, "pi-adapter"); err != nil {
		t.Fatalf("DistributeExtension: %v", err)
	}

	binAbs := filepath.Join(home, "plugins", "pi-adapter.pkg", "main.js")
	if bi, err := os.Stat(binAbs); err != nil || bi.Mode()&0o111 == 0 {
		t.Errorf("expected main.js executable, err=%v", err)
	}
}

// TestDistributeExtensionReinstallReplaces verifies a second install clears the
// stale payload rather than merging it.
func TestDistributeExtensionReinstallReplaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("extension install not supported on windows")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg1 := writePkg(t, `{"name":"pi-x","version":"1.0.0","bin":"./a.js"}`, map[string]string{
		"a.js":    "#!/usr/bin/env node\n",
		"gone.js": "old\n",
	})
	if _, err := DistributeExtension(pkg1, "pi-x"); err != nil {
		t.Fatalf("first install: %v", err)
	}

	pkg2 := writePkg(t, `{"name":"pi-x","version":"2.0.0","bin":"./a.js"}`, map[string]string{
		"a.js": "#!/usr/bin/env node\n",
	})
	if _, err := DistributeExtension(pkg2, "pi-x"); err != nil {
		t.Fatalf("reinstall: %v", err)
	}

	// The file only present in the first install must be gone.
	if _, err := os.Stat(filepath.Join(home, "plugins", "pi-x.pkg", "gone.js")); !os.IsNotExist(err) {
		t.Errorf("stale file survived reinstall: %v", err)
	}
}

// TestDistributeExtensionPiExtensionsEntry verifies a pi extension with no npm
// "bin" resolves its entrypoint from pi.extensions (the pi-simplify shape).
func TestDistributeExtensionPiExtensionsEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("extension install not supported on windows")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-simplify","version":"0.2.3","pi":{"extensions":["dist/index.js"]}}`, map[string]string{
		"dist/index.js": "#!/usr/bin/env node\nconsole.log('hi')\n",
	})

	files, err := DistributeExtension(pkg, "pi-simplify")
	if err != nil {
		t.Fatalf("DistributeExtension: %v", err)
	}

	binAbs := filepath.Join(home, "plugins", "pi-simplify.pkg", "dist", "index.js")
	bi, err := os.Stat(binAbs)
	if err != nil {
		t.Fatalf("stat payload bin: %v", err)
	}
	if bi.Mode()&0o111 == 0 {
		t.Errorf("payload bin not executable: mode %v", bi.Mode())
	}

	// A pi.extensions package must run under the Node host, dropping .pihost.mjs
	// and pointing the launcher at `node <host> <pkgDir>`.
	launcher := filepath.Join(home, "plugins", "pi-simplify")
	host := filepath.Join(home, "plugins", "pi-simplify.pkg", ".pihost.mjs")
	pkgDir := filepath.Join(home, "plugins", "pi-simplify.pkg")
	if _, err := os.Stat(host); err != nil {
		t.Errorf("expected embedded pi host at %q: %v", host, err)
	}
	script, _ := os.ReadFile(launcher)
	if !contains(string(script), "exec node ") || !contains(string(script), host) || !contains(string(script), pkgDir) {
		t.Errorf("launcher script = %q, want node host exec of %q with pkgDir %q", script, host, pkgDir)
	}
	if !contains(string(script), "node not found") {
		t.Errorf("launcher script = %q, missing node-absent guard", script)
	}

	// Both the host and the launcher must be recorded for uninstall.
	var sawHost, sawLauncher bool
	for _, f := range files {
		switch f {
		case host:
			sawHost = true
		case launcher:
			sawLauncher = true
		}
	}
	if !sawHost || !sawLauncher {
		t.Errorf("created files %v missing host or launcher", files)
	}
}

// TestDistributeExtensionBinaryBinDirectExec verifies a native binary bin (no
// pi.extensions, non-JS extension) keeps the historical direct-exec launcher —
// no Node host, no .pihost.mjs.
func TestDistributeExtensionBinaryBinDirectExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("extension install not supported on windows")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-native","version":"1.0.0","bin":"./server"}`, map[string]string{
		"server": "#!/usr/bin/env node\n",
	})

	files, err := DistributeExtension(pkg, "pi-native")
	if err != nil {
		t.Fatalf("DistributeExtension: %v", err)
	}

	launcher := filepath.Join(home, "plugins", "pi-native")
	binAbs := filepath.Join(home, "plugins", "pi-native.pkg", "server")
	script, _ := os.ReadFile(launcher)
	if !contains(string(script), binAbs) {
		t.Errorf("launcher script = %q, want direct exec of %q", script, binAbs)
	}
	if contains(string(script), "exec node ") {
		t.Errorf("binary bin launcher = %q, should not run the Node host", script)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "pi-native.pkg", ".pihost.mjs")); !os.IsNotExist(err) {
		t.Errorf("binary bin should not drop .pihost.mjs, err=%v", err)
	}

	// The launcher and payload dir must be recorded; the host must not be.
	var sawLauncher bool
	for _, f := range files {
		if f == launcher {
			sawLauncher = true
		}
	}
	if !sawLauncher {
		t.Errorf("created files %v missing launcher", files)
	}
}

// TestDistributeExtensionMainEntry verifies the "main" field is used as a
// last-resort entrypoint when neither bin nor pi.extensions is present.
func TestDistributeExtensionMainEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("extension install not supported on windows")
	}
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-main","version":"1.0.0","main":"./dist/index.js"}`, map[string]string{
		"dist/index.js": "#!/usr/bin/env node\n",
	})

	if _, err := DistributeExtension(pkg, "pi-main"); err != nil {
		t.Fatalf("DistributeExtension: %v", err)
	}
	binAbs := filepath.Join(home, "plugins", "pi-main.pkg", "dist", "index.js")
	if bi, err := os.Stat(binAbs); err != nil || bi.Mode()&0o111 == 0 {
		t.Errorf("expected main entrypoint executable, err=%v", err)
	}
}

// TestDistributeExtensionNoBin verifies a package without a bin errors.
func TestDistributeExtensionNoBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("extension install not supported on windows")
	}
	t.Setenv("PIGO_HOME", t.TempDir())
	pkg := writePkg(t, `{"name":"pi-nobin","version":"1.0.0"}`, nil)
	if _, err := DistributeExtension(pkg, "pi-nobin"); err == nil {
		t.Fatal("DistributeExtension without bin = nil error, want error")
	}
}
