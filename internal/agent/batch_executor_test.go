package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// registerAll builds a registry containing every tool given.
func registerAll(t *testing.T, tools ...AgentTool) *ToolRegistry {
	t.Helper()
	r := NewToolRegistry()
	for _, tool := range tools {
		if err := r.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name(), err)
		}
	}
	return r
}

// echoTool returns its name as text; optionally terminates.
func echoTool(name string, mode ToolExecutionMode, terminate bool) execTool {
	return execTool{
		name: name,
		mode: mode,
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
			term := terminate
			return AgentToolResult{Content: ContentList{NewTextContent(name)}, Terminate: &term}, nil
		},
	}
}

func callsFor(names ...string) []AgentToolCall {
	calls := make([]AgentToolCall, len(names))
	for i, n := range names {
		calls[i] = AgentToolCall{ID: fmt.Sprintf("c%d", i), Name: n, Arguments: json.RawMessage(`{}`)}
	}
	return calls
}

// TestBatchParallelPreservesOrder verifies that parallel execution backfills
// results at their source index regardless of completion order.
func TestBatchParallelPreservesOrder(t *testing.T) {
	// t0 sleeps longest, t2 shortest — so completion order is reversed, but the
	// result slice must still be [t0, t1, t2].
	mk := func(name string, delay time.Duration) execTool {
		return execTool{
			name: name,
			mode: ToolExecutionParallel,
			run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
				time.Sleep(delay)
				return AgentToolResult{Content: ContentList{NewTextContent(name)}}, nil
			},
		}
	}
	reg := registerAll(t, mk("t0", 30*time.Millisecond), mk("t1", 15*time.Millisecond), mk("t2", 1*time.Millisecond))
	cfg := BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}}

	results, term := executeToolCalls(context.Background(), cfg, callsFor("t0", "t1", "t2"), nil)
	if term {
		t.Errorf("no tool terminates; batch must not terminate")
	}
	want := []string{"t0", "t1", "t2"}
	for i, w := range want {
		if got := textOf(results[i]); got != w {
			t.Errorf("result[%d] = %q, want %q (order not preserved)", i, got, w)
		}
	}
}

// TestBatchParallelRunsConcurrently confirms parallel tools overlap in time.
func TestBatchParallelRunsConcurrently(t *testing.T) {
	var mu sync.Mutex
	running := 0
	maxConcurrent := 0
	block := make(chan struct{})
	var started sync.WaitGroup
	started.Add(3)

	mk := func(name string) execTool {
		return execTool{
			name: name,
			mode: ToolExecutionParallel,
			run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
				mu.Lock()
				running++
				if running > maxConcurrent {
					maxConcurrent = running
				}
				mu.Unlock()
				started.Done()
				<-block // hold until all have started
				mu.Lock()
				running--
				mu.Unlock()
				return AgentToolResult{Content: ContentList{NewTextContent(name)}}, nil
			},
		}
	}
	reg := registerAll(t, mk("a"), mk("b"), mk("c"))
	cfg := BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}}

	go func() {
		started.Wait()
		close(block)
	}()
	executeToolCalls(context.Background(), cfg, callsFor("a", "b", "c"), nil)

	if maxConcurrent < 3 {
		t.Errorf("expected 3 concurrent tools, saw max %d", maxConcurrent)
	}
}

// TestBatchSequentialWhenAnyToolSequential forces serial execution and records
// the order tools actually ran in.
func TestBatchSequentialWhenAnyToolSequential(t *testing.T) {
	var mu sync.Mutex
	var order []string
	mk := func(name string, mode ToolExecutionMode) execTool {
		return execTool{
			name: name,
			mode: mode,
			run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				return AgentToolResult{Content: ContentList{NewTextContent(name)}}, nil
			},
		}
	}
	// "b" is sequential → whole batch runs serially in source order.
	reg := registerAll(t, mk("a", ToolExecutionParallel), mk("b", ToolExecutionSequential), mk("c", ToolExecutionParallel))
	cfg := BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}}

	executeToolCalls(context.Background(), cfg, callsFor("a", "b", "c"), nil)
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("sequential order = %v, want %v", order, want)
		}
	}
}

// TestBatchForceSequential verifies the global ForceSequential flag serializes
// even all-parallel tools.
func TestBatchForceSequential(t *testing.T) {
	var mu sync.Mutex
	var order []string
	mk := func(name string) execTool {
		return execTool{
			name: name,
			mode: ToolExecutionParallel,
			run: func(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				return AgentToolResult{Content: ContentList{NewTextContent(name)}}, nil
			},
		}
	}
	reg := registerAll(t, mk("a"), mk("b"))
	cfg := BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}, ForceSequential: true}

	executeToolCalls(context.Background(), cfg, callsFor("a", "b"), nil)
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Errorf("force-sequential order = %v, want [a b]", order)
	}
}

// TestBatchTerminateOnlyWhenAll checks the whole-batch terminate semantics.
func TestBatchTerminateOnlyWhenAll(t *testing.T) {
	// Mixed: one terminates, one does not → batch must NOT terminate.
	reg := registerAll(t, echoTool("term", ToolExecutionParallel, true), echoTool("noterm", ToolExecutionParallel, false))
	cfg := BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}}
	_, term := executeToolCalls(context.Background(), cfg, callsFor("term", "noterm"), nil)
	if term {
		t.Errorf("batch with one non-terminating tool must not terminate")
	}

	// All terminate → batch terminates.
	reg2 := registerAll(t, echoTool("t1", ToolExecutionParallel, true), echoTool("t2", ToolExecutionParallel, true))
	cfg2 := BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg2}}
	_, term2 := executeToolCalls(context.Background(), cfg2, callsFor("t1", "t2"), nil)
	if !term2 {
		t.Errorf("batch with all terminating tools must terminate")
	}
}

// TestBatchSequentialAbort verifies that aborting mid-batch fills the remaining
// calls with aborted error results.
func TestBatchSequentialAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mk := func(name string) execTool {
		return execTool{
			name: name,
			mode: ToolExecutionSequential,
			run: func(c context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
				cancel() // abort after the first tool starts
				return AgentToolResult{Content: ContentList{NewTextContent(name)}}, nil
			},
		}
	}
	reg := registerAll(t, mk("first"), echoTool("second", ToolExecutionSequential, false))
	cfg := BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}}

	results, _ := executeToolCalls(ctx, cfg, callsFor("first", "second"), nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[1].IsError {
		t.Errorf("second (post-abort) result must be an error result")
	}
}

// TestBatchEmpty covers the empty-batch fast path.
func TestBatchEmpty(t *testing.T) {
	cfg := BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: NewToolRegistry()}}
	results, term := executeToolCalls(context.Background(), cfg, nil, nil)
	if results != nil || term {
		t.Errorf("empty batch must return (nil, false), got (%v, %v)", results, term)
	}
}
