package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestBashToolSmallOutputNotTruncated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	res, gerr := runBash(t, tool, map[string]any{"command": "echo hello world"}, nil)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	out := resultText(res)
	if strings.Contains(out, "truncated") {
		t.Errorf("small output should not be truncated, got %q", out)
	}
	if strings.TrimSpace(out) != "hello world" {
		t.Errorf("output = %q, want %q", out, "hello world")
	}
}

func TestBashToolLargeOutputTruncatedHeadTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	// Emit a marker at the very start and very end, with a large filler between,
	// so we can prove both the head and the tail survive truncation.
	total := bashMaxOutputBytes * 3
	filler := bashMaxOutputBytes // bytes of 'x' between the two markers
	cmd := fmt.Sprintf("printf 'HEADMARK'; head -c %d /dev/zero | tr '\\0' 'x'; printf 'TAILMARK'", filler)
	_ = total
	res, gerr := runBash(t, tool, map[string]any{"command": cmd}, nil)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	out := resultText(res)
	if len(out) > bashMaxOutputBytes+128 {
		t.Errorf("truncated output too long: %d bytes (cap %d)", len(out), bashMaxOutputBytes)
	}
	if !strings.HasPrefix(out, "HEADMARK") {
		t.Errorf("head not preserved; output starts with %q", out[:min(16, len(out))])
	}
	if !strings.HasSuffix(out, "TAILMARK") {
		t.Errorf("tail not preserved; output ends with %q", out[max(0, len(out)-16):])
	}
	if !strings.Contains(out, "[truncated ") || !strings.Contains(out, " bytes]") {
		t.Errorf("missing truncation marker in %q", out)
	}
}

func TestTruncateBashOutputByteCount(t *testing.T) {
	// A pure-ASCII input of a known size: the marker's N must equal the exact
	// number of middle bytes dropped, i.e. total - head - tail.
	total := bashMaxOutputBytes * 2
	in := strings.Repeat("z", total)
	out := truncateBashOutput(in)

	half := bashMaxOutputBytes / 2
	// For all-ASCII input no rune-boundary trimming happens, so head/tail are
	// each exactly half and N = total - 2*half.
	wantRemoved := total - 2*half
	wantMarker := fmt.Sprintf("[truncated %d bytes]", wantRemoved)
	if !strings.Contains(out, wantMarker) {
		t.Errorf("marker = ...%q..., want to contain %q", out, wantMarker)
	}
	if got := strings.Count(out, "z"); got != 2*half {
		t.Errorf("preserved %d content bytes, want %d (head+tail)", got, 2*half)
	}

	// Input at or below the cap is returned verbatim.
	small := strings.Repeat("b", bashMaxOutputBytes)
	if got := truncateBashOutput(small); got != small {
		t.Errorf("input at cap should be unchanged")
	}
}

