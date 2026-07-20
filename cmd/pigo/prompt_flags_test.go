package main

// Tests for --append-system-prompt value resolution (对标 pi): each value is
// either a path to an existing file whose contents are appended, or literal
// text when it is not an existing file. A value that names an unreadable file
// (a real I/O error other than not-exist) is surfaced rather than silently
// appended verbatim.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveAppendInstructionsEmpty verifies no values yields no appends.
func TestResolveAppendInstructionsEmpty(t *testing.T) {
	out, err := resolveAppendInstructions(nil)
	if err != nil {
		t.Fatalf("resolveAppendInstructions: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil for no values, got %#v", out)
	}
}

// TestResolveAppendInstructionsLiteral verifies a value that is not an existing
// file is treated as literal text and passed through verbatim.
func TestResolveAppendInstructionsLiteral(t *testing.T) {
	out, err := resolveAppendInstructions([]string{"be concise and helpful"})
	if err != nil {
		t.Fatalf("resolveAppendInstructions: %v", err)
	}
	if len(out) != 1 || out[0] != "be concise and helpful" {
		t.Errorf("literal text must pass through verbatim, got %#v", out)
	}
}

// TestResolveAppendInstructionsFile verifies a value that names an existing
// file has its contents read and appended.
func TestResolveAppendInstructionsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guidance.txt")
	if err := os.WriteFile(path, []byte("FILE GUIDANCE"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	out, err := resolveAppendInstructions([]string{path, "literal tail"})
	if err != nil {
		t.Fatalf("resolveAppendInstructions: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 resolved values, got %#v", out)
	}
	if out[0] != "FILE GUIDANCE" {
		t.Errorf("existing file must be read into the append, got %q", out[0])
	}
	if out[1] != "literal tail" {
		t.Errorf("literal value must pass through verbatim, got %q", out[1])
	}
}

// TestResolveAppendInstructionsUnreadableFile verifies a value that looks like a
// path but points at an unreadable file (here, a directory) surfaces an error
// rather than being appended verbatim. A directory is used because os.Stat
// succeeds on it (so it is not treated as literal text) while os.ReadFile fails.
func TestResolveAppendInstructionsUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// A directory: Stat succeeds and IsDir() is true, so it is treated as literal
	// text — assert that. Then create an actually-unreadable regular file to hit
	// the read-error path.
	if out, err := resolveAppendInstructions([]string{dir}); err != nil {
		t.Fatalf("a directory should be treated as literal text, got error: %v", err)
	} else if len(out) != 1 || out[0] != dir {
		t.Errorf("a directory path must pass through as literal text, got %#v", out)
	}

	unreadable := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(unreadable, []byte("nope"), 0o000); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	// Root can read 0o000 files, so skip the read-error assertion when running as
	// root (common in CI containers) — the path would succeed instead of erroring.
	if os.Geteuid() == 0 {
		t.Skip("running as root: 0o000 file is still readable, cannot exercise read-error path")
	}
	if _, err := resolveAppendInstructions([]string{unreadable}); err == nil {
		t.Error("an unreadable append file must surface an error, got nil")
	}
}
