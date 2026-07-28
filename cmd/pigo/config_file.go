// This file binds a config.FileConfig onto the parsed CLI options. The file's
// values replace pigo's built-in defaults, but an explicit command-line flag
// always wins over the file:
//
//	command-line flag > config.toml > built-in default
//
// The file loading/decoding itself lives in internal/cli/config; this overlay
// stays here because it mutates cliOptions, the run-assembly options struct.
package main

import (
	"github.com/smallnest/pigo/internal/cli/config"
)

// applyFileConfig overlays config.toml values onto opts, but only for flags the
// user did not set on the command line (changed reports whether a flag name was
// explicitly passed). This yields the precedence: CLI flag > config file >
// default. Zero-valued config fields never override.
func applyFileConfig(opts *cliOptions, cfg config.FileConfig, changed func(string) bool) {
	if cfg.Model != "" && !changed("model") {
		opts.model = cfg.Model
	}
	if cfg.BaseURL != "" && !changed("base-url") {
		opts.baseURL = cfg.BaseURL
	}
	if cfg.APIKey != "" && !changed("api-key") {
		opts.apiKey = cfg.APIKey
	}
	if cfg.Protocol != "" && !changed("protocol") {
		opts.protocol = cfg.Protocol
	}
	if cfg.Provider != "" && !changed("provider") {
		opts.provider = cfg.Provider
	}
	if cfg.ThinkingLevel != "" && !changed("thinking-level") {
		opts.thinkingLevel = cfg.ThinkingLevel
	}
	if cfg.OutputFormat != "" && !changed("output-format") {
		opts.outputFmt = cfg.OutputFormat
	}
	if cfg.NoTools && !changed("no-tools") {
		opts.noTools = true
	}
	if cfg.NoSkills && !changed("no-skills") {
		opts.noSkills = true
	}
	if cfg.Approve && !changed("approve") {
		opts.approve = true
	}
	if cfg.SystemPrompt != "" && !changed("system-prompt") {
		opts.systemPrompt = cfg.SystemPrompt
	}
	// prompts (settings tier) are additive with --prompt-template (CLI tier,
	// wired in #339), so they are always passed through when present.
	if len(cfg.Prompts) > 0 {
		opts.configPrompts = cfg.Prompts
	}
}
