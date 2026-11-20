package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/agent"
)

// TestWithWorkingDirAcceptsExistingDir is the happy path: an existing
// directory goes through validation and New succeeds.
func TestWithWorkingDirAcceptsExistingDir(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()

	sess, err := agent.New(agent.WithWorkingDir(dir))
	if err != nil {
		t.Fatalf("New(WithWorkingDir(%q)) = %v, want nil", dir, err)
	}
	defer sess.Close()
}

// TestWithWorkingDirRejectsMissingDir: a non-existent directory is a
// configuration mistake and must fail at New, not on the first tool call.
func TestWithWorkingDirRejectsMissingDir(t *testing.T) {
	hermetic(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	sess, err := agent.New(agent.WithWorkingDir(missing))
	if err == nil {
		sess.Close()
		t.Fatalf("New(WithWorkingDir(%q)) = nil, want error", missing)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error %q should name the offending directory", err)
	}
}

// TestWithWorkingDirRejectsRegularFile: a path that exists but is not a
// directory must be rejected too.
func TestWithWorkingDirRejectsRegularFile(t *testing.T) {
	hermetic(t)
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := agent.New(agent.WithWorkingDir(file))
	if err == nil {
		sess.Close()
		t.Fatalf("New(WithWorkingDir(%q)) = nil, want error", file)
	}
}

// TestWithWorkingDirEmptyKeepsDefault: the zero value must remain valid and
// keep the process working directory (covered white-box in
// workingdir_internal_test.go).
func TestWithWorkingDirEmptyKeepsDefault(t *testing.T) {
	hermetic(t)

	sess, err := agent.New()
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer sess.Close()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.WorkingDir(); got != cwd {
		t.Errorf("WorkingDir() = %q, want process cwd %q", got, cwd)
	}
}
