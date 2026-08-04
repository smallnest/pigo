package run

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePolicySkill drops a minimal valid skill into dir so LoadSkills has
// something to advertise.
func writePolicySkill(t *testing.T, dir, name, description string) {
	t.Helper()
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\nDo the thing."
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill %s: %v", name, err)
	}
}

// setupToolNames runs SetupEnv with a policy and returns the resulting tool
// names. The provider is never contacted, so a stub model id is fine; --no-skills
// keeps the run independent of the machine's skills directory.
func setupToolNames(t *testing.T, policy ToolPolicy) []string {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", t.TempDir()) // isolate plugin/skill discovery
	env, err := SetupEnv("openrouter/free", "", "", "", "", false /*noTools*/, true /*noSkills*/, "", nil, false /*memEnabled*/, policy)
	if err != nil {
		t.Fatalf("SetupEnv: %v", err)
	}
	return names(env.Tools)
}

// contains reports whether name is in the set.
func contains(set []string, name string) bool {
	for _, n := range set {
		if n == name {
			return true
		}
	}
	return false
}

// TestSetupEnvAppliesAllowList confirms a whitelist narrows the advertised set.
// The `task` tool is expected to survive only when explicitly allowed.
func TestSetupEnvAppliesAllowList(t *testing.T) {
	got := setupToolNames(t, NewToolPolicy([]string{"read,grep"}, nil))
	want := []string{"read", "grep"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tool set = %q, want exactly %q", got, want)
	}
}

// TestSetupEnvAppliesDenyList confirms a blacklist removes the named tools while
// leaving everything else — including the side-effect tools not named — in place.
func TestSetupEnvAppliesDenyList(t *testing.T) {
	got := setupToolNames(t, NewToolPolicy(nil, []string{"bash", "bash_output", "kill_bash"}))
	for _, denied := range []string{"bash", "bash_output", "kill_bash"} {
		if contains(got, denied) {
			t.Errorf("%q survived the deny list: %q", denied, got)
		}
	}
	for _, kept := range []string{"read", "write", "edit", "grep"} {
		if !contains(got, kept) {
			t.Errorf("%q was removed but was not denied: %q", kept, got)
		}
	}
}

// TestSetupEnvDenyWinsOverAllow is the fail-closed guarantee: a tool named on
// both sides is removed.
func TestSetupEnvDenyWinsOverAllow(t *testing.T) {
	got := setupToolNames(t, NewToolPolicy([]string{"read", "bash"}, []string{"bash"}))
	if contains(got, "bash") {
		t.Errorf("bash was on both lists and must be removed, got %q", got)
	}
	if !contains(got, "read") {
		t.Errorf("read was allowed and not denied, so it must survive, got %q", got)
	}
}

// TestSetupEnvUnconstrainedIsUnchanged is the zero-regression check: no policy
// means the full built-in set, including the side-effect tools.
func TestSetupEnvUnconstrainedIsUnchanged(t *testing.T) {
	got := setupToolNames(t, ToolPolicy{})
	for _, want := range []string{"read", "write", "edit", "grep", "find", "bash", "todo", "webfetch", "websearch", "task"} {
		if !contains(got, want) {
			t.Errorf("unconstrained run is missing %q: %q", want, got)
		}
	}
}

// TestSetupEnvRejectsUnknownToolName confirms a typo aborts setup with a
// ToolPolicyError, which is what maps to exit code 2 rather than a run that
// silently ignores the boundary.
func TestSetupEnvRejectsUnknownToolName(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", t.TempDir())
	_, err := SetupEnv("openrouter/free", "", "", "", "", false, true, "", nil, false, NewToolPolicy([]string{"raed"}, nil))
	if err == nil {
		t.Fatal("SetupEnv = nil error, want a failure for the misspelled tool name")
	}
	var policyErr *ToolPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("error type = %T, want *ToolPolicyError", err)
	}
}

// TestChildToolSetInheritsPolicy closes the sub-agent escape hatch: a child
// dispatched by the task tool must not regain a tool the parent's policy removed,
// or `--disallowed-tools bash` would be bypassable by delegating.
func TestChildToolSetInheritsPolicy(t *testing.T) {
	child := names(ChildToolSet("/tmp", NewToolPolicy(nil, []string{"bash"})))
	if contains(child, "bash") {
		t.Errorf("child regained the denied bash tool: %q", child)
	}
	if contains(child, "task") {
		t.Errorf("child must not contain task (nesting guard): %q", child)
	}
	if !contains(child, "read") {
		t.Errorf("child lost an un-denied tool: %q", child)
	}

	allowOnly := names(ChildToolSet("/tmp", NewToolPolicy([]string{"read"}, nil)))
	if strings.Join(allowOnly, ",") != "read" {
		t.Errorf("child under an allow list = %q, want exactly [read]", allowOnly)
	}

	// With no policy the child is the plain nesting-guarded builtin set.
	unconstrained := names(ChildToolSet("/tmp", ToolPolicy{}))
	if !contains(unconstrained, "bash") || contains(unconstrained, "task") {
		t.Errorf("unconstrained child set = %q, want builtins minus task", unconstrained)
	}
}

// TestSetupEnvSkillsGatedOnFilteredReadTool covers the ordering dependency: the
// <available_skills> block is advertised only when `read` survives the policy,
// because the model needs read to load a skill body. Filtering must therefore
// happen before the system prompt is built.
func TestSetupEnvSkillsGatedOnFilteredReadTool(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", t.TempDir())
	skillsDir := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", skillsDir)
	writePolicySkill(t, skillsDir, "weather", "get the weather")

	withRead, err := SetupEnv("openrouter/free", "", "", "", "", false, false, "", nil, false, ToolPolicy{})
	if err != nil {
		t.Fatalf("SetupEnv (unconstrained): %v", err)
	}
	if !strings.Contains(withRead.SysPrompt, "<available_skills>") {
		t.Fatal("unconstrained run must advertise skills; the fixture or gate is wrong")
	}

	withoutRead, err := SetupEnv("openrouter/free", "", "", "", "", false, false, "", nil, false, NewToolPolicy(nil, []string{"read"}))
	if err != nil {
		t.Fatalf("SetupEnv (read denied): %v", err)
	}
	if strings.Contains(withoutRead.SysPrompt, "available_skills") {
		t.Error("denying read must suppress <available_skills>: the model could not load a skill body")
	}
}
