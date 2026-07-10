package agent

import (
	"context"
	"encoding/json"
)

// execTool is a configurable AgentTool used by the loop/headless tests that
// remain in package agent. Its canonical definition moved to
// internal/agenttool with tool_executor_test.go (US-003 of the package split);
// this copy is re-provided here so the agent-resident tests keep compiling
// during the transition.
type execTool struct {
	name   string
	schema string
	run    func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error)
	mode   ToolExecutionMode
}

func (t execTool) Name() string        { return t.name }
func (t execTool) Description() string { return "exec" }
func (t execTool) Schema() json.RawMessage {
	if t.schema == "" {
		return nil
	}
	return json.RawMessage(t.schema)
}
func (t execTool) ExecutionMode() ToolExecutionMode {
	if t.mode == "" {
		return ToolExecutionParallel
	}
	return t.mode
}
func (t execTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
	return t.run(ctx, id, args, onUpdate)
}

// echoTool returns its name as text; optionally terminates. Canonical
// definition moved with batch_executor_test.go; re-provided here for the
// agent-resident tests.
func echoTool(name string, mode ToolExecutionMode, terminate bool) execTool {
	return execTool{
		name: name,
		mode: mode,
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
			term := terminate
			return AgentToolResult{Content: ContentList{NewTextContent(name)}, Terminate: &term}, nil
		},
	}
}
