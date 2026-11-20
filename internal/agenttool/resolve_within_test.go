package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// symlinkFixture builds target/a.txt plus a sibling symlink link → target, so
// tests can spell one workspace through two different path forms.
func symlinkFixture(t *testing.T) (target, link string) {
	t.Helper()
	parent := t.TempDir()
	target = filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return target, link
}

// TestResolveWithinAcceptsBothSymlinkSpellings: the guard compares physical
// paths, so a workspace spelled through a symlink and through its target is
// one workspace — the model may use either form (macOS's /tmp → /private/tmp
// is the canonical example, and os.Getwd always reports the physical form).
func TestResolveWithinAcceptsBothSymlinkSpellings(t *testing.T) {
	target, link := symlinkFixture(t)
	cases := []struct {
		name string
		root string
		path string
	}{
		{"root as symlink, relative path", link, "a.txt"},
		{"root as symlink, symlink-form abs", link, filepath.Join(link, "a.txt")},
		{"root as symlink, physical abs", link, filepath.Join(target, "a.txt")},
		{"root as target, symlink-form abs", target, filepath.Join(link, "a.txt")},
		{"root as symlink, new file via symlink", link, filepath.Join(link, "new.txt")},
		{"root as target, new file via symlink", target, filepath.Join(link, "new.txt")},
	}
	for _, tc := range cases {
		if _, err := resolveWithin(tc.root, tc.path); err != nil {
			t.Errorf("%s: resolveWithin(%q) = %v, want nil", tc.name, tc.path, err)
		}
	}
}

// TestResolveWithinStillRejectsEscape: the symlink-aware comparison must not
// widen the boundary — lexical traversal and absolute paths outside the root
// stay rejected, and so does a path that only escapes once symlinks resolve.
// The not-yet-created cases matter because WriteTool creates missing parent
// directories: an allowed-but-escaping path would land outside the workspace.
func TestResolveWithinStillRejectsEscape(t *testing.T) {
	target, link := symlinkFixture(t)
	if err := os.Symlink("/etc", filepath.Join(target, "evil")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	// escape points back at the parent directory, which lies outside the
	// workspace root spelled either way (root = link resolves to target).
	escape := filepath.Join(target, "escape")
	if err := os.Symlink(filepath.Dir(target), escape); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	outsideFile := filepath.Join(filepath.Dir(target), "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("out"), 0o644); err != nil {
		t.Fatal(err)
	}

	rejected := map[string]string{
		"absolute path outside":        outsideFile,
		"lexical traversal":            "../../etc/passwd",
		"symlink to /etc, existing":    filepath.Join(link, "evil", "passwd"),
		"escape link, existing target": filepath.Join(escape, "outside.txt"),
		// The not-yet-created escape: only the deepest-existing-ancestor walk
		// catches these; a one-level parent check judges them "inside".
		"escape link, new nested file": filepath.Join(escape, "sub", "new.txt"),
		"escape link, new top file":    filepath.Join(escape, "fresh.txt"),
	}
	for name, p := range rejected {
		if _, err := resolveWithin(link, p); err == nil {
			t.Errorf("%s: resolveWithin(%q) = nil, want outside-the-root error", name, p)
		}
	}
}

// TestWriteToolCannotEscapeThroughSymlink pins the exploit path end to end:
// WriteTool creates missing parent directories, so a guard that only rejects
// already-existing escapes would still land a new file outside the workspace.
func TestWriteToolCannotEscapeThroughSymlink(t *testing.T) {
	target, _ := symlinkFixture(t)
	escape := filepath.Join(target, "escape")
	if err := os.Symlink(filepath.Dir(target), escape); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	tool := &WriteTool{Root: target, Snap: NewFileSnapshotRecorder()}
	args, err := json.Marshal(map[string]string{
		"path":    filepath.Join("escape", "sub", "new.txt"),
		"content": "escaped",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := tool.Execute(context.Background(), "call-1", args, nil)
	if text := agentcore.ContentToText(res.Content); !strings.Contains(text, "outside the workspace root") {
		t.Errorf("write result = %q, want an outside-the-workspace-root rejection", text)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), "sub", "new.txt")); !os.IsNotExist(err) {
		t.Errorf("escape landed: file exists outside the workspace")
	}
}
