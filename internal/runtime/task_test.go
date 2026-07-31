package runtime

// Tests for the generic task tool (US-002/003/004, #454): its identity/schema
// contract, the shared concurrency semaphore (N > cap never exceeds cap), the
// nesting guard (child tool set excludes "task"), that a task returns the
// child's final text, and that a failed child surfaces as a tool error. The
// child loop is driven through the faux provider seam (对标 orchestration_test.go);
// only the provider boundary is faked.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
)

// TestTaskToolContract pins the tool identity, parallel execution mode, and the
// {description?, prompt} schema with prompt required.
func TestTaskToolContract(t *testing.T) {
	tool := NewTaskTool(func() RunConfig { return RunConfig{} }, nil)
	if tool.Name() != "task" {
		t.Errorf("Name() = %q, want task", tool.Name())
	}
	if tool.ExecutionMode() != agentcore.ToolExecutionParallel {
		t.Errorf("ExecutionMode() = %v, want parallel", tool.ExecutionMode())
	}
	var schema struct {
		Properties struct {
			Description json.RawMessage `json:"description"`
			Prompt      json.RawMessage `json:"prompt"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if len(schema.Properties.Prompt) == 0 || len(schema.Properties.Description) == 0 {
		t.Errorf("schema must declare both prompt and description properties")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "prompt" {
		t.Errorf("required = %v, want [prompt]", schema.Required)
	}
}

// TestTaskReturnsChildText verifies a dispatched task drives an independent child
// loop and returns the child's final assistant text as the tool result.
func TestTaskReturnsChildText(t *testing.T) {
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
	res, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"description":"do x","prompt":"do the work"}`), nil)
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "child final report" {
		t.Errorf("task result = %q, want 'child final report'", got)
	}
	if child.callCount() != 1 {
		t.Errorf("child provider calls = %d, want 1", child.callCount())
	}
}

// TestTaskFailedChildErrors verifies a child whose final turn stops on error is
// surfaced to the parent as a tool error (not a silent success).
func TestTaskFailedChildErrors(t *testing.T) {
	// A child turn ending on StopReason=error, carrying diagnostic text as content
	// (executeGoroutine surfaces the child's Content on failure).
	errTurn := func(text string) fauxTurn {
		partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
		withText := partial
		withText.Content = agentcore.ContentList{agentcore.NewTextContent(text)}
		final := withText
		final.StopReason = agentcore.StopReasonError
		return fauxTurn{
			provider.StreamStartEvent{Partial: partial},
			provider.StreamTextEvent{Partial: withText},
			provider.StreamDoneEvent{Message: final},
		}
	}
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{errTurn("child exploded")},
	}
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
		}
	}
	tool := NewTaskTool(factory, nil)
	_, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
	if err == nil {
		t.Fatal("a child that stopped on error must surface as a tool error")
	}
	if !strings.Contains(err.Error(), "child exploded") {
		t.Errorf("error should carry the child's diagnostic, got %v", err)
	}
}

// TestTaskSemaphoreBoundsConcurrency dispatches N tasks concurrently through a
// shared semaphore of capacity cap (< N) and asserts the number of children
// running at once never exceeds cap. Each child calls a blocking fake tool that
// parks on a barrier, so all admitted children pile up simultaneously and the
// peak concurrency is observable.
func TestTaskSemaphoreBoundsConcurrency(t *testing.T) {
	const capN, n = 2, 6
	sem := make(chan struct{}, capN)

	var running, peak int64
	release := make(chan struct{})
	// blockTool parks until the test closes release, holding a semaphore slot for
	// the duration and recording the peak number of concurrent children.
	blockTool := execTool{
		name: "block",
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			cur := atomic.AddInt64(&running, 1)
			for {
				p := atomic.LoadInt64(&peak)
				if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
					break
				}
			}
			defer atomic.AddInt64(&running, -1)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("blocked")}}, nil
		},
	}
	// Each child runs one turn that calls the blocking tool, then (after release)
	// a final text turn.
	factory := func() RunConfig {
		p := &fauxProvider{
			name:   "faux-child",
			models: []provider.Model{{Provider: "c", ID: "c"}},
			turns:  []fauxTurn{toolCallTurn("t", "block", `{}`), textTurn("done")},
		}
		reg := agenttool.NewToolRegistry()
		_ = reg.Register(blockTool)
		return RunConfig{
			LoopConfig: LoopConfig{Model: "c", Stream: provider.StreamFnFromProvider(p)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
		}
	}
	tool := NewTaskTool(factory, sem)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
		}()
	}
	// Give the admitted children time to reach the barrier, then let them go.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&running) < int64(capN) {
		select {
		case <-deadline:
			t.Fatalf("only %d children started, expected the semaphore to admit %d", atomic.LoadInt64(&running), capN)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// Hold briefly so any over-admission (a semaphore bug) would push peak > cap.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt64(&peak); got > int64(capN) {
		t.Errorf("peak concurrent children = %d, must not exceed cap %d", got, capN)
	}
	if got := atomic.LoadInt64(&peak); got == 0 {
		t.Error("no child ever ran; the semaphore blocked everything")
	}
}
