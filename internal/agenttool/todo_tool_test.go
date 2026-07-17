// Tests for the todo tool (US-011, #127): registration/validation, status
// transitions across successive writes, and the rendered progress view.
package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// execTodo runs the tool with the given JSON args and returns the result.
func execTodo(t *testing.T, tool *TodoTool, args string) agentcore.AgentToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), "call-1", json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return res
}

// TestTodoToolRegisters checks the tool registers cleanly (valid schema) and is
// retrievable — the "注册进 agenttool registry" acceptance criterion.
func TestTodoToolRegisters(t *testing.T) {
	reg := NewToolRegistry()
	tool := &TodoTool{Store: NewTodoStore()}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := reg.Get("todo")
	if !ok {
		t.Fatal("todo tool not found after Register")
	}
	if got.Name() != "todo" {
		t.Errorf("Name = %q, want todo", got.Name())
	}
}

// TestTodoToolStoresList checks a write lands in the session store.
func TestTodoToolStoresList(t *testing.T) {
	store := NewTodoStore()
	tool := &TodoTool{Store: store}
	execTodo(t, tool, `{"todos":[
		{"content":"first","status":"in_progress"},
		{"content":"second","status":"pending"}
	]}`)

	items := store.Snapshot()
	if len(items) != 2 {
		t.Fatalf("Snapshot len = %d, want 2", len(items))
	}
	if items[0].Content != "first" || items[0].Status != TodoInProgress {
		t.Errorf("item 0 = %+v", items[0])
	}
	if items[1].Status != TodoPending {
		t.Errorf("item 1 status = %q, want pending", items[1].Status)
	}
}

// TestTodoToolStatusTransition checks a later write replaces the previous list,
// reflecting a status flow pending → in_progress → completed.
func TestTodoToolStatusTransition(t *testing.T) {
	store := NewTodoStore()
	tool := &TodoTool{Store: store}

	execTodo(t, tool, `{"todos":[{"content":"build feature","status":"pending"}]}`)
	if s := store.Snapshot()[0].Status; s != TodoPending {
		t.Fatalf("after write 1 status = %q, want pending", s)
	}

	execTodo(t, tool, `{"todos":[{"content":"build feature","status":"in_progress"}]}`)
	if s := store.Snapshot()[0].Status; s != TodoInProgress {
		t.Fatalf("after write 2 status = %q, want in_progress", s)
	}

	execTodo(t, tool, `{"todos":[{"content":"build feature","status":"completed"}]}`)
	items := store.Snapshot()
	if len(items) != 1 {
		t.Fatalf("after write 3 len = %d, want 1", len(items))
	}
	if items[0].Status != TodoCompleted {
		t.Errorf("after write 3 status = %q, want completed", items[0].Status)
	}
}

// TestTodoToolRejectsInvalidStatus checks an unknown status degrades to an error
// result and does not mutate the store.
func TestTodoToolRejectsInvalidStatus(t *testing.T) {
	store := NewTodoStore()
	tool := &TodoTool{Store: store}
	res := execTodo(t, tool, `{"todos":[{"content":"x","status":"done"}]}`)
	if !strings.Contains(agentcore.ContentToText(res.Content), "invalid status") {
		t.Errorf("expected invalid-status error, got %q", agentcore.ContentToText(res.Content))
	}
	if len(store.Snapshot()) != 0 {
		t.Error("store mutated despite invalid input")
	}
}

// TestTodoToolRejectsEmptyContent checks a blank content is rejected.
func TestTodoToolRejectsEmptyContent(t *testing.T) {
	tool := &TodoTool{Store: NewTodoStore()}
	res := execTodo(t, tool, `{"todos":[{"content":"  ","status":"pending"}]}`)
	if !strings.Contains(agentcore.ContentToText(res.Content), "empty content") {
		t.Errorf("expected empty-content error, got %q", agentcore.ContentToText(res.Content))
	}
}

// TestRenderTodoList checks the rendered progress view: marks per status and a
// completion count.
func TestRenderTodoList(t *testing.T) {
	out := RenderTodoList([]TodoItem{
		{Content: "alpha", Status: TodoCompleted},
		{Content: "beta", Status: TodoInProgress},
		{Content: "gamma", Status: TodoPending},
	})
	for _, want := range []string{"[x] alpha", "[~] beta", "[ ] gamma", "(1/3 completed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

// TestRenderTodoListEmpty checks an empty list renders visibly (an intentional
// clear should still show).
func TestRenderTodoListEmpty(t *testing.T) {
	if out := RenderTodoList(nil); !strings.Contains(out, "no tasks") {
		t.Errorf("empty render = %q, want a (no tasks) marker", out)
	}
}

// TestTodoToolResultRenders checks Execute returns the rendered list as content
// so the REPL has something to display.
func TestTodoToolResultRenders(t *testing.T) {
	tool := &TodoTool{Store: NewTodoStore()}
	res := execTodo(t, tool, `{"todos":[{"content":"do it","status":"completed"}]}`)
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, "[x] do it") || !strings.Contains(text, "(1/1 completed)") {
		t.Errorf("result content = %q", text)
	}
}
