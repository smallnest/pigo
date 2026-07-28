package main

import (
	"testing"

	"github.com/smallnest/pigo/internal/cli/config"
)

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
