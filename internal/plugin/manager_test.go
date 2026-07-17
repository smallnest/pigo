// Tests for plugin discovery (US-016, #132): executable detection, deterministic
// order, fault tolerance (a bad plugin is skipped, not fatal), and empty/missing
// directory handling.
package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDiscoverMissingDir checks a missing directory yields an empty manager.
func TestDiscoverMissingDir(t *testing.T) {
	m, err := Discover(filepath.Join(t.TempDir(), "nope"), nil, nil)
	if err != nil {
		t.Fatalf("Discover missing dir: %v", err)
	}
	if len(m.Plugins()) != 0 {
		t.Errorf("want no plugins, got %d", len(m.Plugins()))
	}
}

// TestDiscoverSkipsNonExecutable checks non-executable files are ignored.
func TestDiscoverSkipsNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix executable-bit semantics not applicable on windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := Discover(dir, nil, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(m.Plugins()) != 0 {
		t.Errorf("non-executable/dir entries should be skipped, got %d plugins", len(m.Plugins()))
	}
}

// TestDiscoverLoadsAndIsolatesBad checks that a good plugin loads and a bad one
// (executable that isn't a valid plugin) is logged and skipped rather than
// aborting discovery.
func TestDiscoverLoadsAndIsolatesBad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugins are unix-only in this test")
	}
	dir := t.TempDir()

	// Good plugin: compiled from echoPluginSrc, placed inside the discovery dir.
	good := buildTestPlugin(t, "aaa-echo", echoPluginSrc)
	if err := os.Rename(good, filepath.Join(dir, "aaa-echo")); err != nil {
		t.Fatal(err)
	}

	// Bad plugin: an executable that immediately exits without speaking the
	// protocol, so the initialize handshake fails.
	bad := filepath.Join(dir, "zzz-bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	m, err := Discover(dir, &warn, os.Stderr)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	defer m.Close()

	if len(m.Plugins()) != 1 {
		t.Fatalf("want 1 good plugin loaded, got %d", len(m.Plugins()))
	}
	if m.Plugins()[0].Manifest.Name != "echo" {
		t.Errorf("loaded wrong plugin: %q", m.Plugins()[0].Manifest.Name)
	}
	if !strings.Contains(warn.String(), "zzz-bad") {
		t.Errorf("bad plugin should be logged, warn=%q", warn.String())
	}
	if tools := m.Tools(); len(tools) != 1 || tools[0].Name() != "shout" {
		t.Errorf("aggregated tools = %+v, want one 'shout'", tools)
	}
}
