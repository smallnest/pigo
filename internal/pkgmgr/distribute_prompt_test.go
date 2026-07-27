package pkgmgr

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDistributePromptPromptsDirPreferred verifies the pi-aligned prompts/
// subdir is preferred over the legacy commands/ subdir (#342): only the
// prompts/ templates are installed.
func TestDistributePromptPromptsDirPreferred(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-prompts","version":"1.0.0","pi":{"type":"prompt"}}`, map[string]string{
		"prompts/review.md":  "---\ndescription: review\n---\nReview $ARGUMENTS",
		"commands/legacy.md": "Legacy $ARGUMENTS",
	})

	files, err := DistributePrompt(pkg, "pi-prompts")
	if err != nil {
		t.Fatalf("DistributePrompt: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("created %d files, want 1 (prompts/ preferred over commands/): %v", len(files), files)
	}
	if _, err := os.Stat(filepath.Join(home, "prompts", "review.md")); err != nil {
		t.Errorf("review.md not placed in prompts/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "prompts", "legacy.md")); !os.IsNotExist(err) {
		t.Errorf("legacy.md from commands/ should not be installed when prompts/ exists: %v", err)
	}
}

// TestDistributePromptCommandsDirFallback verifies the legacy commands/ subdir
// is used when prompts/ is absent, installing into ~/.pigo/prompts.
func TestDistributePromptCommandsDirFallback(t *testing.T) {
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
		if _, err := os.Stat(filepath.Join(home, "prompts", want)); err != nil {
			t.Errorf("%s not placed in prompts/: %v", want, err)
		}
	}
	// Root README.md is not copied (commands/ was used, not root).
	if _, err := os.Stat(filepath.Join(home, "prompts", "README.md")); !os.IsNotExist(err) {
		t.Errorf("root README.md leaked into prompts: %v", err)
	}
}

// TestDistributePromptRootFallback verifies root-level *.md are used when
// neither prompts/ nor commands/ exists, skipping README.md, installing into
// ~/.pigo/prompts.
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
	if _, err := os.Stat(filepath.Join(home, "prompts", "summarize.md")); err != nil {
		t.Errorf("summarize.md not placed in prompts/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "prompts", "README.md")); !os.IsNotExist(err) {
		t.Errorf("README.md should be skipped at root fallback: %v", err)
	}
}

// TestDistributePromptNone verifies a package with no prompt templates errors.
func TestDistributePromptNone(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	pkg := writePkg(t, `{"name":"pi-empty","version":"1.0.0"}`, nil)
	if _, err := DistributePrompt(pkg, "pi-empty"); err == nil {
		t.Fatal("DistributePrompt with no templates = nil error, want error")
	}
}

// TestDistributePromptReturnsPromptsPaths verifies the returned paths (recorded
// in the lockfile) are under ~/.pigo/prompts, so uninstall removes precisely
// what was installed.
func TestDistributePromptReturnsPromptsPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	pkg := writePkg(t, `{"name":"pi-p","version":"1.0.0","pi":{"type":"prompt"}}`, map[string]string{
		"prompts/x.md": "X $ARGUMENTS",
	})
	files, err := DistributePrompt(pkg, "pi-p")
	if err != nil {
		t.Fatalf("DistributePrompt: %v", err)
	}
	wantDir := filepath.Join(home, "prompts")
	for _, f := range files {
		if filepath.Dir(f) != wantDir {
			t.Errorf("installed path %q not under %s", f, wantDir)
		}
	}
}
