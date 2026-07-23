package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// execTool is a configurable AgentTool for executor tests.
type execTool struct {
	name   string
	schema string
	run    func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error)
	mode   agentcore.ToolExecutionMode
}

func (t execTool) Name() string        { return t.name }
func (t execTool) Description() string { return "exec" }
func (t execTool) Schema() json.RawMessage {
	if t.schema == "" {
		return nil
	}
	return json.RawMessage(t.schema)
}
func (t execTool) ExecutionMode() agentcore.ToolExecutionMode {
	if t.mode == "" {
		return agentcore.ToolExecutionParallel
	}
	return t.mode
}
func (t execTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return t.run(ctx, id, args, onUpdate)
}

func newExecCfg(t *testing.T, tool agentcore.AgentTool) ToolExecutorConfig {
	t.Helper()
	r := NewToolRegistry()
	if err := r.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	return ToolExecutorConfig{Registry: r}
}

func textOf(msg agentcore.ToolResultMessage) string {
	if len(msg.Content) == 0 {
		return ""
	}
	if tc, ok := msg.Content[0].(agentcore.TextContent); ok {
		return tc.Text
	}
	return ""
}

func TestExecutorNormal(t *testing.T) {
	tool := execTool{name: "echo", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("done")}}, nil
	}}
	cfg := newExecCfg(t, tool)

	var events []agentcore.AgentEvent
	emit := func(ctx context.Context, ev agentcore.AgentEvent) error { events = append(events, ev); return nil }
	msg, term := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "echo"}, emit)

	if msg.IsError || textOf(msg) != "done" {
		t.Fatalf("normal result wrong: %+v", msg)
	}
	if term {
		t.Error("normal result should not terminate")
	}
	wantKinds := []string{agentcore.EventToolExecutionStart, agentcore.EventToolExecutionEnd}
	if len(events) != 2 || events[0].EventType() != wantKinds[0] || events[1].EventType() != wantKinds[1] {
		t.Errorf("events wrong: %+v", events)
	}
}

func TestExecutorUnknownTool(t *testing.T) {
	cfg := ToolExecutorConfig{Registry: NewToolRegistry()}
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "ghost"}, nil)
	if !msg.IsError {
		t.Fatalf("unknown tool should be error result: %+v", msg)
	}
}

func TestExecutorValidationFailure(t *testing.T) {
	schema := `{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`
	tool := execTool{name: "need", schema: schema, run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		t.Fatal("execute must not run on validation failure")
		return agentcore.AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "need", Arguments: json.RawMessage(`{}`)}, nil)
	if !msg.IsError {
		t.Fatalf("validation failure should be error result: %+v", msg)
	}
}

func TestExecutorBlock(t *testing.T) {
	tool := execTool{name: "echo", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		t.Fatal("execute must not run when blocked")
		return agentcore.AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Block: true}
	}
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "echo"}, nil)
	if !msg.IsError {
		t.Fatalf("blocked call should be error result: %+v", msg)
	}
}

func TestExecutorToolError(t *testing.T) {
	tool := execTool{name: "boom", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{}, errors.New("kaboom")
	}}
	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "boom"}, nil)
	if !msg.IsError {
		t.Fatalf("tool error should be error result: %+v", msg)
	}
}

func TestExecutorPanicRecovered(t *testing.T) {
	tool := execTool{name: "panic", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		panic("oops")
	}}
	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "panic"}, nil)
	if !msg.IsError {
		t.Fatalf("panic should be recovered into error result: %+v", msg)
	}
}

func TestExecutorAfterToolCallOverride(t *testing.T) {
	tool := execTool{name: "echo", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("orig")}}, nil
	}}
	cfg := newExecCfg(t, tool)
	newContent := agentcore.ContentList{agentcore.NewTextContent("overridden")}
	isErr := true
	term := true
	cfg.AfterToolCall = func(ctx context.Context, call agentcore.AgentToolCall, result agentcore.AgentToolResult, isError bool) *agentcore.AfterToolCallResult {
		return &agentcore.AfterToolCallResult{Content: &newContent, IsError: &isErr, Terminate: &term}
	}
	msg, terminate := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "echo"}, nil)
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
	tool := execTool{name: "stream", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		onUpdate(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("partial")}})
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("final")}}, nil
	}}
	cfg := newExecCfg(t, tool)
	var updates int
	emit := func(ctx context.Context, ev agentcore.AgentEvent) error {
		if ev.EventType() == agentcore.EventToolExecutionUpdate {
			updates++
		}
		return nil
	}
	executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "stream"}, emit)
	if updates != 1 {
		t.Errorf("expected 1 update event, got %d", updates)
	}
}

func TestExecutorAbortedContext(t *testing.T) {
	tool := execTool{name: "echo", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		t.Fatal("execute must not run when context already cancelled")
		return agentcore.AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg, _ := executeToolCall(ctx, cfg, agentcore.AgentToolCall{ID: "1", Name: "echo"}, nil)
	if !msg.IsError {
		t.Fatalf("aborted call should be error result: %+v", msg)
	}
}

// TestExecutorResultBudget proves the executor-layer byte budget applies to any
// tool: a stub tool emitting output larger than the budget gets its result text
// truncated with an accurate "[truncated N bytes]" marker, while a small output
// is left untouched.
func TestExecutorResultBudget(t *testing.T) {
	const budget = 1000
	big := strings.Repeat("A", budget) + strings.Repeat("B", budget) // 2*budget bytes
	tool := execTool{name: "flood", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(big)}}, nil
	}}
	cfg := newExecCfg(t, tool)
	cfg.MaxResultBytes = budget

	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "flood"}, nil)
	got := textOf(msg)
	if len(got) >= len(big) {
		t.Fatalf("output not truncated: len=%d, original=%d", len(got), len(big))
	}
	half := budget / 2
	removed := len(big) - 2*half
	marker := fmt.Sprintf("\n[truncated %d bytes]\n", removed)
	if !strings.Contains(got, marker) {
		t.Fatalf("missing/incorrect truncation marker %q in output %q", marker, got)
	}
	want := big[:half] + marker + big[len(big)-half:]
	if got != want {
		t.Fatalf("truncated output mismatch:\n got=%q\nwant=%q", got, want)
	}
}

// TestExecutorResultBudgetSmallOutputUntouched proves outputs within budget are
// passed through verbatim.
func TestExecutorResultBudgetSmallOutputUntouched(t *testing.T) {
	small := "just a little output"
	tool := execTool{name: "tiny", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(small)}}, nil
	}}
	cfg := newExecCfg(t, tool)
	cfg.MaxResultBytes = 1000

	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "tiny"}, nil)
	if got := textOf(msg); got != small {
		t.Fatalf("small output altered: got=%q want=%q", got, small)
	}
}

// TestExecutorResultBudgetDefault proves the default (zero MaxResultBytes) uses
// toolResultMaxBytes and truncates output beyond it.
func TestExecutorResultBudgetDefault(t *testing.T) {
	big := strings.Repeat("x", toolResultMaxBytes+5000)
	tool := execTool{name: "flood", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(big)}}, nil
	}}
	cfg := newExecCfg(t, tool) // MaxResultBytes == 0 -> default

	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "flood"}, nil)
	got := textOf(msg)
	if !strings.Contains(got, "[truncated ") {
		t.Fatalf("default budget did not truncate: len=%d", len(got))
	}
	if len(got) > toolResultMaxBytes+64 {
		t.Fatalf("default-truncated output too large: %d", len(got))
	}
}

// TestExecutorResultBudgetDisabled proves a negative MaxResultBytes disables the
// budget entirely.
func TestExecutorResultBudgetDisabled(t *testing.T) {
	big := strings.Repeat("y", toolResultMaxBytes*2)
	tool := execTool{name: "flood", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(big)}}, nil
	}}
	cfg := newExecCfg(t, tool)
	cfg.MaxResultBytes = -1

	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "flood"}, nil)
	if got := textOf(msg); got != big {
		t.Fatalf("disabled budget altered output: len=%d want=%d", len(got), len(big))
	}
}

// --- Tool-execution retry (node #252) ---------------------------------------

// countingTool returns a transient error for its first failN attempts, then
// succeeds; if failN < 0 it always fails. It records how many times Execute ran.
type retryStub struct {
	failN   int   // number of leading failures before success; <0 = always fail
	err     error // error to return on a failing attempt
	attempts int32
}

func (s *retryStub) run(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	n := atomic.AddInt32(&s.attempts, 1)
	if s.failN < 0 || int(n) <= s.failN {
		return agentcore.AgentToolResult{}, s.err
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("ok")}}, nil
}

func newRetryCfg(t *testing.T, name string, s *retryStub) ToolExecutorConfig {
	t.Helper()
	return newExecCfg(t, execTool{name: name, run: s.run})
}

func TestExecutorRetryTransientThenSuccess(t *testing.T) {
	// Fails 2 times with a transient error, then succeeds. With the default cap
	// (2 retries = 3 attempts) this should ultimately succeed on attempt 3.
	s := &retryStub{failN: 2, err: syscall.ECONNRESET}
	cfg := newRetryCfg(t, "flaky", s)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "flaky"}, nil)
	if msg.IsError {
		t.Fatalf("expected eventual success, got error: %q", textOf(msg))
	}
	if got := atomic.LoadInt32(&s.attempts); got != 3 {
		t.Fatalf("expected 3 attempts (2 retries), got %d", got)
	}
}

func TestExecutorRetryCapExhausted(t *testing.T) {
	// Always fails with a transient error: must stop after maxToolRetries+1
	// attempts (default cap) and give up with an error result.
	s := &retryStub{failN: -1, err: syscall.ETIMEDOUT}
	cfg := newRetryCfg(t, "always", s)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "always"}, nil)
	if !msg.IsError {
		t.Fatalf("expected error result after exhausting retries: %+v", msg)
	}
	want := int32(maxToolRetries + 1)
	if got := atomic.LoadInt32(&s.attempts); got != want {
		t.Fatalf("expected %d attempts, got %d", want, got)
	}
}

func TestExecutorRetryTerminalNoRetry(t *testing.T) {
	// A terminal (non-transient) error must be tried exactly once.
	s := &retryStub{failN: -1, err: errors.New("invalid argument: bad")}
	cfg := newRetryCfg(t, "terminal", s)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "terminal"}, nil)
	if !msg.IsError {
		t.Fatalf("expected error result: %+v", msg)
	}
	if got := atomic.LoadInt32(&s.attempts); got != 1 {
		t.Fatalf("terminal error must not retry: got %d attempts", got)
	}
}

func TestExecutorRetryDisabled(t *testing.T) {
	// MaxToolRetries < 0 disables retry even for a transient error.
	s := &retryStub{failN: -1, err: syscall.ECONNRESET}
	cfg := newRetryCfg(t, "notretry", s)
	cfg.MaxToolRetries = -1
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "notretry"}, nil)
	if !msg.IsError {
		t.Fatalf("expected error result: %+v", msg)
	}
	if got := atomic.LoadInt32(&s.attempts); got != 1 {
		t.Fatalf("disabled retry must try once: got %d attempts", got)
	}
}

func TestExecutorRetryCustomCap(t *testing.T) {
	// A custom positive cap is honored: always-failing transient error stops at
	// cap+1 attempts.
	s := &retryStub{failN: -1, err: syscall.EAGAIN}
	cfg := newRetryCfg(t, "custom", s)
	cfg.MaxToolRetries = 4
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "custom"}, nil)
	if !msg.IsError {
		t.Fatalf("expected error result: %+v", msg)
	}
	if got := atomic.LoadInt32(&s.attempts); got != 5 {
		t.Fatalf("expected 5 attempts (cap 4), got %d", got)
	}
}

func TestExecutorRetryCanceledContextNoRetry(t *testing.T) {
	// context.Canceled surfaced by the tool must never be retried.
	s := &retryStub{failN: -1, err: context.Canceled}
	cfg := newRetryCfg(t, "cancel", s)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "cancel"}, nil)
	if !msg.IsError {
		t.Fatalf("expected error result: %+v", msg)
	}
	if got := atomic.LoadInt32(&s.attempts); got != 1 {
		t.Fatalf("context.Canceled must not retry: got %d attempts", got)
	}
}

func TestExecutorRetryStopsWhenOuterCtxCanceled(t *testing.T) {
	// If the outer ctx is cancelled during execution, retries stop even though
	// the returned error is transient.
	ctx, cancel := context.WithCancel(context.Background())
	s := &retryStub{failN: -1, err: syscall.ECONNRESET}
	tool := execTool{name: "abortmid", run: func(c context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		atomic.AddInt32(&s.attempts, 1)
		cancel() // outer ctx dies after the first attempt
		return agentcore.AgentToolResult{}, syscall.ECONNRESET
	}}
	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(ctx, cfg, agentcore.AgentToolCall{ID: "1", Name: "abortmid"}, nil)
	if !msg.IsError {
		t.Fatalf("expected error result: %+v", msg)
	}
	if got := atomic.LoadInt32(&s.attempts); got != 1 {
		t.Fatalf("cancelled outer ctx must stop retry: got %d attempts", got)
	}
}

func TestIsRetryableToolError(t *testing.T) {
	transient := []error{
		syscall.ETIMEDOUT,
		syscall.ECONNRESET,
		syscall.EAGAIN,
		context.DeadlineExceeded,
		os.ErrDeadlineExceeded,
		fmt.Errorf("dial tcp: %w", syscall.ECONNRESET),
		errors.New("connection refused"),
		errors.New("resource temporarily unavailable"),
		errors.New("read: i/o timeout"),
		&net.DNSError{IsTimeout: true},
	}
	for _, err := range transient {
		if !isRetryableToolError(err) {
			t.Errorf("expected transient (retryable): %v", err)
		}
	}
	terminal := []error{
		nil,
		context.Canceled,
		fmt.Errorf("wrapped: %w", context.Canceled),
		errors.New("file not found"),
		errors.New("invalid argument"),
		os.ErrNotExist,
		toolPanic{value: "boom"},
	}
	for _, err := range terminal {
		if isRetryableToolError(err) {
			t.Errorf("expected terminal (not retryable): %v", err)
		}
	}
}

// TestExecutorRetrySuccessResultWithIsErrorNotRetried proves that a
// (result, nil) whose own content signals an error is NOT retried: only a
// non-nil Go error triggers retry.
func TestExecutorRetrySuccessResultWithIsErrorNotRetried(t *testing.T) {
	var attempts int32
	term := false
	tool := execTool{name: "toolerr", run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		atomic.AddInt32(&attempts, 1)
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("tool-level error")}, Terminate: &term}, nil
	}}
	cfg := newExecCfg(t, tool)
	// afterToolCall marks it as an error result; still must not be retried.
	isErr := true
	cfg.AfterToolCall = func(ctx context.Context, call agentcore.AgentToolCall, result agentcore.AgentToolResult, isError bool) *agentcore.AfterToolCallResult {
		return &agentcore.AfterToolCallResult{IsError: &isErr}
	}
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "1", Name: "toolerr"}, nil)
	if !msg.IsError {
		t.Fatalf("expected error result from afterToolCall override")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("(result,nil) must not be retried: got %d attempts", got)
	}
}

