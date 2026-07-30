package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// recordingTool is a fake AgentTool that records whether Execute ran and echoes
// a fixed result, so a PreToolUse block can be asserted as "never executed".
type recordingTool struct {
	name string
	ran  *bool
}

func (t recordingTool) Name() string            { return t.name }
func (t recordingTool) Description() string     { return "fake" }
func (t recordingTool) Schema() json.RawMessage { return nil }
func (t recordingTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (t recordingTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	*t.ran = true
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("executed")}}, nil
}

func wiredConfig(t *testing.T, tool agentcore.AgentTool, set hooks.HookSet) runtime.RunConfig {
	t.Helper()
	reg := agenttool.NewToolRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	var cfg runtime.RunConfig
	cfg.Batch.ToolExecutorConfig.Registry = reg
	if d := InstallHooks(&cfg, set, HookDeps{SessionID: "s1", ProjectDir: t.TempDir()}); d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	return cfg
}

// TestPreToolUseBlocksBashRmRf: a PreToolUse hook matching bash inspects the
// piped tool_input for "rm -rf" and exits 2 with a reason; the tool must not run
// and the reason must surface in the result the model receives.
func TestPreToolUseBlocksBashRmRf(t *testing.T) {
	ran := false
	tool := recordingTool{name: "bash", ran: &ran}
	// Hook: block (exit 2) when stdin JSON contains "rm -rf", printing a reason.
	cmd := `if grep -q "rm -rf" ; then echo "dangerous command blocked" 1>&2; exit 2; fi`
	set := hooks.HookSet{
		"PreToolUse": {{Matcher: "bash", Hooks: []hooks.HookConfig{{Command: cmd}}}},
	}
	cfg := wiredConfig(t, tool, set)

	call := agentcore.AgentToolCall{ID: "1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /tmp/x"}`)}
	msgs, _ := agenttool.ExecuteToolCalls(context.Background(), cfg.Batch, []agentcore.AgentToolCall{call}, nil)

	if ran {
		t.Fatal("tool must not execute when PreToolUse blocks")
	}
	if len(msgs) != 1 || !msgs[0].IsError {
		t.Fatalf("blocked call should be an error result: %+v", msgs)
	}
	if txt := textOfMsg(msgs[0]); !strings.Contains(txt, "dangerous command blocked") {
		t.Fatalf("block reason not surfaced to model, got %q", txt)
	}
}

// TestPreToolUseAllowsSafeBash: the same hook allows a command without "rm -rf".
func TestPreToolUseAllowsSafeBash(t *testing.T) {
	ran := false
	tool := recordingTool{name: "bash", ran: &ran}
	cmd := `if grep -q "rm -rf" ; then echo "blocked" 1>&2; exit 2; fi`
	set := hooks.HookSet{
		"PreToolUse": {{Matcher: "bash", Hooks: []hooks.HookConfig{{Command: cmd}}}},
	}
	cfg := wiredConfig(t, tool, set)

	call := agentcore.AgentToolCall{ID: "1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)}
	msgs, _ := agenttool.ExecuteToolCalls(context.Background(), cfg.Batch, []agentcore.AgentToolCall{call}, nil)

	if !ran {
		t.Fatal("safe command should execute")
	}
	if msgs[0].IsError {
		t.Fatalf("safe command should not error: %+v", msgs[0])
	}
}

// TestPostToolUseAppendsFeedback: a PostToolUse hook prints additionalContext,
// which must be appended to the executed tool's result (not undo it).
func TestPostToolUseAppendsFeedback(t *testing.T) {
	ran := false
	tool := recordingTool{name: "write", ran: &ran}
	cmd := `echo '{"additionalContext":"linted: 0 issues"}'`
	set := hooks.HookSet{
		"PostToolUse": {{Matcher: "write", Hooks: []hooks.HookConfig{{Command: cmd}}}},
	}
	cfg := wiredConfig(t, tool, set)

	call := agentcore.AgentToolCall{ID: "1", Name: "write", Arguments: json.RawMessage(`{"path":"a.go"}`)}
	msgs, _ := agenttool.ExecuteToolCalls(context.Background(), cfg.Batch, []agentcore.AgentToolCall{call}, nil)

	if !ran {
		t.Fatal("tool should execute; Post hook must not undo it")
	}
	txt := allTextOfMsg(msgs[0])
	if !strings.Contains(txt, "executed") {
		t.Fatalf("original result lost: %q", txt)
	}
	if !strings.Contains(txt, "linted: 0 issues") {
		t.Fatalf("Post hook feedback not appended: %q", txt)
	}
}

// TestPreToolUseUpdatedInputRewritesArgs: a PreToolUse hook returns updatedInput,
// which must replace the tool's arguments before execution.
func TestPreToolUseUpdatedInputRewritesArgs(t *testing.T) {
	var gotArgs json.RawMessage
	captured := false
	tool := capturingTool{name: "bash", got: &gotArgs, captured: &captured}
	reg := agenttool.NewToolRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	cmd := `echo '{"updatedInput":{"command":"echo safe"}}'`
	set := hooks.HookSet{
		"PreToolUse": {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: cmd}}}},
	}
	var cfg runtime.RunConfig
	cfg.Batch.ToolExecutorConfig.Registry = reg
	if d := InstallHooks(&cfg, set, HookDeps{ProjectDir: t.TempDir()}); d == nil {
		t.Fatal("expected non-nil dispatcher")
	}

	call := agentcore.AgentToolCall{ID: "1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /"}`)}
	agenttool.ExecuteToolCalls(context.Background(), cfg.Batch, []agentcore.AgentToolCall{call}, nil)

	if !captured {
		t.Fatal("tool should have executed with rewritten args")
	}
	if !strings.Contains(string(gotArgs), "echo safe") {
		t.Fatalf("args not rewritten by updatedInput, got %q", string(gotArgs))
	}
}

type capturingTool struct {
	name     string
	got      *json.RawMessage
	captured *bool
}

func (t capturingTool) Name() string            { return t.name }
func (t capturingTool) Description() string     { return "capture" }
func (t capturingTool) Schema() json.RawMessage { return nil }
func (t capturingTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (t capturingTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	*t.got = args
	*t.captured = true
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("ok")}}, nil
}

func textOfMsg(msg agentcore.ToolResultMessage) string {
	if len(msg.Content) == 0 {
		return ""
	}
	if tc, ok := msg.Content[0].(agentcore.TextContent); ok {
		return tc.Text
	}
	return ""
}

func allTextOfMsg(msg agentcore.ToolResultMessage) string {
	var b strings.Builder
	for _, c := range msg.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}
