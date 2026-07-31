package runtime

// Tests for system-prompt assembly (US-021, #40): the base instruction, the
// environment block, and — the acceptance-critical part — the general-to-
// specific ordering of AGENTS.md injection from a root directory down to the
// working directory. AGENTS.md layout is faked via PromptConfig.ReadFile so the
// ordering is asserted without touching disk.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedTime is a deterministic clock for the environment block.
func fixedTime() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }

// TestBuildSystemPromptBaseAndEnv verifies the base instruction and environment
// block (cwd, OS, date) are present, with no AGENTS.md when none exist.
func TestBuildSystemPromptBaseAndEnv(t *testing.T) {
	got, err := BuildSystemPrompt(PromptConfig{
		WorkingDir: "/work/proj",
		Now:        fixedTime,
		ReadFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	if !strings.HasPrefix(got, DefaultBaseInstruction) {
		t.Errorf("prompt should start with the default base instruction, got:\n%s", got)
	}
	if !strings.Contains(got, "Working directory: /work/proj") {
		t.Errorf("environment block missing working directory:\n%s", got)
	}
	if !strings.Contains(got, "Date: 2026-07-10") {
		t.Errorf("environment block missing date:\n%s", got)
	}
	if strings.Contains(got, "Project instructions") {
		t.Errorf("no AGENTS.md exists, but prompt injected one:\n%s", got)
	}
}

// TestBuildSystemPromptAdvertisesTaskFanout verifies the base instruction tells
// the model about the generic task tool: that it dispatches an independent
// sub-agent (delegation) and that multiple task calls in one message run in
// parallel (fan-out, US-008/#458).
func TestBuildSystemPromptAdvertisesTaskFanout(t *testing.T) {
	got, err := BuildSystemPrompt(PromptConfig{
		WorkingDir: "/work/proj",
		Now:        fixedTime,
		ReadFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	lower := strings.ToLower(got)
	if !strings.Contains(lower, "task tool") {
		t.Errorf("prompt should advertise the task tool:\n%s", got)
	}
	if !strings.Contains(lower, "sub-agent") || !strings.Contains(lower, "independent") {
		t.Errorf("prompt should describe the task tool as an independent sub-agent (delegation):\n%s", got)
	}
	if !strings.Contains(lower, "parallel") {
		t.Errorf("prompt should state that multiple task calls run in parallel (fan-out):\n%s", got)
	}
}

// TestBuildSystemPromptAGENTSOrdering is the acceptance-critical test: with an
// AGENTS.md at the root and at a nested working directory, the root's content
// must appear BEFORE the nested one (general → specific).
func TestBuildSystemPromptAGENTSOrdering(t *testing.T) {
	root := filepath.Clean("/repo")
	mid := filepath.Join(root, "services")
	wd := filepath.Join(mid, "api")

	files := map[string]string{
		filepath.Join(root, agentsFileName): "ROOT CONVENTIONS",
		filepath.Join(mid, agentsFileName):  "SERVICES CONVENTIONS",
		filepath.Join(wd, agentsFileName):   "API CONVENTIONS",
	}
	got, err := BuildSystemPrompt(PromptConfig{
		WorkingDir: wd,
		Root:       root,
		Now:        fixedTime,
		ReadFile: func(path string) ([]byte, error) {
			if c, ok := files[path]; ok {
				return []byte(c), nil
			}
			return nil, os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}

	iRoot := strings.Index(got, "ROOT CONVENTIONS")
	iMid := strings.Index(got, "SERVICES CONVENTIONS")
	iAPI := strings.Index(got, "API CONVENTIONS")
	if iRoot < 0 || iMid < 0 || iAPI < 0 {
		t.Fatalf("all three AGENTS.md must be injected, got:\n%s", got)
	}
	if !(iRoot < iMid && iMid < iAPI) {
		t.Errorf("AGENTS.md must be ordered general→specific (root<mid<api), got positions root=%d mid=%d api=%d", iRoot, iMid, iAPI)
	}
}

// TestBuildSystemPromptSkipsMissingIntermediate verifies a missing intermediate
// AGENTS.md is skipped without breaking the ordering of the present ones.
func TestBuildSystemPromptSkipsMissingIntermediate(t *testing.T) {
	root := filepath.Clean("/repo")
	mid := filepath.Join(root, "services")
	wd := filepath.Join(mid, "api")
	files := map[string]string{
		filepath.Join(root, agentsFileName): "ROOT ONLY",
		filepath.Join(wd, agentsFileName):   "API ONLY",
		// no AGENTS.md at mid
	}
	got, err := BuildSystemPrompt(PromptConfig{
		WorkingDir: wd, Root: root, Now: fixedTime,
		ReadFile: func(path string) ([]byte, error) {
			if c, ok := files[path]; ok {
				return []byte(c), nil
			}
			return nil, os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	iRoot := strings.Index(got, "ROOT ONLY")
	iAPI := strings.Index(got, "API ONLY")
	if iRoot < 0 || iAPI < 0 || iRoot >= iAPI {
		t.Errorf("present AGENTS.md must stay ordered root<api, got root=%d api=%d in:\n%s", iRoot, iAPI, got)
	}
}

// TestBuildSystemPromptNoRootOnlyWorkingDir verifies that with no Root, only the
// working directory's own AGENTS.md is considered (no ancestor walk).
func TestBuildSystemPromptNoRootOnlyWorkingDir(t *testing.T) {
	wd := filepath.Clean("/repo/services/api")
	ancestor := filepath.Join(filepath.Dir(wd), agentsFileName)
	files := map[string]string{
		filepath.Join(wd, agentsFileName): "WD ONLY",
		ancestor:                          "ANCESTOR (should NOT appear)",
	}
	got, err := BuildSystemPrompt(PromptConfig{
		WorkingDir: wd, Now: fixedTime,
		ReadFile: func(path string) ([]byte, error) {
			if c, ok := files[path]; ok {
				return []byte(c), nil
			}
			return nil, os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	if !strings.Contains(got, "WD ONLY") {
		t.Errorf("working-dir AGENTS.md must be injected:\n%s", got)
	}
	if strings.Contains(got, "ANCESTOR") {
		t.Errorf("with no Root, ancestor AGENTS.md must not be walked:\n%s", got)
	}
}

// TestBuildSystemPromptReadErrorSurfaces verifies a present-but-unreadable
// AGENTS.md (a non-not-exist I/O error) is reported rather than silently
// dropped.
func TestBuildSystemPromptReadErrorSurfaces(t *testing.T) {
	wd := filepath.Clean("/repo")
	_, err := BuildSystemPrompt(PromptConfig{
		WorkingDir: wd, Now: fixedTime,
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrPermission },
	})
	if err == nil {
		t.Fatal("an unreadable AGENTS.md must surface an error, got nil")
	}
}

// TestBuildSystemPromptBaseInstructionOverride verifies a non-empty
// BaseInstruction replaces the default coding-assistant prompt (对标 pi 的
// --system-prompt) while the environment block still follows it.
func TestBuildSystemPromptBaseInstructionOverride(t *testing.T) {
	got, err := BuildSystemPrompt(PromptConfig{
		BaseInstruction: "You are a haiku poet.",
		WorkingDir:      "/work/proj",
		Now:             fixedTime,
		ReadFile:        func(string) ([]byte, error) { return nil, os.ErrNotExist },
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	if !strings.HasPrefix(got, "You are a haiku poet.") {
		t.Errorf("custom base instruction should lead the prompt, got:\n%s", got)
	}
	if strings.Contains(got, DefaultBaseInstruction) {
		t.Errorf("default base instruction must not appear when overridden:\n%s", got)
	}
	if !strings.Contains(got, "Working directory: /work/proj") {
		t.Errorf("environment block must still follow the custom base:\n%s", got)
	}
}

// TestBuildSystemPromptAppendInstructions verifies --append-system-prompt
// entries are layered onto the end of the prompt in order, after the base
// instruction and environment block, with empty entries skipped.
func TestBuildSystemPromptAppendInstructions(t *testing.T) {
	got, err := BuildSystemPrompt(PromptConfig{
		WorkingDir:         "/work/proj",
		Now:                fixedTime,
		AppendInstructions: []string{"FIRST APPEND", "   ", "SECOND APPEND"},
		ReadFile:           func(string) ([]byte, error) { return nil, os.ErrNotExist },
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	iEnv := strings.Index(got, "Working directory")
	iFirst := strings.Index(got, "FIRST APPEND")
	iSecond := strings.Index(got, "SECOND APPEND")
	if iFirst < 0 || iSecond < 0 {
		t.Fatalf("both appended instructions must be present, got:\n%s", got)
	}
	if !(iEnv < iFirst && iFirst < iSecond) {
		t.Errorf("appends must follow the env block and keep order (env<first<second), got env=%d first=%d second=%d", iEnv, iFirst, iSecond)
	}
}

// TestBuildSystemPromptInjectsSkills verifies the <available_skills> block is
// appended after the base/env/append layers when the read tool is available.
func TestBuildSystemPromptInjectsSkills(t *testing.T) {
	skills := []*Skill{
		{Frontmatter: SkillFrontmatter{Name: "weather", Description: "get weather"}, Path: "/skills/weather.md"},
		{Frontmatter: SkillFrontmatter{Name: "secret", Description: "hidden", DisableModelInvocation: true}, Path: "/skills/secret.md"},
	}
	got, err := BuildSystemPrompt(PromptConfig{
		WorkingDir:        "/work/proj",
		Now:               fixedTime,
		ReadFile:          func(string) ([]byte, error) { return nil, os.ErrNotExist },
		Skills:            skills,
		ReadToolAvailable: true,
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	if !strings.Contains(got, "<available_skills>") || !strings.Contains(got, "<name>weather</name>") {
		t.Errorf("skills block must be injected, got:\n%s", got)
	}
	if strings.Contains(got, "secret") {
		t.Errorf("disable-model-invocation skill must not appear, got:\n%s", got)
	}
	iEnv := strings.Index(got, "Working directory")
	iSkills := strings.Index(got, "<available_skills>")
	if !(iEnv < iSkills) {
		t.Errorf("skills block must come after env block, env=%d skills=%d", iEnv, iSkills)
	}
}

// TestBuildSystemPromptNoSkillsWithoutReadTool verifies skills are NOT injected
// when the read tool is unavailable, and that a skill-free prompt is unchanged.
func TestBuildSystemPromptNoSkillsWithoutReadTool(t *testing.T) {
	skills := []*Skill{{Frontmatter: SkillFrontmatter{Name: "weather", Description: "d"}, Path: "/s/weather.md"}}
	withTool, _ := BuildSystemPrompt(PromptConfig{WorkingDir: "/w", Now: fixedTime, ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }, Skills: skills, ReadToolAvailable: false})
	if strings.Contains(withTool, "available_skills") {
		t.Errorf("no read tool → no skills block, got:\n%s", withTool)
	}
	// A prompt with no skills at all must equal one with read tool but empty list.
	bare, _ := BuildSystemPrompt(PromptConfig{WorkingDir: "/w", Now: fixedTime, ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }})
	withReadNoSkills, _ := BuildSystemPrompt(PromptConfig{WorkingDir: "/w", Now: fixedTime, ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }, ReadToolAvailable: true})
	if bare != withReadNoSkills {
		t.Errorf("empty skill list must not alter the prompt even with read tool:\n%q\nvs\n%q", bare, withReadNoSkills)
	}
}

// TestDisableModelInvocationCoexistence verifies the #305 coexistence contract:
// a skill with disable-model-invocation:true is STILL exposed as a /skill-name
// slash command (body expansion + $ARGUMENTS), while being EXCLUDED from the
// <available_skills> prompt injection. The two invocation paths are independent.
func TestDisableModelInvocationCoexistence(t *testing.T) {
	disabled := &Skill{
		Frontmatter: SkillFrontmatter{Name: "secret", Description: "hidden", DisableModelInvocation: true},
		Path:        "/skills/secret.md",
		Body:        "Do the secret thing with $ARGUMENTS.",
	}
	enabled := &Skill{
		Frontmatter: SkillFrontmatter{Name: "weather", Description: "get weather"},
		Path:        "/skills/weather.md",
		Body:        "Report the weather.",
	}
	skills := []*Skill{disabled, enabled}

	// 1. The disabled skill must be excluded from the model-facing prompt block,
	//    while the enabled one appears.
	block := FormatSkillsForPrompt(skills)
	if strings.Contains(block, "secret") {
		t.Errorf("disable-model-invocation skill must not appear in <available_skills>, got:\n%s", block)
	}
	if !strings.Contains(block, "<name>weather</name>") {
		t.Errorf("model-invocable skill must appear in <available_skills>, got:\n%s", block)
	}

	// 2. The disabled skill must still be invocable via its /skill-name command,
	//    with $ARGUMENTS substitution intact (behavior identical to an enabled one).
	cmd := disabled.SlashCommand()
	if cmd.Name != "secret" {
		t.Errorf("disabled skill slash name = %q, want secret", cmd.Name)
	}
	if cmd.Expand == nil {
		t.Fatal("disabled skill must expose a prompt command (Expand != nil)")
	}
	if got := cmd.Expand("now"); got != "Do the secret thing with now." {
		t.Errorf("Expand(now) = %q, want $ARGUMENTS substituted", got)
	}
}
