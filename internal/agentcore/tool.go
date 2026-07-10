package agentcore

import (
	"context"
	"encoding/json"
)

// AgentContext is the input state for a loop run: system prompt, conversation
// messages, and the tools available to the model.
type AgentContext struct {
	SystemPrompt string      `json:"systemPrompt"`
	Messages     MessageList `json:"messages"`
	Tools        []AgentTool `json:"-"`
}

// ToolExecutionMode selects how a tool is executed relative to others in a batch.
type ToolExecutionMode string

const (
	// ToolExecutionParallel allows the tool to run concurrently with others.
	ToolExecutionParallel ToolExecutionMode = "parallel"
	// ToolExecutionSequential forces the whole batch to run serially.
	ToolExecutionSequential ToolExecutionMode = "sequential"
)

// ToolUpdateFunc receives a partial result during tool execution; the loop
// turns each call into a tool_execution_update event.
type ToolUpdateFunc func(partial AgentToolResult)

// AgentTool is a tool the model can invoke. Schema is the JSON Schema used to
// validate arguments before execution (US-014).
type AgentTool interface {
	Name() string
	Description() string
	// Schema returns the JSON Schema (as raw JSON) for the tool's arguments.
	Schema() json.RawMessage
	// ExecutionMode reports whether this tool forces sequential execution.
	ExecutionMode() ToolExecutionMode
	// Execute runs the tool. onUpdate may be nil.
	Execute(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error)
}

// AgentToolCall is a decoded request to invoke a tool (the loop-level view of a
// ToolCallContent block).
type AgentToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// AgentToolResult is the outcome of executing a tool.
//
// Details uses `any` in the first version (matching pi's internal
// AgentToolResult<any>); a generic form can be added later. Terminate is a
// *bool so "not set" is distinguishable from an explicit false — the loop only
// signals early termination when every result in a batch has Terminate=true.
type AgentToolResult struct {
	Content   ContentList `json:"content"`
	Details   any         `json:"details,omitempty"`
	Terminate *bool       `json:"terminate,omitempty"`
}
