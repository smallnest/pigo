package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// execTool is a configurable AgentTool for executor tests.
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

func newExecCfg(t *testing.T, tool AgentTool) ToolExecutorConfig {
	t.Helper()
	r := NewToolRegistry()
	if err := r.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	return ToolExecutorConfig{Registry: r}
}

func textOf(msg ToolResultMessage) string {
	if len(msg.Content) == 0 {
		return ""
	}
	if tc, ok := msg.Content[0].(TextContent); ok {
		return tc.Text
	}
	return ""
}

func TestExecutorNormal(t *testing.T) {
	tool := execTool{name: "echo", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
		return AgentToolResult{Content: ContentList{NewTextContent("done")}}, nil
	}}
	cfg := newExecCfg(t, tool)

	var events []AgentEvent
	emit := func(ctx context.Context, ev AgentEvent) error { events = append(events, ev); return nil }
	msg, term := executeToolCall(context.Background(), cfg, AgentToolCall{ID: "1", Name: "echo"}, emit)

	if msg.IsError || textOf(msg) != "done" {
		t.Fatalf("normal result wrong: %+v", msg)
	}
	if term {
		t.Error("normal result should not terminate")
	}
	wantKinds := []string{EventToolExecutionStart, EventToolExecutionEnd}
	if len(events) != 2 || events[0].EventType() != wantKinds[0] || events[1].EventType() != wantKinds[1] {
		t.Errorf("events wrong: %+v", events)
	}
}

func TestExecutorUnknownTool(t *testing.T) {
	cfg := ToolExecutorConfig{Registry: NewToolRegistry()}
	msg, _ := executeToolCall(context.Background(), cfg, AgentToolCall{ID: "1", Name: "ghost"}, nil)
	if !msg.IsError {
		t.Fatalf("unknown tool should be error result: %+v", msg)
	}
}

func TestExecutorValidationFailure(t *testing.T) {
	schema := `{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`
	tool := execTool{name: "need", schema: schema, run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
		t.Fatal("execute must not run on validation failure")
		return AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(context.Background(), cfg, AgentToolCall{ID: "1", Name: "need", Arguments: json.RawMessage(`{}`)}, nil)
	if !msg.IsError {
		t.Fatalf("validation failure should be error result: %+v", msg)
	}
}

func TestExecutorBlock(t *testing.T) {
	tool := execTool{name: "echo", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
		t.Fatal("execute must not run when blocked")
		return AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(ctx context.Context, call AgentToolCall) *BeforeToolCallDecision {
		return &BeforeToolCallDecision{Block: true}
	}
	msg, _ := executeToolCall(context.Background(), cfg, AgentToolCall{ID: "1", Name: "echo"}, nil)
	if !msg.IsError {
		t.Fatalf("blocked call should be error result: %+v", msg)
	}
}

func TestExecutorToolError(t *testing.T) {
	tool := execTool{name: "boom", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
		return AgentToolResult{}, errors.New("kaboom")
	}}
	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(context.Background(), cfg, AgentToolCall{ID: "1", Name: "boom"}, nil)
	if !msg.IsError {
		t.Fatalf("tool error should be error result: %+v", msg)
	}
}

func TestExecutorPanicRecovered(t *testing.T) {
	tool := execTool{name: "panic", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
		panic("oops")
	}}
	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(context.Background(), cfg, AgentToolCall{ID: "1", Name: "panic"}, nil)
	if !msg.IsError {
		t.Fatalf("panic should be recovered into error result: %+v", msg)
	}
}

func TestExecutorAfterToolCallOverride(t *testing.T) {
	tool := execTool{name: "echo", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
		return AgentToolResult{Content: ContentList{NewTextContent("orig")}}, nil
	}}
	cfg := newExecCfg(t, tool)
	newContent := ContentList{NewTextContent("overridden")}
	isErr := true
	term := true
	cfg.AfterToolCall = func(ctx context.Context, call AgentToolCall, result AgentToolResult, isError bool) *AfterToolCallResult {
		return &AfterToolCallResult{Content: &newContent, IsError: &isErr, Terminate: &term}
	}
	msg, terminate := executeToolCall(context.Background(), cfg, AgentToolCall{ID: "1", Name: "echo"}, nil)
	if textOf(msg) != "overridden" {
		t.Errorf("content override failed: %q", textOf(msg))
	}
	if !msg.IsError {
		t.Error("isError override failed")
	}
	if !terminate {
		t.Error("terminate override failed")
	}
}

func TestExecutorUpdateCallback(t *testing.T) {
	tool := execTool{name: "stream", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
		onUpdate(AgentToolResult{Content: ContentList{NewTextContent("partial")}})
		return AgentToolResult{Content: ContentList{NewTextContent("final")}}, nil
	}}
	cfg := newExecCfg(t, tool)
	var updates int
	emit := func(ctx context.Context, ev AgentEvent) error {
		if ev.EventType() == EventToolExecutionUpdate {
			updates++
		}
		return nil
	}
	executeToolCall(context.Background(), cfg, AgentToolCall{ID: "1", Name: "stream"}, emit)
	if updates != 1 {
		t.Errorf("expected 1 update event, got %d", updates)
	}
}

func TestExecutorAbortedContext(t *testing.T) {
	tool := execTool{name: "echo", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
		t.Fatal("execute must not run when context already cancelled")
		return AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg, _ := executeToolCall(ctx, cfg, AgentToolCall{ID: "1", Name: "echo"}, nil)
	if !msg.IsError {
		t.Fatalf("aborted call should be error result: %+v", msg)
	}
}
