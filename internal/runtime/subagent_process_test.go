package runtime

// Tests for process-isolated sub-agents (US-019, #135): the Isolation field,
// the process-mode Execute path (params shaping, crash-as-tool-error), the
// shared RunSubAgentOnce core, and the real defaultProcessCall transport
// (exec + stdio JSON-RPC + crash handling) driven through a tiny compiled
// helper binary - no provider or network, mirroring the plugin tests.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	goruntime "runtime"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// errorTurn scripts a turn whose final message stops on StopReasonError, so a
// RunSubAgentOnce run surfaces as a failure (matching the goroutine-mode
// "failed run -> tool error" contract).
func errorTurn(msg string) fauxTurn {
	partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	final := partial
	final.StopReason = agentcore.StopReasonError
	final.ErrorMessage = msg
	return fauxTurn{
		provider.StreamStartEvent{Partial: partial},
		provider.StreamDoneEvent{Message: final},
	}
}

// TestSubAgentGoroutineModeUnchanged verifies the default (zero-value) isolation
// still runs in-process: the existing parent->child->parent tests cover the
// full path, but this pins that Isolation==goroutine is the default and reaches
// NewRunConfig (not the process path) so the regression is caught locally too.
func TestSubAgentGoroutineModeUnchanged(t *testing.T) {
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{textTurn("child answer")},
	}
	sub := NewSubAgentTool(SubAgentSpec{
		Name:         "researcher",
		SystemPrompt: "you are a researcher",
		NewRunConfig: func() RunConfig { return newFauxRunCfg(child) },
	})
	if sub.spec.Isolation != SubAgentIsolationGoroutine {
		t.Errorf("default Isolation = %v, want goroutine", sub.spec.Isolation)
	}
	res, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
	if err != nil {
		t.Fatalf("goroutine Execute err = %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "child answer" {
		t.Errorf("goroutine result = %q, want 'child answer'", got)
	}
	if child.callCount() != 1 {
		t.Errorf("child provider calls = %d, want 1", child.callCount())
	}
}

// TestSubAgentGoroutineNilRunConfigErrors verifies goroutine mode reports a
// missing NewRunConfig, and - preserving the original precedence - reports it
// even when the prompt is empty (so a nil config is not masked by "empty
// prompt"). This pins the L3 regression: the NewRunConfig check stays before
// the empty-prompt check on the goroutine path.
func TestSubAgentGoroutineNilRunConfigErrors(t *testing.T) {
	sub := NewSubAgentTool(SubAgentSpec{Name: "x"}) // no NewRunConfig, default goroutine
	_, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "no run configuration") {
		t.Errorf("err = %v, want 'no run configuration'", err)
	}
	// Precedence: nil NewRunConfig is reported even when the prompt is empty.
	_, err = sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":""}`), nil)
	if err == nil || !strings.Contains(err.Error(), "no run configuration") {
		t.Errorf("empty-prompt err = %v, want 'no run configuration' (nil config takes precedence)", err)
	}
}

// TestSubAgentProcessModeFake drives process mode through an injectable
// processCall (the test seam) to verify params shaping and crash-as-tool-error
// without a real subprocess. The real transport is covered separately by
// TestSubAgentProcessDefaultCall.
func TestSubAgentProcessModeFake(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		sub := NewSubAgentTool(SubAgentSpec{
			Name:         "proc",
			Isolation:    SubAgentIsolationProcess,
			Process:      SubAgentProcessConfig{Model: "faux"},
			SystemPrompt: "you are a subprocess child",
		})
		var got SubAgentRunParams
		sub.processCall = func(ctx context.Context, cfg SubAgentProcessConfig, params SubAgentRunParams) (string, error) {
			got = params
			return "process result: 99", nil
		}
		res, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"find it"}`), nil)
		if err != nil {
			t.Fatalf("Execute err = %v", err)
		}
		if got := agentcore.ContentToText(res.Content); got != "process result: 99" {
			t.Errorf("result = %q, want 'process result: 99'", got)
		}
		// The prompt, system prompt, and model are forwarded to the subprocess;
		// NewRunConfig is NOT called (the subprocess resolves its own provider).
		if got.Prompt != "find it" || got.Model != "faux" || got.SystemPrompt != "you are a subprocess child" {
			t.Errorf("forwarded params = %+v", got)
		}
	})

	t.Run("crash is tool error", func(t *testing.T) {
		sub := NewSubAgentTool(SubAgentSpec{
			Name:      "proc",
			Isolation: SubAgentIsolationProcess,
			Process:   SubAgentProcessConfig{Model: "faux"},
		})
		sub.processCall = func(context.Context, SubAgentProcessConfig, SubAgentRunParams) (string, error) {
			return "", errors.New("subprocess exited: signal: killed")
		}
		_, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"x"}`), nil)
		if err == nil {
			t.Fatal("expected tool error on subprocess crash, got nil")
		}
	})

	t.Run("missing model errors", func(t *testing.T) {
		sub := NewSubAgentTool(SubAgentSpec{
			Name:      "proc",
			Isolation: SubAgentIsolationProcess,
			// Process.Model intentionally empty.
		})
		_, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"x"}`), nil)
		if err == nil {
			t.Fatal("expected error when Process.Model is missing")
		}
	})

	t.Run("forwards tool names", func(t *testing.T) {
		// spec.Tools is in-process; process mode forwards only their names so
		// the subprocess can rebuild builtins by name.
		sub := NewSubAgentTool(SubAgentSpec{
			Name:      "proc",
			Isolation: SubAgentIsolationProcess,
			Process:   SubAgentProcessConfig{Model: "faux"},
			Tools:     []agentcore.AgentTool{nameOnlyTool("read"), nameOnlyTool("grep")},
		})
		var got SubAgentRunParams
		sub.processCall = func(_ context.Context, _ SubAgentProcessConfig, params SubAgentRunParams) (string, error) {
			got = params
			return "ok", nil
		}
		if _, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"x"}`), nil); err != nil {
			t.Fatalf("Execute err = %v", err)
		}
		if len(got.Tools) != 2 || got.Tools[0] != "read" || got.Tools[1] != "grep" {
			t.Errorf("forwarded tool names = %v, want [read grep]", got.Tools)
		}
	})
}

// nameOnlyTool is a minimal AgentTool whose only meaningful attribute is its
// Name, used to verify process mode forwards tool names without needing real
// tool implementations.
func nameOnlyTool(name string) agentcore.AgentTool { return nameOnly{name: name} }

type nameOnly struct{ name string }

func (t nameOnly) Name() string            { return t.name }
func (t nameOnly) Description() string     { return "" }
func (t nameOnly) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (t nameOnly) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}
func (t nameOnly) Execute(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{}, nil
}

// TestRunSubAgentOnce verifies the shared subprocess-side agent core: a normal
// run returns the child's final text, and a run whose final turn stopped on
// error is reported as an error (so the subprocess surfaces it as an RPC error
// and the parent marks the tool result IsError).
func TestRunSubAgentOnce(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		p := &fauxProvider{
			name:   "faux",
			models: []provider.Model{{Provider: "faux", ID: "faux"}},
			turns:  []fauxTurn{textTurn("hello from child")},
		}
		text, err := RunSubAgentOnce(context.Background(), "sys", "do it", nil, newFauxRunCfg(p))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if text != "hello from child" {
			t.Errorf("text = %q, want 'hello from child'", text)
		}
		if p.callCount() != 1 {
			t.Errorf("provider calls = %d, want 1", p.callCount())
		}
	})

	t.Run("failed run errors", func(t *testing.T) {
		p := &fauxProvider{
			name:   "faux",
			models: []provider.Model{{Provider: "faux", ID: "faux"}},
			turns:  []fauxTurn{errorTurn("boom")},
		}
		_, err := RunSubAgentOnce(context.Background(), "sys", "do it", nil, newFauxRunCfg(p))
		if err == nil {
			t.Fatal("expected error for failed child run, got nil")
		}
		// The diagnostic is in ErrorMessage (errorTurn sets no Content); the
		// subprocess must surface it rather than a bare "error" stop reason.
		if !strings.Contains(err.Error(), "boom") {
			t.Errorf("error %q does not contain the 'boom' diagnostic", err.Error())
		}
	})
}

// TestSubAgentProcessDefaultCall exercises the real defaultProcessCall transport
// (exec + stdio JSON-RPC) against a tiny compiled helper binary. It verifies the
// happy round-trip (prompt forwarded, result decoded) and that a crashing
// subprocess is surfaced as a Go error (the AC: "子进程崩溃被父捕获为工具错误").
func TestSubAgentProcessDefaultCall(t *testing.T) {
	bin := buildSubAgentHelper(t)
	cfg := SubAgentProcessConfig{Command: bin}

	t.Run("happy round-trip", func(t *testing.T) {
		text, err := defaultProcessCall(context.Background(), cfg, SubAgentRunParams{
			Prompt: "hello", Model: "faux", SystemPrompt: "sys",
		})
		if err != nil {
			t.Fatalf("defaultProcessCall err = %v", err)
		}
		if text != "echo: hello" {
			t.Errorf("text = %q, want 'echo: hello'", text)
		}
	})

	t.Run("crash is error", func(t *testing.T) {
		_, err := defaultProcessCall(context.Background(), cfg, SubAgentRunParams{
			Prompt: "CRASH", Model: "faux",
		})
		if err == nil {
			t.Fatal("expected error for crashing subprocess, got nil")
		}
	})
}

// buildSubAgentHelper compiles the sub-agent helper binary into a temp dir and
// returns its path. The helper speaks the same JSON-RPC "subagent/run" wire
// format as pigo --subagent-rpc (decoded with plain encoding/json, no internal
// imports, so it stays a standalone main package).
func buildSubAgentHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "subagent_helper.go")
	if err := os.WriteFile(srcPath, []byte(subAgentHelperSrc), 0o644); err != nil {
		t.Fatalf("write helper source: %v", err)
	}
	bin := filepath.Join(dir, "subagent_helper")
	if goruntime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sub-agent helper: %v\n%s", err, out)
	}
	return bin
}

// subAgentHelperSrc is a tiny JSON-RPC server mirroring pigo --subagent-rpc:
// read a "subagent/run" request per line, and either respond with
// {text:"echo: <prompt>"} or, when the prompt is "CRASH", exit without
// responding to simulate a subprocess crash.
const subAgentHelperSrc = `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type params struct {
	Prompt string ` + "`json:\"prompt\"`" + `
}

type request struct {
	ID     *json.RawMessage ` + "`json:\"id\"`" + `
	Method string           ` + "`json:\"method\"`" + `
	Params params           ` + "`json:\"params\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r request
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if r.Params.Prompt == "CRASH" {
			// Simulate a subprocess crash: exit without writing a response so
			// the parent's JSON-RPC reader sees EOF and fails the call.
			os.Exit(1)
		}
		result, _ := json.Marshal(map[string]string{"text": "echo: " + r.Params.Prompt})
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      r.ID,
			"result":  json.RawMessage(result),
		}
		_ = enc.Encode(resp)
	}
}
`
