package tui

import (
	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// Options carries the resolved run configuration into Run. It deliberately
// mirrors repl.Options (see internal/cli/repl/interactive.go) field-for-field so
// cmd/pigo's dispatch can map the same assembled environment to either path, and
// so downstream nodes can port the REPL's session/live/slash/trust wiring into
// the TUI without reshaping the entry seam. This skeleton node does not yet
// consume most fields — they are here to lock the contract.
type Options struct {
	Model        string
	ProviderName string
	Provider     provider.Provider
	BaseURL      string
	APIKey       string
	Protocol     string
	// ThinkingLevel is the resolved reasoning-effort level (US-023): it seeds the
	// live run config so every turn requests it, until a control command changes
	// it.
	ThinkingLevel agentcore.ThinkingLevel
	Tools         []agentcore.AgentTool
	SysPrompt     string

	// ResumeID, when non-empty, resumes an existing session: its messages seed
	// the context and replayed transcript. Otherwise a fresh session is created.
	ResumeID string

	// Approve, when true, grants the launch directory session trust before the
	// run so the first-launch trust prompt is skipped and side-effect tools run
	// without per-call confirmation (对标 pi 的 --approve/-a).
	Approve bool
	// Skills is the pre-loaded skill set (loaded once by run.SetupEnv, shared with
	// prompt injection). Each is registered as a /skill-name command. Empty under
	// --no-skills, so nothing is registered.
	Skills []*runtime.Skill

	// Plugins holds the loaded plugin manager so the TUI can deliver lifecycle
	// events to subscribed plugins (US-017, #133). It may be nil (no plugins).
	Plugins *plugin.Manager

	// ConfigPrompts holds prompt-template paths from the config.toml `prompts`
	// array (settings tier); each is a file or dir loaded non-recursively.
	ConfigPrompts []string
	// CliPrompts holds --prompt-template paths (CLI tier, repeatable).
	CliPrompts []string
	// NoPromptTemplates disables all prompt-template discovery (global, project,
	// settings, CLI); built-in slash commands are unaffected. Independent of
	// --no-skills.
	NoPromptTemplates bool
}
