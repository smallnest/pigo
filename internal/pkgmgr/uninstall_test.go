package pkgmgr

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListInstalled verifies ListInstalled returns entries sorted by name and
// an empty slice when no lockfile exists.
func TestListInstalled(t *testing.T) {
	home := t.TempDir()
	lockPath := filepath.Join(home, "packages.json")

	// No lockfile yet → empty.
	got, err := ListInstalled(lockPath)
	if err != nil {
		t.Fatalf("ListInstalled (empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty list = %v, want none", got)
	}

	// Seed two packages out of order; expect sorted by name.
	lf, err := Load(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lf.Set(InstalledPackage{Name: "zeta", Source: "npm:zeta", Version: "1.0.0", Types: []PackageType{TypeSkill}})
	lf.Set(InstalledPackage{Name: "alpha", Source: "npm:alpha", Version: "2.0.0", Types: []PackageType{TypeExtension}})
	if err := lf.Save(); err != nil {
		t.Fatal(err)
	}

	got, err = ListInstalled(lockPath)
	if err != nil {
		t.Fatalf("ListInstalled: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("list = %+v, want [alpha zeta] sorted", got)
	}
}

// TestUninstallRemovesFilesAndEntry verifies uninstall deletes the recorded
// files (and payload dirs) and drops the lockfile entry.
func TestUninstallRemovesFilesAndEntry(t *testing.T) {
	home := t.TempDir()
	lockPath := filepath.Join(home, "packages.json")

	// Lay down a payload dir + a file inside it, and a standalone launcher file.
	payload := filepath.Join(home, "plugins", "demo.pkg")
	if err := os.MkdirAll(payload, 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(payload, "cli.js")
	if err := os.WriteFile(inner, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(home, "plugins", "demo")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	lf, err := Load(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lf.Set(InstalledPackage{
		Name:    "demo",
		Source:  "npm:demo",
		Version: "1.0.0",
		Types:   []PackageType{TypeExtension},
		Files:   []string{inner, payload, launcher},
	})
	if err := lf.Save(); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall("demo", lockPath, nil); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	// Files gone.
	if _, err := os.Stat(payload); !os.IsNotExist(err) {
		t.Errorf("payload dir still present: %v", err)
	}
	if _, err := os.Stat(launcher); !os.IsNotExist(err) {
		t.Errorf("launcher still present: %v", err)
	}
	// Lockfile entry gone.
	lf2, err := Load(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lf2.Get("demo"); ok {
		t.Error("lockfile still has demo after uninstall")
	}
}

// TestUninstallMissingFilesSkipped verifies uninstall converges (removes the
// entry) even when some recorded files are already gone.
func TestUninstallMissingFilesSkipped(t *testing.T) {
	home := t.TempDir()
	lockPath := filepath.Join(home, "packages.json")

	present := filepath.Join(home, "commands", "x.md")
	if err := os.MkdirAll(filepath.Dir(present), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(present, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "commands", "gone.md") // never created

	lf, err := Load(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lf.Set(InstalledPackage{Name: "cmds", Version: "1.0.0", Types: []PackageType{TypePrompt}, Files: []string{present, missing}})
	if err := lf.Save(); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall("cmds", lockPath, nil); err != nil {
		t.Fatalf("Uninstall with a missing file: %v", err)
	}
	if _, err := os.Stat(present); !os.IsNotExist(err) {
		t.Errorf("present file not removed: %v", err)
	}
	lf2, _ := Load(lockPath)
	if _, ok := lf2.Get("cmds"); ok {
		t.Error("entry not removed after uninstall")
	}
}

// TestUninstallNotInstalled verifies uninstalling an unknown package errors.
func TestUninstallNotInstalled(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "packages.json")
	if err := Uninstall("nope", lockPath, nil); err == nil {
		t.Fatal("Uninstall of missing package = nil error, want error")
	}
}
