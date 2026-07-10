package agenttool

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

func runBash(t *testing.T, tool *BashTool, args map[string]any, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), "call-1", raw, onUpdate)
}

func TestBashToolSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	res, gerr := runBash(t, tool, map[string]any{"command": "echo hello"}, nil)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	if !strings.Contains(resultText(res), "hello") {
		t.Errorf("output = %q, want to contain hello", resultText(res))
	}
	details, ok := res.Details.(map[string]any)
	if !ok || details["exitCode"] != 0 {
		t.Errorf("expected exitCode 0, details = %+v", res.Details)
	}
}

func TestBashToolNonZeroExitIsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	res, gerr := runBash(t, tool, map[string]any{"command": "echo oops >&2; exit 3"}, nil)
	// A non-zero exit must surface as a Go error so the executor flags isError.
	if gerr == nil {
		t.Fatalf("expected go error for non-zero exit, got nil")
	}
	if !strings.Contains(gerr.Error(), "code 3") {
		t.Errorf("error = %q, want to mention code 3", gerr.Error())
	}
	// The captured output must ride along.
	if !strings.Contains(gerr.Error(), "oops") {
		t.Errorf("error = %q, want to carry output", gerr.Error())
	}
	details, ok := res.Details.(map[string]any)
	if !ok || details["exitCode"] != 3 {
		t.Errorf("expected exitCode 3, details = %+v", res.Details)
	}
}

func TestBashToolStreaming(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	var mu sync.Mutex
	var updates []string
	onUpdate := func(r agentcore.AgentToolResult) {
		mu.Lock()
		updates = append(updates, resultText(r))
		mu.Unlock()
	}
	_, gerr := runBash(t, tool, map[string]any{"command": "printf 'a'; printf 'b'"}, onUpdate)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(updates) == 0 {
		t.Fatalf("expected streaming updates, got none")
	}
	// The final partial should be the full accumulated output.
	if last := updates[len(updates)-1]; !strings.Contains(last, "ab") {
		t.Errorf("final update = %q, want to contain ab", last)
	}
}

func TestBashToolTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	start := time.Now()
	res, gerr := runBash(t, tool, map[string]any{"command": "sleep 5", "timeout_ms": 100}, nil)
	if gerr == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !strings.Contains(gerr.Error(), "timed out") {
		t.Errorf("error = %q, want to mention timed out", gerr.Error())
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout took too long: %s (process not killed?)", elapsed)
	}
	_ = res
}

func TestBashToolCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	ctx, cancel := context.WithCancel(context.Background())
	raw, _ := json.Marshal(map[string]any{"command": "sleep 5"})
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, gerr := tool.Execute(ctx, "call-1", raw, nil)
	if gerr == nil {
		t.Fatalf("expected cancellation error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancel took too long: %s (process not killed?)", elapsed)
	}
}

func TestBashToolMissingCommand(t *testing.T) {
	tool := &BashTool{}
	res, gerr := runBash(t, tool, map[string]any{"command": ""}, nil)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	if !strings.Contains(resultText(res), "command is required") {
		t.Errorf("expected command-required error, got %q", resultText(res))
	}
}

func TestBashToolMode(t *testing.T) {
	tool := &BashTool{}
	if tool.Name() != "bash" {
		t.Errorf("name = %q", tool.Name())
	}
	if tool.ExecutionMode() != agentcore.ToolExecutionSequential {
		t.Error("bash should be sequential")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Errorf("schema not valid JSON: %v", err)
	}
}
