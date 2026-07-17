// This file implements the todo tool (US-011, #127): a structured task list the
// model uses to plan and track multi-step work, with progress visible to the
// user. Unlike the file tools this one is stateful — the written list lives in a
// per-session TodoStore the tool holds, so a later write replaces the plan and
// the REPL can render the current progress after each update.
//
// pi itself has no such tool; this mirrors Claude Code's TodoWrite: the model
// submits the WHOLE list each call (not incremental edits), each item carries a
// content string and a status of pending | in_progress | completed.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TodoStatus is the lifecycle state of a single todo item.
type TodoStatus string

const (
	// TodoPending is a task not yet started.
	TodoPending TodoStatus = "pending"
	// TodoInProgress is the task currently being worked on.
	TodoInProgress TodoStatus = "in_progress"
	// TodoCompleted is a finished task.
	TodoCompleted TodoStatus = "completed"
)

// validTodoStatus reports whether s is one of the three accepted statuses.
func validTodoStatus(s TodoStatus) bool {
	switch s {
	case TodoPending, TodoInProgress, TodoCompleted:
		return true
	default:
		return false
	}
}

// TodoItem is one entry in the task list.
type TodoItem struct {
	// Content is the human-readable task description.
	Content string `json:"content"`
	// Status is the item's lifecycle state.
	Status TodoStatus `json:"status"`
}

// TodoStore holds the current task list for a session. It is safe for concurrent
// use so the tool (which may run in a batch) and the REPL renderer can touch it
// without racing. A single store is shared for a session's lifetime.
type TodoStore struct {
	mu    sync.RWMutex
	items []TodoItem
}

// NewTodoStore returns an empty store.
func NewTodoStore() *TodoStore { return &TodoStore{} }

// Set replaces the whole list with items (a copy, so the caller's slice can be
// reused).
func (s *TodoStore) Set(items []TodoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items[:0:0], items...)
}

// Snapshot returns a copy of the current list, safe to read without holding the
// lock.
func (s *TodoStore) Snapshot() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]TodoItem(nil), s.items...)
}

// TodoTool is the stateful todo-list tool. It writes the submitted list into
// Store, replacing any previous list, and returns a rendered progress view.
type TodoTool struct {
	// Store holds the session task list. Must be non-nil; NewTodoStore builds one.
	Store *TodoStore
}

// todoToolArgs is the decoded argument shape: the full task list to store.
type todoToolArgs struct {
	Todos []TodoItem `json:"todos"`
}

// Name implements AgentTool.
func (t *TodoTool) Name() string { return "todo" }

// Description implements AgentTool.
func (t *TodoTool) Description() string {
	return "Record and update a structured task list to plan and track multi-step " +
		"work. Submit the ENTIRE list every call; it replaces the previous list. " +
		"Each item has a content string and a status of pending, in_progress, or " +
		"completed. Keep exactly one item in_progress at a time and mark items " +
		"completed as soon as they are done."
}

// Schema implements AgentTool.
func (t *TodoTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "todos": {
      "type": "array",
      "description": "The full task list, replacing any previous list.",
      "items": {
        "type": "object",
        "properties": {
          "content": {"type": "string", "description": "Task description."},
          "status":  {"type": "string", "enum": ["pending", "in_progress", "completed"], "description": "Task lifecycle state."}
        },
        "required": ["content", "status"],
        "additionalProperties": false
      }
    }
  },
  "required": ["todos"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Updating the shared list mutates session
// state → sequential so a batch cannot interleave two list writes.
func (t *TodoTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool. It validates every item's status, stores the
// list, and returns the rendered progress as the result content. Invalid input
// degrades to an error result (matching the file tools) rather than a Go error.
func (t *TodoTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[todoToolArgs](args, "todo")
	if bad != nil {
		return *bad, nil
	}
	for i, it := range a.Todos {
		if strings.TrimSpace(it.Content) == "" {
			return errorResult(fmt.Sprintf("todo: item %d has empty content", i+1)), nil
		}
		if !validTodoStatus(it.Status) {
			return errorResult(fmt.Sprintf("todo: item %d has invalid status %q (want pending|in_progress|completed)", i+1, it.Status)), nil
		}
	}

	if t.Store == nil {
		t.Store = NewTodoStore()
	}
	t.Store.Set(a.Todos)

	rendered := RenderTodoList(a.Todos)
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(rendered)},
		Details: map[string]any{"todos": a.Todos},
	}, nil
}

// RenderTodoList renders items as a checkbox progress block, one line per task,
// with a trailing summary count. An empty list renders as a single "(no tasks)"
// line so an intentional clear is still visible. The marks are: [ ] pending,
// [~] in_progress, [x] completed.
func RenderTodoList(items []TodoItem) string {
	if len(items) == 0 {
		return "Todos: (no tasks)"
	}
	var b strings.Builder
	done := 0
	b.WriteString("Todos:")
	for _, it := range items {
		mark := " "
		switch it.Status {
		case TodoInProgress:
			mark = "~"
		case TodoCompleted:
			mark = "x"
			done++
		}
		fmt.Fprintf(&b, "\n  [%s] %s", mark, it.Content)
	}
	fmt.Fprintf(&b, "\n(%d/%d completed)", done, len(items))
	return b.String()
}
