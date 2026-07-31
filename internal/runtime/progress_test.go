package runtime

// Tests for sub-agent progress reporting (US-005, #455): a dispatched task's
// child tool-execution / turn boundaries are translated into
// SubAgentProgressEvent and surfaced through the run-level emitter the parent
// loop injects into ctx (WithProgressEmitter). The parent tool-call id must ride
// on the event, the activity must map from the child event, and a nil emitter
// (the tool called outside a loop, e.g. a direct unit test) must be a silent
// no-op rather than a panic. The child loop is driven through the faux provider
// seam; only the provider boundary is faked.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

// TestActivityOf pins the child-event → activity mapping (D-8 / §5.3): tool
// starts map to their display verb, a turn start maps to "Thinking", and
// everything else maps to "" (no emission).
func TestActivityOf(t *testing.T) {
	cases := []struct {
		ev   agentcore.AgentEvent
		want string
	}{
		{agentcore.ToolExecutionStartEvent{ToolName: "read"}, "Reading"},
		{agentcore.ToolExecutionStartEvent{ToolName: "edit"}, "Editing"},
		{agentcore.ToolExecutionStartEvent{ToolName: "write"}, "Editing"},
		{agentcore.ToolExecutionStartEvent{ToolName: "bash"}, "Running bash"},
		{agentcore.ToolExecutionStartEvent{ToolName: "grep"}, "Searching"},
		{agentcore.ToolExecutionStartEvent{ToolName: "find"}, "Searching"},
		{agentcore.ToolExecutionStartEvent{ToolName: "ls"}, "Searching"},
		{agentcore.ToolExecutionStartEvent{ToolName: "webfetch"}, "Fetching"},
		{agentcore.ToolExecutionStartEvent{ToolName: "todo"}, ""},
		{agentcore.TurnStartEvent{}, "Thinking"},
		{agentcore.ToolExecutionEndEvent{ToolName: "read"}, ""},
		{agentcore.MessageStartEvent{}, ""},
	}
	for _, c := range cases {
		if got := activityOf(c.ev); got != c.want {
			t.Errorf("activityOf(%T{%v}) = %q, want %q", c.ev, c.ev, got, c.want)
		}
	}
}

// TestTaskEmitsSubAgentProgress verifies a child that executes a tool triggers a
// SubAgentProgressEvent carrying the parent task's tool-call id and the mapped
// activity ("Reading" for a child "read" tool), plus the "Thinking" boundary at
// each turn start.
func TestTaskEmitsSubAgentProgress(t *testing.T) {
	// A child tool named "read" so its ToolExecutionStart maps to "Reading".
	readTool := execTool{
		name: "read",
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("file body")}}, nil
		},
	}
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{toolCallTurn("t1", "read", `{}`), textTurn("child final report")},
	}
	factory := func() RunConfig {
		reg := agenttool.NewToolRegistry()
		_ = reg.Register(readTool)
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
		}
	}
	tool := NewTaskTool(factory, nil)

	var mu sync.Mutex
	var progress []agentcore.SubAgentProgressEvent
	emit := func(ctx context.Context, ev agentcore.AgentEvent) error {
		if p, ok := ev.(agentcore.SubAgentProgressEvent); ok {
			mu.Lock()
			progress = append(progress, p)
			mu.Unlock()
		}
		return nil
	}
	ctx := agentcore.WithProgressEmitter(context.Background(), emit)

	const parentID = "parent-call-id"
	res, err := tool.Execute(ctx, parentID, json.RawMessage(`{"description":"read the file","prompt":"do the work"}`), nil)
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "child final report" {
		t.Errorf("task result = %q, want 'child final report'", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(progress) == 0 {
		t.Fatal("expected at least one SubAgentProgressEvent, got none")
	}
	var sawReading bool
	for _, p := range progress {
		if p.ToolCallID != parentID {
			t.Errorf("progress ToolCallID = %q, want %q", p.ToolCallID, parentID)
		}
		if p.Description != "read the file" {
			t.Errorf("progress Description = %q, want 'read the file'", p.Description)
		}
		if p.Activity == "" {
			t.Errorf("progress emitted with empty Activity (should be skipped)")
		}
		if p.Activity == "Reading" {
			sawReading = true
		}
	}
	if !sawReading {
		t.Errorf("expected a 'Reading' activity from the child read tool, got %+v", progress)
	}
}

// TestTaskNilEmitterNoPanic verifies that when no progress emitter is present in
// ctx (the tool called outside a loop, as in direct unit tests) the child still
// runs, returns its text, and emits no progress — without panicking.
func TestTaskNilEmitterNoPanic(t *testing.T) {
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{textTurn("child final report")},
	}
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
		}
	}
	tool := NewTaskTool(factory, nil)

	// Plain ctx: no WithProgressEmitter, so ProgressEmitterFromContext is nil.
	res, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "child final report" {
		t.Errorf("task result = %q, want 'child final report'", got)
	}
}
