// This file implements the optional user config file at ~/.config/pigo/config.toml
// (honoring $XDG_CONFIG_HOME when set). Values in the file replace pigo's built-in
// defaults, but an explicit command-line flag always wins over the file:
//
//	command-line flag > config.toml > built-in default
//
// A missing file is not an error (defaults apply); a malformed file is surfaced
// to the caller so it can warn rather than silently ignore user intent.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// fileConfig is the on-disk shape of config.toml. Every field is optional; an
// absent (zero-value) field leaves the corresponding default/flag untouched.
// Keys are snake_case to read naturally in TOML.
type fileConfig struct {
	Model         string `toml:"model"`
	BaseURL       string `toml:"base_url"`
	APIKey        string `toml:"api_key"`
	Protocol      string `toml:"protocol"`
	Provider      string `toml:"provider"`
	ThinkingLevel string `toml:"thinking_level"`
	OutputFormat  string `toml:"output_format"`
	NoTools       bool   `toml:"no_tools"`
	NoSkills      bool   `toml:"no_skills"`
	Approve       bool   `toml:"approve"`
	SystemPrompt  string `toml:"system_prompt"`
	// Prompts is the config.toml `prompts` array: paths (files or dirs) to load
	// prompt templates from at the settings tier (对标 pi's settings prompts).
	Prompts []string `toml:"prompts"`
}

// fileConfigPath returns the path to the user config file:
// $XDG_CONFIG_HOME/pigo/config.toml, or ~/.config/pigo/config.toml by default.
// It returns "" when neither can be resolved, so the caller treats the file as
// absent.
func fileConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "pigo", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pigo", "config.toml")
}

// loadFileConfig reads and decodes config.toml. A missing file (or an empty
// path) returns a zero config with no error; a malformed file is an error.
func loadFileConfig(path string) (fileConfig, error) {
	if path == "" {
		return fileConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileConfig{}, nil
		}
		return fileConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg fileConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return fileConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// applyFileConfig overlays config.toml values onto opts, but only for flags the
// user did not set on the command line (changed reports whether a flag name was
// explicitly passed). This yields the precedence: CLI flag > config file >
// default. Zero-valued config fields never override.
func applyFileConfig(opts *cliOptions, cfg fileConfig, changed func(string) bool) {
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
