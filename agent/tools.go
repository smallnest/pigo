package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// ToolExecutionMode selects how a custom tool is scheduled relative to other
// tool calls in the same batch.
type ToolExecutionMode string

const (
	// ToolExecutionParallel allows the tool to run concurrently with peers.
	ToolExecutionParallel ToolExecutionMode = "parallel"
	// ToolExecutionSequential forces the containing tool batch to run serially.
	ToolExecutionSequential ToolExecutionMode = "sequential"
)

// ToolCall describes one model-requested invocation of a custom tool.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResultContentType identifies one supported custom-tool result block.
type ToolResultContentType string

const (
	ToolResultContentText  ToolResultContentType = "text"
	ToolResultContentImage ToolResultContentType = "image"
)

// ToolResultContent is one text or image block returned by a custom tool.
type ToolResultContent struct {
	Type     ToolResultContentType `json:"type"`
	Text     string                `json:"text,omitempty"`
	Data     string                `json:"data,omitempty"`
	MIMEType string                `json:"mimeType,omitempty"`
}

// ToolResult is the result of a custom tool invocation. Details is application
// metadata and Terminate requests early agent-loop termination when true.
type ToolResult struct {
	Content   []ToolResultContent `json:"content"`
	Details   any                 `json:"details,omitempty"`
	Terminate *bool               `json:"terminate,omitempty"`
}

// ToolExecuteFunc executes a custom tool. onUpdate may be nil; when non-nil the
// tool may synchronously publish partial results while it runs.
type ToolExecuteFunc func(ctx context.Context, call ToolCall, onUpdate func(ToolResult)) (ToolResult, error)

// Tool defines a custom tool explicitly registered by an SDK consumer. Schema
// is the JSON Schema advertised for Arguments.
type Tool struct {
	Name          string
	Description   string
	Schema        json.RawMessage
	ExecutionMode ToolExecutionMode
	Execute       ToolExecuteFunc
}

type customToolAdapter struct {
	tool Tool
}

func (t customToolAdapter) Name() string        { return t.tool.Name }
func (t customToolAdapter) Description() string { return t.tool.Description }
func (t customToolAdapter) Schema() json.RawMessage {
	return append(json.RawMessage(nil), t.tool.Schema...)
}
func (t customToolAdapter) ExecutionMode() agentcore.ToolExecutionMode {
	if t.tool.ExecutionMode == ToolExecutionSequential {
		return agentcore.ToolExecutionSequential
	}
	return agentcore.ToolExecutionParallel
}

func (t customToolAdapter) Execute(
	ctx context.Context,
	id string,
	args json.RawMessage,
	onUpdate agentcore.ToolUpdateFunc,
) (agentcore.AgentToolResult, error) {
	var updateErr error
	var publicUpdate func(ToolResult)
	if onUpdate != nil {
		publicUpdate = func(partial ToolResult) {
			if updateErr != nil {
				return
			}
			converted, err := toolResultToInternal(partial)
			if err != nil {
				updateErr = err
				return
			}
			onUpdate(converted)
		}
	}

	result, err := t.tool.Execute(ctx, ToolCall{
		ID:        id,
		Name:      t.tool.Name,
		Arguments: append(json.RawMessage(nil), args...),
	}, publicUpdate)
	if err != nil {
		return agentcore.AgentToolResult{}, err
	}
	if updateErr != nil {
		return agentcore.AgentToolResult{}, updateErr
	}
	return toolResultToInternal(result)
}

func adaptCustomTools(custom []Tool, builtins []agentcore.AgentTool) ([]agentcore.AgentTool, error) {
	if len(custom) == 0 {
		return builtins, nil
	}

	used := make(map[string]struct{}, len(builtins)+len(custom))
	for _, tool := range builtins {
		used[strings.ToLower(tool.Name())] = struct{}{}
	}

	out := make([]agentcore.AgentTool, 0, len(builtins)+len(custom))
	out = append(out, builtins...)
	for _, raw := range custom {
		tool, err := validateAndCopyTool(raw)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(tool.Name)
		if _, exists := used[key]; exists {
			return nil, fmt.Errorf("custom tool %q duplicates or shadows another tool", tool.Name)
		}
		used[key] = struct{}{}
		out = append(out, customToolAdapter{tool: tool})
	}
	return out, nil
}

func validateAndCopyTool(tool Tool) (Tool, error) {
	trimmed := strings.TrimSpace(tool.Name)
	if trimmed == "" || trimmed != tool.Name {
		return Tool{}, fmt.Errorf("custom tool name must be non-empty and have no surrounding whitespace")
	}
	if tool.Execute == nil {
		return Tool{}, fmt.Errorf("custom tool %q has no Execute function", tool.Name)
	}
	if len(tool.Schema) == 0 || !json.Valid(tool.Schema) {
		return Tool{}, fmt.Errorf("custom tool %q has invalid JSON Schema", tool.Name)
	}
	var schemaObject map[string]any
	if err := json.Unmarshal(tool.Schema, &schemaObject); err != nil || schemaObject == nil {
		return Tool{}, fmt.Errorf("custom tool %q schema must be a JSON object", tool.Name)
	}
	switch tool.ExecutionMode {
	case "", ToolExecutionParallel:
		tool.ExecutionMode = ToolExecutionParallel
	case ToolExecutionSequential:
	default:
		return Tool{}, fmt.Errorf("custom tool %q has invalid execution mode %q", tool.Name, tool.ExecutionMode)
	}
	tool.Schema = append(json.RawMessage(nil), tool.Schema...)
	return tool, nil
}

func toolResultToInternal(result ToolResult) (agentcore.AgentToolResult, error) {
	content := make(agentcore.ContentList, 0, len(result.Content))
	for _, block := range result.Content {
		switch block.Type {
		case ToolResultContentText:
			content = append(content, agentcore.NewTextContent(block.Text))
		case ToolResultContentImage:
			content = append(content, agentcore.NewImageContent(block.Data, block.MIMEType))
		default:
			return agentcore.AgentToolResult{}, fmt.Errorf("unsupported custom tool result content type %q", block.Type)
		}
	}
	return agentcore.AgentToolResult{
		Content:   content,
		Details:   result.Details,
		Terminate: cloneBool(result.Terminate),
	}, nil
}

func toolResultFromInternal(result agentcore.AgentToolResult) ToolResult {
	content := make([]ToolResultContent, 0, len(result.Content))
	for _, block := range result.Content {
		switch value := block.(type) {
		case agentcore.TextContent:
			content = append(content, ToolResultContent{Type: ToolResultContentText, Text: value.Text})
		case agentcore.ImageContent:
			content = append(content, ToolResultContent{Type: ToolResultContentImage, Data: value.Data, MIMEType: value.MimeType})
		}
	}
	return ToolResult{
		Content:   content,
		Details:   result.Details,
		Terminate: cloneBool(result.Terminate),
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
