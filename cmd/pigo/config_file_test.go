package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFileConfigPath_XDGOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgroot")
	got := fileConfigPath()
	want := filepath.Join("/tmp/xdgroot", "pigo", "config.toml")
	if got != want {
		t.Fatalf("fileConfigPath() = %q, want %q", got, want)
	}
}

func TestFileConfigPath_DefaultHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := fileConfigPath()
	want := filepath.Join(home, ".config", "pigo", "config.toml")
	if got != want {
		t.Fatalf("fileConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadFileConfig_Missing(t *testing.T) {
	cfg, err := loadFileConfig(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if !reflect.DeepEqual(cfg, fileConfig{}) {
		t.Fatalf("missing file should yield zero config, got %+v", cfg)
	}
}

func TestLoadFileConfig_EmptyPath(t *testing.T) {
	cfg, err := loadFileConfig("")
	if err != nil {
		t.Fatalf("empty path should not error, got %v", err)
	}
	if !reflect.DeepEqual(cfg, fileConfig{}) {
		t.Fatalf("empty path should yield zero config, got %+v", cfg)
	}
}

func TestLoadFileConfig_Valid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
model = "claude-opus-4-8"
base_url = "https://example.com"
api_key = "sk-test"
protocol = "anthropic"
provider = "deepseek"
thinking_level = "high"
output_format = "stream-json"
no_tools = true
no_skills = true
approve = true
system_prompt = "be terse"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFileConfig(path)
	if err != nil {
		t.Fatalf("valid file should parse, got %v", err)
	}
	want := fileConfig{
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
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("parsed config = %+v, want %+v", cfg, want)
	}
}

func TestLoadFileConfig_Malformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("model = = ="), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFileConfig(path); err == nil {
		t.Fatal("malformed file should error")
	}
}

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
	cfg := fileConfig{
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
	cfg := fileConfig{Model: "config-model", OutputFormat: "stream-json"}
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
	applyFileConfig(&opts, fileConfig{}, changedSet())
	if opts.model != "openrouter/free" || opts.outputFmt != "text" {
		t.Fatalf("empty config should not change opts, got %+v", opts)
	}
	if opts.baseURL != "" || opts.provider != "" || opts.noTools {
		t.Fatalf("empty config should leave unset fields empty, got %+v", opts)
	}
}
