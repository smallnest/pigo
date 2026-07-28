package main

// Test for config.toml `prompts` array -> settings-tier prompt templates
// (US-007, #338): applyFileConfig passes the parsed array into cliOptions.
// The loadPromptPaths/buildSlashRegistry tests moved to package repl
// (internal/cli/repl/prompts_config_test.go) alongside those functions.

import (
	"testing"

	"github.com/smallnest/pigo/internal/cli/config"
)

func TestApplyFileConfigPrompts(t *testing.T) {
	var opts cliOptions
	cfg := config.FileConfig{Prompts: []string{"./my-prompts", "/abs/x.md"}}
	applyFileConfig(&opts, cfg, func(string) bool { return false })
	if len(opts.configPrompts) != 2 || opts.configPrompts[0] != "./my-prompts" || opts.configPrompts[1] != "/abs/x.md" {
		t.Errorf("opts.configPrompts = %v, want [./my-prompts /abs/x.md]", opts.configPrompts)
	}
}
