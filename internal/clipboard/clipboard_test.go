package clipboard

// Tests for the clipboard helper (US-009, #125). Copy shells out to a platform
// utility, so we cannot assert the OS clipboard actually changed in a hermetic
// test. Instead we verify the graceful-degradation contract: with no utility on
// PATH, Copy returns ErrUnavailable (so the REPL can fall back to printing) and
// Available reports false. We control the environment by pointing PATH at an
// empty temp dir.

import (
	"errors"
	"os"
	"testing"
)

// TestCopyUnavailableWhenNoUtility verifies Copy returns ErrUnavailable and
// Available returns false when PATH holds no clipboard utility. This is the
// contract the REPL relies on to degrade to printing.
func TestCopyUnavailableWhenNoUtility(t *testing.T) {
	// Point PATH at an empty dir so exec.LookPath finds no pbcopy/xclip/etc.
	empty := t.TempDir()
	t.Setenv("PATH", empty)

	if Available() {
		t.Error("Available() = true with empty PATH, want false")
	}
	err := Copy("hello")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Copy err = %v, want ErrUnavailable", err)
	}
}

// TestCopyUsesUtilityOnPath verifies Copy invokes a discovered utility and
// succeeds when the utility exits 0. We plant a fake executable named after the
// current platform's first candidate on PATH and confirm Copy returns nil.
func TestCopyUsesUtilityOnPath(t *testing.T) {
	cands := candidates()
	if len(cands) == 0 {
		t.Skip("no clipboard candidates for this platform")
	}
	dir := t.TempDir()
	name := cands[0].name
	if os.PathSeparator == '\\' {
		t.Skip("fake-executable planting not supported on Windows in this test")
	}
	// A trivial script that drains stdin and exits 0, using only shell builtins
	// (the test empties PATH, so external commands like `cat` are unavailable).
	script := "#!/bin/sh\nwhile read _; do :; done\nexit 0\n"
	path := dir + string(os.PathSeparator) + name
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", dir)

	if !Available() {
		t.Fatal("Available() = false after planting fake utility")
	}
	if err := Copy("payload"); err != nil {
		t.Errorf("Copy err = %v, want nil", err)
	}
}
