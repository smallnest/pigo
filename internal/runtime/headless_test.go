package runtime

// This file is the end-to-end test for the headless / stdio run modes (US-020,
// #39). It drives RunHeadless over the real faux provider seam (no loop-internal
// mocking) and asserts the two output contracts — PrintMode's final text and
// StreamJSONMode's line-delimited JSON events — plus the success/failure signal
// that the CLI maps to a process exit code.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// TestRunHeadlessPrintMode runs a text→tool→text scenario through RunHeadless in
// PrintMode and asserts that only the final assistant text reaches the writer,
// terminated by a newline, and that the run reports success (nil error).
func TestRunHeadlessPrintMode(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "echo", `{"msg":"hi"}`), // turn 1: tool call
			textTurn("final answer"),                       // turn 2: final text
		},
	}
	cfg := newFauxRunCfg(p, echoTool("echo", agentcore.ToolExecutionParallel, false))
	var out bytes.Buffer
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}}}}

	err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Mode: PrintMode, Out: &out})
	if err != nil {
		t.Fatalf("RunHeadless print mode: unexpected error %v", err)
	}
	got := out.String()
	if got != "final answer\n" {
		t.Errorf("print mode output = %q, want %q", got, "final answer\n")
	}
}

// TestRunHeadlessStreamJSON runs the same scenario in StreamJSONMode and asserts
// every line is a valid JSON object carrying a "type" discriminant, that the run
// is bracketed by agent_start/agent_end, and that a tool execution is reported —
// the machine-readable protocol a parent process consumes.
func TestRunHeadlessStreamJSON(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "echo", `{"msg":"hi"}`),
			textTurn("done"),
		},
	}
	cfg := newFauxRunCfg(p, echoTool("echo", agentcore.ToolExecutionParallel, false))
	var out bytes.Buffer
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}}}}

	if err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Mode: StreamJSONMode, Out: &out}); err != nil {
		t.Fatalf("RunHeadless stream-json: unexpected error %v", err)
	}

	var types []string
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("stream-json line is not valid JSON: %q (%v)", line, err)
		}
		typ, ok := env["type"].(string)
		if !ok || typ == "" {
			t.Errorf("stream-json line missing type discriminant: %q", line)
		}
		types = append(types, typ)
	}
	if len(types) == 0 {
		t.Fatal("stream-json produced no event lines")
	}
	if types[0] != agentcore.EventAgentStart || types[len(types)-1] != agentcore.EventAgentEnd {
		t.Errorf("stream must be bracketed by agent_start/agent_end, got %v", types)
	}
	if !contains(types, agentcore.EventToolExecutionEnd) {
		t.Errorf("expected a tool_execution_end event, got %v", types)
	}
}

// TestRunHeadlessReportsFailure verifies that a run whose final assistant message
// carries stopReason=error surfaces as an ErrRunFailed, so the CLI maps it to a
// non-zero exit code.
func TestRunHeadlessReportsFailure(t *testing.T) {
	errPartial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	errFinal := errPartial
	errFinal.StopReason = agentcore.StopReasonError
	errFinal.ErrorMessage = "boom"
	p := &fauxProvider{
		name: "faux",
		turns: []fauxTurn{
			{
				provider.StreamStartEvent{Partial: errPartial},
				provider.StreamDoneEvent{Message: errFinal},
			},
		},
	}
	cfg := newFauxRunCfg(p)
	var out bytes.Buffer
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}

	err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Mode: PrintMode, Out: &out})
	if err == nil {
		t.Fatal("run ending in stopReason=error must return a non-nil error")
	}
	var failed *ErrRunFailed
	if !as(err, &failed) {
		t.Fatalf("error = %T (%v), want *ErrRunFailed", err, err)
	}
	if !strings.Contains(failed.Error(), "boom") {
		t.Errorf("error message = %q, want it to mention the failure reason", failed.Error())
	}
}

// TestRunHeadlessNilWriter guards the misconfiguration path.
func TestRunHeadlessNilWriter(t *testing.T) {
	p := &fauxProvider{turns: []fauxTurn{textTurn("x")}}
	cfg := newFauxRunCfg(p)
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser}}}
	if err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Out: nil}); err == nil {
		t.Fatal("nil output writer must be rejected")
	}
}

// contains reports whether s contains v.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// as is a tiny errors.As shim kept local to avoid an extra import in a test that
// only ever unwraps one level.
func as(err error, target **ErrRunFailed) bool {
	if e, ok := err.(*ErrRunFailed); ok {
		*target = e
		return true
	}
	return false
}
