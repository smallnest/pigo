package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func TestCustomToolAdapterPreservesCallUpdatesAndResult(t *testing.T) {
	terminate := true
	var gotCall ToolCall
	var updates []ToolResult

	tool := Tool{
		Name:          "piflow_lookup",
		Description:   "look something up",
		Schema:        json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		ExecutionMode: ToolExecutionSequential,
		Execute: func(_ context.Context, call ToolCall, onUpdate func(ToolResult)) (ToolResult, error) {
			gotCall = call
			onUpdate(ToolResult{
				Content: []ToolResultContent{{Type: ToolResultContentText, Text: "partial"}},
				Details: map[string]any{"phase": "running"},
			})
			return ToolResult{
				Content: []ToolResultContent{
					{Type: ToolResultContentText, Text: "done"},
					{Type: ToolResultContentImage, Data: "aW1hZ2U=", MIMEType: "image/png"},
				},
				Details:   map[string]any{"count": float64(1)},
				Terminate: &terminate,
			}, nil
		},
	}

	adapted, err := adaptCustomTools([]Tool{tool}, nil)
	if err != nil {
		t.Fatalf("adaptCustomTools: %v", err)
	}
	if len(adapted) != 1 {
		t.Fatalf("len(adapted) = %d, want 1", len(adapted))
	}
	if got := adapted[0].ExecutionMode(); got != agentcore.ToolExecutionSequential {
		t.Fatalf("ExecutionMode() = %q, want sequential", got)
	}

	var internalUpdates []agentcore.AgentToolResult
	result, err := adapted[0].Execute(
		context.Background(),
		"call-1",
		json.RawMessage(`{"q":"PiFlow"}`),
		func(update agentcore.AgentToolResult) { internalUpdates = append(internalUpdates, update) },
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if gotCall.ID != "call-1" || gotCall.Name != "piflow_lookup" || string(gotCall.Arguments) != `{"q":"PiFlow"}` {
		t.Fatalf("public call = %#v", gotCall)
	}
	if len(internalUpdates) != 1 {
		t.Fatalf("internal updates = %d, want 1", len(internalUpdates))
	}
	updates = append(updates, toolResultFromInternal(internalUpdates[0]))
	if got := updates[0].Content; !reflect.DeepEqual(got, []ToolResultContent{{Type: ToolResultContentText, Text: "partial"}}) {
		t.Fatalf("update content = %#v", got)
	}
	if !reflect.DeepEqual(updates[0].Details, map[string]any{"phase": "running"}) {
		t.Fatalf("update details = %#v", updates[0].Details)
	}

	publicResult := toolResultFromInternal(result)
	wantContent := []ToolResultContent{
		{Type: ToolResultContentText, Text: "done"},
		{Type: ToolResultContentImage, Data: "aW1hZ2U=", MIMEType: "image/png"},
	}
	if !reflect.DeepEqual(publicResult.Content, wantContent) {
		t.Fatalf("result content = %#v, want %#v", publicResult.Content, wantContent)
	}
	if !reflect.DeepEqual(publicResult.Details, map[string]any{"count": float64(1)}) {
		t.Fatalf("result details = %#v", publicResult.Details)
	}
	if publicResult.Terminate == nil || !*publicResult.Terminate {
		t.Fatalf("result terminate = %#v, want true", publicResult.Terminate)
	}
}

func TestCustomToolAdapterCopiesSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	adapted, err := adaptCustomTools([]Tool{{
		Name:   "piflow_copy",
		Schema: schema,
		Execute: func(context.Context, ToolCall, func(ToolResult)) (ToolResult, error) {
			return ToolResult{}, nil
		},
	}}, nil)
	if err != nil {
		t.Fatalf("adaptCustomTools: %v", err)
	}

	schema[0] = '['
	if got := string(adapted[0].Schema()); got != `{"type":"object"}` {
		t.Fatalf("Schema() = %q, want original copied schema", got)
	}
}

func TestCustomToolAdapterRejectsInvalidResultContent(t *testing.T) {
	adapted, err := adaptCustomTools([]Tool{{
		Name:   "piflow_bad_result",
		Schema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, ToolCall, func(ToolResult)) (ToolResult, error) {
			return ToolResult{Content: []ToolResultContent{{Type: ToolResultContentType("video")}}}, nil
		},
	}}, nil)
	if err != nil {
		t.Fatalf("adaptCustomTools: %v", err)
	}

	_, err = adapted[0].Execute(context.Background(), "call-1", json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("Execute succeeded, want invalid result-content error")
	}
}
