package pkgmgr

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDistributePromptCommandsDir verifies *.md under the package's commands/
// dir land in $PIGO_HOME/commands, discoverable by LoadUserCommandsDir.
func TestDistributePromptCommandsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-prompts","version":"1.0.0","pi":{"type":"prompt"}}`, map[string]string{
		"commands/review.md":  "---\ndescription: review\n---\nReview $ARGUMENTS",
		"commands/explain.md": "Explain $ARGUMENTS",
		"README.md":           "readme",
	})

	files, err := DistributePrompt(pkg, "pi-prompts")
	if err != nil {
		t.Fatalf("DistributePrompt: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("created %d files, want 2: %v", len(files), files)
	}
	for _, want := range []string{"review.md", "explain.md"} {
		if _, err := os.Stat(filepath.Join(home, "commands", want)); err != nil {
			t.Errorf("%s not placed: %v", want, err)
		}
	}
	// README.md under commands/ isn't special — but it's not present here; the
	// root README.md must NOT be copied (we used the commands/ dir).
	if _, err := os.Stat(filepath.Join(home, "commands", "README.md")); !os.IsNotExist(err) {
		t.Errorf("root README.md leaked into commands: %v", err)
	}
}

// TestDistributePromptRootFallback verifies root-level *.md are used when there
// is no commands/ dir, skipping README.md.
func TestDistributePromptRootFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-p","version":"1.0.0","pi":{"type":"prompt"}}`, map[string]string{
		"summarize.md": "Summarize $ARGUMENTS",
		"README.md":    "readme",
	})

	files, err := DistributePrompt(pkg, "pi-p")
	if err != nil {
		t.Fatalf("DistributePrompt: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("created %d files, want 1: %v", len(files), files)
	}
	if _, err := os.Stat(filepath.Join(home, "commands", "summarize.md")); err != nil {
		t.Errorf("summarize.md not placed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "commands", "README.md")); !os.IsNotExist(err) {
		t.Errorf("README.md should be skipped at root fallback: %v", err)
	}
}

// TestDistributePromptNone verifies a package with no command templates errors.
func TestDistributePromptNone(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	pkg := writePkg(t, `{"name":"pi-empty","version":"1.0.0"}`, nil)
	if _, err := DistributePrompt(pkg, "pi-empty"); err == nil {
		t.Fatal("DistributePrompt with no templates = nil error, want error")
	}
}
