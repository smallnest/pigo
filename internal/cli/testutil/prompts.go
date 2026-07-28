// Package testutil holds test helpers shared across the internal/cli
// subpackages. Only helpers reused by two or more test files and free of
// cmd/pigo-internal types belong here; helpers that build the concrete replDeps
// aggregate stay with the package that owns it.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// WritePrompt writes a prompt-template .md at home/<sub>/<name> with the given
// body, creating the directory as needed. It fails the test on any I/O error.
func WritePrompt(t *testing.T, home, sub, name, body string) {
	t.Helper()
	d := filepath.Join(home, sub)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
