// This file re-exports the symbols moved to internal/agenttool (US-003 of the
// agent package split) via type aliases, var bindings, and const
// re-declarations, so the remaining files in package agent (loop.go, config.go,
// etc.) compile unchanged during the transition. It will be removed as later
// steps migrate call sites to reference internal/agenttool directly.
package agent

import "github.com/smallnest/pigo/internal/agenttool"

// --- bash_tool.go ---

type BashTool = agenttool.BashTool

// --- edit_tool.go ---

type EditTool = agenttool.EditTool

// --- read_tool.go ---

type ReadTool = agenttool.ReadTool

// --- write_tool.go ---

type WriteTool = agenttool.WriteTool

// --- search_tool.go ---

type (
	GrepTool = agenttool.GrepTool
	FindTool = agenttool.FindTool
	LsTool   = agenttool.LsTool
)

// --- registry.go ---

type (
	ToolRegistry = agenttool.ToolRegistry
	FieldError   = agenttool.FieldError
)

var (
	NewToolRegistry       = agenttool.NewToolRegistry
	ValidationErrorResult = agenttool.ValidationErrorResult
)

// --- tool_executor.go ---

type ToolExecutorConfig = agenttool.ToolExecutorConfig

// --- batch_executor.go ---
//
// executeToolCalls was unexported in package agent; cross-package visibility
// forces exporting it as agenttool.ExecuteToolCalls. Bind it back to the old
// unexported name so callers in package agent (loop.go) compile unchanged.

type BatchConfig = agenttool.BatchConfig

var executeToolCalls = agenttool.ExecuteToolCalls

// --- sandbox.go ---

type (
	Decision    = agenttool.Decision
	AccessKind  = agenttool.AccessKind
	Request     = agenttool.Request
	Policy      = agenttool.Policy
	SandboxGate = agenttool.SandboxGate
)

const (
	DecisionDeny   = agenttool.DecisionDeny
	DecisionPrompt = agenttool.DecisionPrompt
	DecisionAllow  = agenttool.DecisionAllow

	AccessRead    = agenttool.AccessRead
	AccessWrite   = agenttool.AccessWrite
	AccessExec    = agenttool.AccessExec
	AccessNetwork = agenttool.AccessNetwork
)

var NewPolicy = agenttool.NewPolicy

// --- sandbox_secrets.go ---

type SecretRegistry = agenttool.SecretRegistry

var NewSecretRegistry = agenttool.NewSecretRegistry
