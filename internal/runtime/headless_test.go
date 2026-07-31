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

// TestRunHeadlessStreamJSONSessionID verifies that when RunConfig.SessionID is
// set, the first stream-json event (agent_start) carries it under "sessionId",
// so a consumer can associate the run's output with a session and resume it
// later (对标 pi/Claude Code). When SessionID is empty the key is omitted.
func TestRunHeadlessStreamJSONSessionID(t *testing.T) {
	run := func(sessionID string) map[string]any {
		p := &fauxProvider{
			name:   "faux",
			models: []provider.Model{{Provider: "faux", ID: "faux"}},
			turns:  []fauxTurn{textTurn("done")},
		}
		cfg := newFauxRunCfg(p)
		cfg.SessionID = sessionID
		var out bytes.Buffer
		agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}}}}
		if err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Mode: StreamJSONMode, Out: &out}); err != nil {
			t.Fatalf("RunHeadless stream-json: unexpected error %v", err)
		}
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
			if env["type"] == agentcore.EventAgentStart {
				return env
			}
		}
		t.Fatal("no agent_start event found")
		return nil
	}

	first := run("sess-123")
	if got, ok := first["sessionId"].(string); !ok || got != "sess-123" {
		t.Errorf("agent_start sessionId = %v, want %q", first["sessionId"], "sess-123")
	}

	none := run("")
	if _, present := none["sessionId"]; present {
		t.Errorf("agent_start must omit sessionId when SessionID is empty, got %v", none["sessionId"])
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

// emitTool returns a tool that surfaces ev on the run stream via the run-level
// progress emitter the loop injects into ctx (WithProgressEmitter), then returns
// a trivial text result. This mirrors how a dispatched sub-agent surfaces a
// SubAgentProgressEvent up the parent stream.
func emitTool(name string, ev agentcore.AgentEvent) execTool {
	return execTool{
		name: name,
		mode: agentcore.ToolExecutionParallel,
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			if emit := agentcore.ProgressEmitterFromContext(ctx); emit != nil {
				_ = emit(ctx, ev)
			}
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(name)}}, nil
		},
	}
}

// TestRunHeadlessSubAgentProgressToStderr verifies the D-9 contract: a
// SubAgentProgressEvent emitted during the run is rendered as a human-readable
// line to the progress writer (stderr) and is NEVER serialised onto stdout —
// neither the final result text nor the stream-json envelope stream may contain
// it. The event is injected via a faux tool whose execution fires it on the
// run's event stream (the same seam the loop uses).
func TestRunHeadlessSubAgentProgressToStderr(t *testing.T) {
	const desc = "investigate the parser"
	const activity = "Editing"

	run := func(mode HeadlessMode) (stdout, stderr string) {
		p := &fauxProvider{
			name:   "faux",
			models: []provider.Model{{Provider: "faux", ID: "faux"}},
			turns: []fauxTurn{
				toolCallTurn("call-1", "task", `{"description":"investigate the parser"}`),
				textTurn("done"),
			},
		}
		// The tool emits a SubAgentProgressEvent onto the run stream, mimicking a
		// dispatched sub-agent surfacing progress up the parent stream.
		tool := emitTool("task", agentcore.SubAgentProgressEvent{
			ToolCallID:  "call-1",
			Description: desc,
			Activity:    activity,
		})
		cfg := newFauxRunCfg(p, tool)
		var out, prog bytes.Buffer
		agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}}}}
		if err := RunHeadless(context.Background(), agentCtx, HeadlessConfig{Run: cfg, Mode: mode, Out: &out, Progress: &prog}); err != nil {
			t.Fatalf("RunHeadless: unexpected error %v", err)
		}
		return out.String(), prog.String()
	}

	for _, mode := range []struct {
		name string
		mode HeadlessMode
	}{{"print", PrintMode}, {"stream-json", StreamJSONMode}} {
		t.Run(mode.name, func(t *testing.T) {
			stdout, stderr := run(mode.mode)
			// (a) stderr carries the progress line with description + activity.
			if !strings.Contains(stderr, desc) || !strings.Contains(stderr, activity) {
				t.Errorf("stderr = %q, want it to contain description %q and activity %q", stderr, desc, activity)
			}
			// (b) stdout must not contain the progress event in any form.
			if strings.Contains(stdout, "subagent_progress") {
				t.Errorf("stdout must not contain the subagent_progress envelope, got %q", stdout)
			}
			if strings.Contains(stdout, desc) {
				t.Errorf("stdout must not leak the progress description, got %q", stdout)
			}
		})
	}
}

// TestWriteProgressLineEmptyDescription verifies the line degrades gracefully to
// the activity alone when the task supplied no description.
func TestWriteProgressLineEmptyDescription(t *testing.T) {
	var buf bytes.Buffer
	writeProgressLine(&buf, agentcore.SubAgentProgressEvent{Activity: "Thinking"})
	got := buf.String()
	if !strings.Contains(got, "Thinking") {
		t.Errorf("line = %q, want it to contain the activity", got)
	}
	if strings.Contains(got, "·") {
		t.Errorf("line = %q, want no separator when description is empty", got)
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
