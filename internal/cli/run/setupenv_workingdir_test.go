package run

import (
	"os"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agenttool"
)

// TestSetupEnvWithWorkingDir pins the parameter contract added for issue #1:
// a non-empty workingDir overrides the process cwd for the tool roots, Env.Cwd,
// and the system prompt, while an empty one keeps the process cwd.
func TestSetupEnvWithWorkingDir(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", t.TempDir())
	dir := t.TempDir()

	env, err := SetupEnv("openrouter/free", "", "", "", "", false, true, "", nil, false, ToolPolicy{}, dir)
	if err != nil {
		t.Fatalf("SetupEnv(workingDir=%q) = %v, want nil", dir, err)
	}
	if env.Cwd != dir {
		t.Errorf("Env.Cwd = %q, want %q", env.Cwd, dir)
	}
	for name, root := range map[string]string{
		"read":  toolRoot(t, env, "read"),
		"write": toolRoot(t, env, "write"),
		"edit":  toolRoot(t, env, "edit"),
		"grep":  toolRoot(t, env, "grep"),
		"find":  toolRoot(t, env, "find"),
		"bash":  toolRoot(t, env, "bash"),
	} {
		if root != dir {
			t.Errorf("%s root = %q, want %q", name, root, dir)
		}
	}
	if !strings.Contains(env.SysPrompt, "- Working directory: "+dir+"\n") {
		t.Errorf("system prompt should anchor its Environment block at %q", dir)
	}
}

// TestSetupEnvEmptyWorkingDirKeepsProcessCwd guards the backward-compatible
// path used by the CLI: an empty workingDir means the process cwd, whatever a
// previous os.Chdir left it at.
func TestSetupEnvEmptyWorkingDirKeepsProcessCwd(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", t.TempDir())
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })
	// os.Getwd resolves the /var symlink on macOS, so the expectation must come
	// from Getwd itself rather than the raw t.TempDir string.
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	env, err := SetupEnv("openrouter/free", "", "", "", "", false, true, "", nil, false, ToolPolicy{}, "")
	if err != nil {
		t.Fatalf("SetupEnv(workingDir=\"\") = %v, want nil", err)
	}
	if env.Cwd != want {
		t.Errorf("Env.Cwd = %q, want process cwd %q", env.Cwd, want)
	}
}

// toolRoot extracts the workspace root of the named built-in tool, failing the
// test when the tool is missing or has an unexpected concrete type.
func toolRoot(t *testing.T, env Env, name string) string {
	t.Helper()
	for _, tool := range env.Tools {
		if !strings.EqualFold(tool.Name(), name) {
			continue
		}
		switch tt := tool.(type) {
		case *agenttool.ReadTool:
			return tt.Root
		case *agenttool.WriteTool:
			return tt.Root
		case *agenttool.EditTool:
			return tt.Root
		case *agenttool.GrepTool:
			return tt.Root
		case *agenttool.FindTool:
			return tt.Root
		case *agenttool.BashTool:
			return tt.Dir
		default:
			t.Fatalf("tool %q has unexpected type %T", name, tool)
		}
	}
	t.Fatalf("tool %q not found in env.Tools", name)
	return ""
}
