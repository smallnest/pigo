// This file re-exports the symbols moved to internal/runtime (US-004 of the
// agent package split) via type aliases, function var bindings, and typed-const
// re-declarations, so external callers (cmd/, internal/tui, internal/session)
// that still reference agent.* keep compiling during the transition. It will be
// removed when US-005 migrates those call sites to reference internal/runtime
// directly.
package agent

import "github.com/smallnest/pigo/internal/runtime"

// --- loop.go ---

type (
	RunConfig       = runtime.RunConfig
	LoopEventStream = runtime.LoopEventStream
)

var (
	StartRun    = runtime.StartRun
	ContinueRun = runtime.ContinueRun
)

// --- stream_response.go ---

type LoopConfig = runtime.LoopConfig

// --- prompt.go ---

type PromptConfig = runtime.PromptConfig

var BuildSystemPrompt = runtime.BuildSystemPrompt

// --- headless.go ---

type (
	HeadlessConfig = runtime.HeadlessConfig
	HeadlessMode   = runtime.HeadlessMode
)

const (
	PrintMode      = runtime.PrintMode
	StreamJSONMode = runtime.StreamJSONMode
)

var RunHeadless = runtime.RunHeadless

// --- slashcommand.go ---

type SlashRegistry = runtime.SlashRegistry

var (
	NewSlashRegistry    = runtime.NewSlashRegistry
	LoadUserCommandsDir = runtime.LoadUserCommandsDir
)

// --- subagent.go ---

var NewSubAgentTool = runtime.NewSubAgentTool

// --- skills.go ---

var ParseSkill = runtime.ParseSkill
