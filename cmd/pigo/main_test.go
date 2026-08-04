package main

// Tests for the thin CLI entry point: the dispatch seam (options+writers →
// exit code), the config.toml overlay (applyFileConfig precedence), and the
// settings-tier prompts pass-through. These exercise the branching without
// spawning a provider or re-parsing the global flag set.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/cli/run"
)

// --- dispatch seam ---

// TestDispatchListSessionsEmpty verifies --list-sessions is a standalone action
// that succeeds (exit 0) and prints the empty-store message, using an isolated
// PIGO_HOME so it never touches the real session store.
func TestDispatchListSessionsEmpty(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	code := dispatch(context.Background(), cliOptions{listSessions: true}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (errOut=%q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "no sessions") {
		t.Errorf("out = %q, want the empty-store message", out.String())
	}
}

// TestDispatchContinueNoSessions verifies --continue with an empty store is an
// error (exit 1) that says there is nothing to continue, rather than starting a
// blank REPL.
func TestDispatchContinueNoSessions(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	// Ensure a non-terminal path is not taken before the continue guard: continue
	// resolves the id first and errors when the store is empty.
	var out, errOut bytes.Buffer
	code := dispatch(context.Background(), cliOptions{continueLast: true}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (errOut=%q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no sessions to continue") {
		t.Errorf("errOut = %q, want the no-sessions-to-continue message", errOut.String())
	}
}

// TestDispatchNoPromptNonTerminal verifies the CI/pipe guard: no prompt, no
// resume, and a non-terminal stdout is a usage error (exit 2) with a diagnostic
// on errOut — reachable now that dispatch takes its writers as parameters.
func TestDispatchNoPromptNonTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(context.Background(), cliOptions{}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "no prompt") {
		t.Errorf("errOut = %q, want it to mention the missing prompt", errOut.String())
	}
}

// TestDispatchBadOutputFormat verifies an unknown --output-format is rejected
// (exit 2) before any provider work, naming the offending value.
func TestDispatchBadOutputFormat(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(context.Background(), cliOptions{prompt: "hi", outputFmt: "yaml"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "yaml") {
		t.Errorf("errOut = %q, want it to name the bad format", errOut.String())
	}
}

// TestDispatchTUIGating verifies the pure entry-gating predicate shouldUseTUI
// (US-001, SPEC 4.2/5.2) that dispatch uses to choose the full-screen TUI vs the
// line-based REPL on the no-prompt path. The decision is tested directly so it
// needs no real TTY and never spawns Bubble Tea: TUI only when stdout is a TTY
// and --no-tui is unset; --no-tui or a non-terminal stdout always forces REPL.
func TestDispatchTUIGating(t *testing.T) {
	tests := []struct {
		name  string
		opts  cliOptions
		isTTY bool
		want  bool
	}{
		{name: "TTY and no flag uses TUI", opts: cliOptions{}, isTTY: true, want: true},
		{name: "--no-tui forces REPL on a TTY", opts: cliOptions{noTUI: true}, isTTY: true, want: false},
		{name: "non-TTY never uses TUI", opts: cliOptions{}, isTTY: false, want: false},
		{name: "non-TTY with --no-tui stays REPL", opts: cliOptions{noTUI: true}, isTTY: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseTUI(tt.opts, tt.isTTY); got != tt.want {
				t.Errorf("shouldUseTUI(%+v, %v) = %v, want %v", tt.opts, tt.isTTY, got, tt.want)
			}
		})
	}
}

// TestCwdChdirRootsEnv verifies the guarantee --cwd relies on: after the
// process working directory is switched (what the --cwd flag does via os.Chdir),
// run.SetupEnv roots the run — and thus the built-in file tools — at that
// directory. This is the contract that lets pigo be pointed at an arbitrary
// project root as an SDK backend. It exercises the downstream effect rather than
// re-parsing flags, since the chdir itself lives in main().
func TestCwdChdirRootsEnv(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	// macOS temp dirs are symlinks (/tmp → /private/tmp); os.Getwd resolves them,
	// so compare against the resolved form.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	env, err := run.SetupEnv("openrouter/free", "", "", "", "", true /*noTools*/, true /*noSkills*/, "", nil, false /*memEnabled*/, run.ToolPolicy{})
	if err != nil {
		t.Fatalf("SetupEnv: %v", err)
	}
	if env.Cwd != want {
		t.Errorf("env.Cwd = %q, want %q (the chdir'd directory)", env.Cwd, want)
	}
}

// TestUpdateIsSelfUpdate verifies the US-003 `pigo update` routing classifier:
// no positional package name (including flags-only invocations like
// `pigo update --check`) routes to binary self-update (true); any positional
// package name routes to pkgmgr package-update (false). Tested directly so the
// dispatch split needs no argv parsing or spawning either update path.
func TestUpdateIsSelfUpdate(t *testing.T) {
	tests := []struct {
		name string
		rest []string
		want bool
	}{
		{name: "no args is self-update", rest: nil, want: true},
		{name: "empty slice is self-update", rest: []string{}, want: true},
		{name: "flags-only is self-update", rest: []string{"--check"}, want: true},
		{name: "multiple flags is self-update", rest: []string{"--check", "-v"}, want: true},
		{name: "single package name is package-update", rest: []string{"pi-mcp-adapter"}, want: false},
		{name: "multiple package names is package-update", rest: []string{"a", "b"}, want: false},
		{name: "flag then package name is package-update", rest: []string{"--check", "pkg"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateIsSelfUpdate(tt.rest); got != tt.want {
				t.Errorf("updateIsSelfUpdate(%v) = %v, want %v", tt.rest, got, tt.want)
			}
		})
	}
}

// --- config.toml overlay ---

// changedSet turns a set of flag names into a lookup func for applyFileConfig.
func changedSet(names ...string) func(string) bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return func(name string) bool { return set[name] }
}

func TestApplyFileConfig_FillsUnsetFlags(t *testing.T) {
	opts := cliOptions{model: "openrouter/free", outputFmt: "text"}
	cfg := config.FileConfig{
		Model:         "claude-opus-4-8",
		BaseURL:       "https://example.com",
		APIKey:        "sk-test",
		Protocol:      "anthropic",
		Provider:      "deepseek",
		ThinkingLevel: "high",
		OutputFormat:  "stream-json",
		NoTools:       true,
		NoSkills:      true,
		Approve:       true,
		SystemPrompt:  "be terse",
	}
	applyFileConfig(&opts, cfg, changedSet())

	if opts.model != "claude-opus-4-8" {
		t.Errorf("model = %q, want claude-opus-4-8", opts.model)
	}
	if opts.baseURL != "https://example.com" {
		t.Errorf("baseURL = %q", opts.baseURL)
	}
	if opts.apiKey != "sk-test" {
		t.Errorf("apiKey = %q", opts.apiKey)
	}
	if opts.protocol != "anthropic" {
		t.Errorf("protocol = %q", opts.protocol)
	}
	if opts.provider != "deepseek" {
		t.Errorf("provider = %q", opts.provider)
	}
	if opts.thinkingLevel != "high" {
		t.Errorf("thinkingLevel = %q", opts.thinkingLevel)
	}
	if opts.outputFmt != "stream-json" {
		t.Errorf("outputFmt = %q", opts.outputFmt)
	}
	if !opts.noTools || !opts.noSkills || !opts.approve {
		t.Errorf("bool flags not applied: %+v", opts)
	}
	if opts.systemPrompt != "be terse" {
		t.Errorf("systemPrompt = %q", opts.systemPrompt)
	}
}

func TestApplyFileConfig_CLIWins(t *testing.T) {
	opts := cliOptions{model: "cli-model", outputFmt: "text"}
	cfg := config.FileConfig{Model: "config-model", OutputFormat: "stream-json"}
	// --model was set on the command line; --output-format was not.
	applyFileConfig(&opts, cfg, changedSet("model"))

	if opts.model != "cli-model" {
		t.Errorf("CLI model should win, got %q", opts.model)
	}
	if opts.outputFmt != "stream-json" {
		t.Errorf("unset output-format should take config value, got %q", opts.outputFmt)
	}
}

func TestApplyFileConfig_EmptyConfigNoChange(t *testing.T) {
	opts := cliOptions{model: "openrouter/free", outputFmt: "text"}
	applyFileConfig(&opts, config.FileConfig{}, changedSet())
	if opts.model != "openrouter/free" || opts.outputFmt != "text" {
		t.Fatalf("empty config should not change opts, got %+v", opts)
	}
	if opts.baseURL != "" || opts.provider != "" || opts.noTools {
		t.Fatalf("empty config should leave unset fields empty, got %+v", opts)
	}
}

func TestApplyFileConfigPrompts(t *testing.T) {
	var opts cliOptions
	cfg := config.FileConfig{Prompts: []string{"./my-prompts", "/abs/x.md"}}
	applyFileConfig(&opts, cfg, func(string) bool { return false })
	if len(opts.configPrompts) != 2 || opts.configPrompts[0] != "./my-prompts" || opts.configPrompts[1] != "/abs/x.md" {
		t.Errorf("opts.configPrompts = %v, want [./my-prompts /abs/x.md]", opts.configPrompts)
	}
}

// The tool boundary follows CLI > file > default like the other scalar flags,
// rather than the additive treatment `prompts` gets: merging would prevent a CLI
// flag from widening a boundary the config file narrowed.
func TestApplyFileConfigToolPolicy(t *testing.T) {
	t.Run("fills unset flags", func(t *testing.T) {
		var opts cliOptions
		cfg := config.FileConfig{
			AllowedTools:    []string{"read", "grep"},
			DisallowedTools: []string{"bash"},
		}
		applyFileConfig(&opts, cfg, changedSet())
		if len(opts.allowedTools) != 2 || opts.allowedTools[0] != "read" {
			t.Errorf("allowedTools = %v, want [read grep]", opts.allowedTools)
		}
		if len(opts.disallowedTools) != 1 || opts.disallowedTools[0] != "bash" {
			t.Errorf("disallowedTools = %v, want [bash]", opts.disallowedTools)
		}
	})

	t.Run("CLI replaces file value wholesale", func(t *testing.T) {
		opts := cliOptions{allowedTools: []string{"bash"}}
		cfg := config.FileConfig{AllowedTools: []string{"read"}, DisallowedTools: []string{"write"}}
		applyFileConfig(&opts, cfg, changedSet("allowed-tools"))
		if len(opts.allowedTools) != 1 || opts.allowedTools[0] != "bash" {
			t.Errorf("CLI --allowed-tools must win outright, got %v", opts.allowedTools)
		}
		if len(opts.disallowedTools) != 1 || opts.disallowedTools[0] != "write" {
			t.Errorf("unset --disallowed-tools should take the config value, got %v", opts.disallowedTools)
		}
	})

	t.Run("absent config leaves the boundary open", func(t *testing.T) {
		var opts cliOptions
		applyFileConfig(&opts, config.FileConfig{}, changedSet())
		if opts.allowedTools != nil || opts.disallowedTools != nil {
			t.Errorf("empty config must not constrain tools, got %v / %v", opts.allowedTools, opts.disallowedTools)
		}
	})
}

// setupExitCode maps a bad tool policy to the usage exit code (2) and everything
// else to a runtime failure (1), so a typo is distinguishable from e.g. a
// provider-resolution error.
func TestSetupExitCode(t *testing.T) {
	if got := setupExitCode(&run.ToolPolicyError{UnknownAllowed: []string{"raed"}}); got != 2 {
		t.Errorf("setupExitCode(ToolPolicyError) = %d, want 2 (usage error)", got)
	}
	if got := setupExitCode(errors.New("provider boom")); got != 1 {
		t.Errorf("setupExitCode(generic) = %d, want 1", got)
	}
	if got := setupExitCode(fmt.Errorf("wrapped: %w", &run.ToolPolicyError{})); got != 2 {
		t.Errorf("setupExitCode must unwrap, got %d, want 2", got)
	}
}

// applyFileConfig always resolves the [memory]/[checkpoint]/[compaction] tables
// into opts.memory, applying defaults when they are absent.
func TestApplyFileConfig_MemoryDefaults(t *testing.T) {
	var opts cliOptions
	applyFileConfig(&opts, config.FileConfig{}, changedSet())
	if !opts.memory.Memory.Enabled || !opts.memory.Memory.ReconcileOnSearch {
		t.Errorf("memory defaults not applied: %+v", opts.memory.Memory)
	}
	if opts.memory.Memory.SearchScoreFloor != 0.15 {
		t.Errorf("search_score_floor default = %v, want 0.15", opts.memory.Memory.SearchScoreFloor)
	}
	if len(opts.memory.CheckpointThresholds) != 3 {
		t.Errorf("checkpoint thresholds default = %v, want 3 entries", opts.memory.CheckpointThresholds)
	}
	if opts.memory.MaxContext.IsSet() {
		t.Errorf("max_context should be unset by default")
	}
}

// A configured [memory]/[compaction] set overlays into opts.memory.
func TestApplyFileConfig_MemoryOverride(t *testing.T) {
	var opts cliOptions
	enabled := false
	cfg := config.FileConfig{
		Memory:     config.MemoryConfig{Enabled: &enabled},
		Compaction: config.CompactionConfig{MaxContext: "50%"},
	}
	applyFileConfig(&opts, cfg, changedSet())
	if opts.memory.Memory.Enabled {
		t.Errorf("memory.enabled=false should overlay, got true")
	}
	if got := opts.memory.MaxContext.Resolve(200000); got != 100000 {
		t.Errorf("max_context 50%% of 200000 = %d, want 100000", got)
	}
}
