package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agenttool"
)

// hermetic mirrors the black-box helper in agent_test.go: provider/skill/plugin
// discovery points at throwaway dirs and a dummy key lets New resolve fully
// without network access.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", t.TempDir())
}

// rootedTools maps the built-in workspace-bound tools to the directory they
// were rooted at, for white-box assertions on how WithWorkingDir propagates
// through New into the assembled tool set.
func rootedTools(t *testing.T, sess *Session) map[string]string {
	t.Helper()
	roots := map[string]string{}
	for _, tool := range sess.env.Tools {
		switch tt := tool.(type) {
		case *agenttool.ReadTool:
			roots["read"] = tt.Root
		case *agenttool.WriteTool:
			roots["write"] = tt.Root
		case *agenttool.EditTool:
			roots["edit"] = tt.Root
		case *agenttool.GrepTool:
			roots["grep"] = tt.Root
		case *agenttool.FindTool:
			roots["find"] = tt.Root
		case *agenttool.BashTool:
			roots["bash"] = tt.Dir
		}
	}
	return roots
}

// TestWithWorkingDirRootsToolSet is the core contract of issue #1: every
// workspace-bound built-in tool, the bash working directory, and the system
// prompt anchor at the session directory instead of the process cwd. The
// session dir is canonicalized (symlink-resolved), so expectations use the
// canonical form — on macOS t.TempDir()'s /var path resolves to /private/var.
func TestWithWorkingDirRootsToolSet(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("session-agents-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := New(WithWorkingDir(dir))
	if err != nil {
		t.Fatalf("New(WithWorkingDir(%q)) = %v, want nil", dir, err)
	}
	defer sess.Close()

	for name, root := range rootedTools(t, sess) {
		if root != canonical {
			t.Errorf("%s tool root = %q, want canonical session dir %q", name, root, canonical)
		}
	}
	if got := sess.env.Cwd; got != canonical {
		t.Errorf("env.Cwd = %q, want %q", got, canonical)
	}
	if got := sess.WorkingDir(); got != canonical {
		t.Errorf("WorkingDir() = %q, want %q", got, canonical)
	}
	if !strings.Contains(sess.env.SysPrompt, "- Working directory: "+canonical+"\n") {
		t.Errorf("system prompt should anchor its Environment block at the session dir %q", canonical)
	}
	if !strings.Contains(sess.env.SysPrompt, "session-agents-content") {
		t.Errorf("system prompt should include the session dir's AGENTS.md")
	}
}

// TestWithWorkingDirCanonicalizesSymlink: a symlinked session directory roots
// the session at the physical target — the same form the OS reports
// (os.Getwd resolves symlinks) and the form resolveWithin compares against,
// so neither spelling of a path is rejected by the workspace guard.
func TestWithWorkingDirCanonicalizesSymlink(t *testing.T) {
	hermetic(t)
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := New(WithWorkingDir(link))
	if err != nil {
		t.Fatalf("New(WithWorkingDir(%q)) = %v, want nil", link, err)
	}
	defer sess.Close()

	if got := sess.WorkingDir(); got != canonical {
		t.Errorf("WorkingDir() = %q, want canonical target %q", got, canonical)
	}
	if got := rootedTools(t, sess)["read"]; got != canonical {
		t.Errorf("read tool root = %q, want canonical target %q", got, canonical)
	}
}

// TestWithWorkingDirDoesNotWalkAboveSessionDir: the AGENTS.md chain is
// anchored at the session dir, so a parent's AGENTS.md must not leak in.
func TestWithWorkingDirDoesNotWalkAboveSessionDir(t *testing.T) {
	hermetic(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "child")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "AGENTS.md"), []byte("parent-agents-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := New(WithWorkingDir(dir))
	if err != nil {
		t.Fatalf("New(WithWorkingDir(%q)) = %v, want nil", dir, err)
	}
	defer sess.Close()

	if strings.Contains(sess.env.SysPrompt, "parent-agents-content") {
		t.Errorf("system prompt must not include AGENTS.md from above the session dir")
	}
}

// TestWithWorkingDirResolvesRelativePath: a relative dir is normalized to an
// absolute path at New, so tool roots do not silently depend on the caller's
// cwd changing later.
func TestWithWorkingDirResolvesRelativePath(t *testing.T) {
	hermetic(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "child")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })
	// filepath.Abs (used by New) builds on os.Getwd, which on some platforms
	// (macOS) resolves the /var symlink — so the expectation must come from
	// Getwd, not from the raw t.TempDir string.
	resolvedParent, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolvedParent, "child")

	sess, err := New(WithWorkingDir("child"))
	if err != nil {
		t.Fatalf("New(WithWorkingDir(\"child\")) = %v, want nil", err)
	}
	defer sess.Close()

	if got := rootedTools(t, sess)["read"]; got != want {
		t.Errorf("read tool root = %q, want absolute %q", got, want)
	}
}

// TestDefaultWorkingDirIsProcessCwd: without WithWorkingDir the tool set
// stays rooted at the process working directory, preserving pre-#1 behavior.
func TestDefaultWorkingDirIsProcessCwd(t *testing.T) {
	hermetic(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	sess, err := New()
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer sess.Close()

	if got := rootedTools(t, sess)["read"]; got != cwd {
		t.Errorf("read tool root = %q, want process cwd %q", got, cwd)
	}
}
