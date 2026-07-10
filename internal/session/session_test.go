package session

// Tests for local JSONL session persistence and resume (US-024, #43). They
// cover the write→read round-trip, listing order, resume into an AgentContext,
// schema-version guarding, and append — driving the real filesystem via
// t.TempDir(), the standard Go pattern for behavior tests.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// writeFile writes content to path (test helper for hand-crafted fixtures).
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// sampleMessages returns a small multi-turn transcript: user prompt, assistant
// with a tool call, tool result, then a final assistant reply.
func sampleMessages() agentcore.MessageList {
	return agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("read main.go")}},
		agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			Content:    agentcore.ContentList{agentcore.NewTextContent("Reading it."), agentcore.NewToolCallContent("call-1", "read", []byte(`{"path":"main.go"}`))},
			StopReason: agentcore.StopReasonToolUse,
		},
		agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: "call-1",
			ToolName:   "read",
			Content:    agentcore.ContentList{agentcore.NewTextContent("package main")},
		},
		agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			Content:    agentcore.ContentList{agentcore.NewTextContent("It is package main.")},
			StopReason: agentcore.StopReasonEndTurn,
		},
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// TestSaveLoadRoundTrip is the core acceptance check: a saved session loads
// back with an identical header and message sequence.
func TestSaveLoadRoundTrip(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 10, 14, 25, 30, 0, time.UTC)
	header := SessionHeader{
		ID:           NewID(now),
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        "anthropic/claude-opus-4",
		Provider:     "anthropic",
		SystemPrompt: "You are pigo.",
	}
	msgs := sampleMessages()
	if err := s.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	gotHeader, gotMsgs, err := s.Load(header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotHeader.Version != SchemaVersion {
		t.Errorf("version = %d, want %d", gotHeader.Version, SchemaVersion)
	}
	if gotHeader.Model != header.Model || gotHeader.SystemPrompt != header.SystemPrompt {
		t.Errorf("header mismatch: %+v", gotHeader)
	}
	if len(gotMsgs) != len(msgs) {
		t.Fatalf("message count = %d, want %d", len(gotMsgs), len(msgs))
	}
	// Roles must round-trip in order.
	wantRoles := []string{agentcore.RoleUser, agentcore.RoleAssistant, agentcore.RoleToolResult, agentcore.RoleAssistant}
	for i, m := range gotMsgs {
		if m.Role() != wantRoles[i] {
			t.Errorf("message[%d] role = %q, want %q", i, m.Role(), wantRoles[i])
		}
	}
	// The assistant tool call must survive the round-trip.
	a, ok := gotMsgs[1].(agentcore.AssistantMessage)
	if !ok {
		t.Fatalf("message[1] is not AssistantMessage: %T", gotMsgs[1])
	}
	calls := a.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "read" {
		t.Errorf("tool calls = %+v, want one 'read'", calls)
	}
}

// TestSaveOverwrites verifies Save replaces an existing session file (same id)
// atomically rather than appending.
func TestSaveOverwrites(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	h := SessionHeader{ID: "fixed", CreatedAt: now, UpdatedAt: now}
	if err := s.Save(h, sampleMessages()); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	shorter := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}}}
	if err := s.Save(h, shorter); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	_, msgs, err := s.Load("fixed")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("after overwrite, message count = %d, want 1", len(msgs))
	}
}

// TestListSortedByUpdatedDesc verifies List returns sessions most-recent-first.
func TestListSortedByUpdatedDesc(t *testing.T) {
	s := newStore(t)
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"old", "mid", "new"} {
		h := SessionHeader{
			ID:        id,
			CreatedAt: base,
			UpdatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := s.Save(h, sampleMessages()); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}
	headers, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(headers) != 3 {
		t.Fatalf("List count = %d, want 3", len(headers))
	}
	wantOrder := []string{"new", "mid", "old"}
	for i, h := range headers {
		if h.ID != wantOrder[i] {
			t.Errorf("List[%d].ID = %q, want %q", i, h.ID, wantOrder[i])
		}
	}
}

// TestResumeReconstructsContext verifies Resume rebuilds an AgentContext whose
// system prompt and messages match the persisted session — the data the TUI
// replays on resume.
func TestResumeReconstructsContext(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	// A session whose last message is a tool result (not assistant), so it is
	// resumable per agentLoopContinue's precondition.
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("go")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent("c1", "read", []byte(`{}`))}, StopReason: agentcore.StopReasonToolUse},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "c1", ToolName: "read", Content: agentcore.ContentList{agentcore.NewTextContent("data")}},
	}
	h := SessionHeader{ID: "resumable", CreatedAt: now, UpdatedAt: now, SystemPrompt: "sys"}
	if err := s.Save(h, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ctx, header, err := s.Resume("resumable")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if ctx.SystemPrompt != "sys" {
		t.Errorf("resumed system prompt = %q, want sys", ctx.SystemPrompt)
	}
	if len(ctx.Messages) != 3 {
		t.Errorf("resumed message count = %d, want 3", len(ctx.Messages))
	}
	if header.ID != "resumable" {
		t.Errorf("resumed header ID = %q", header.ID)
	}
}

// TestResumeRejectsTrailingAssistant verifies Resume errors when the last
// message is an assistant message (nothing to continue from).
func TestResumeRejectsTrailingAssistant(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a")}, StopReason: agentcore.StopReasonEndTurn},
	}
	if err := s.Save(SessionHeader{ID: "done", CreatedAt: now, UpdatedAt: now}, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, _, err := s.Resume("done"); err == nil {
		t.Error("Resume must reject a session whose last message is an assistant message")
	}
}

// TestLoadRejectsNewerSchema verifies a file whose version is newer than the
// binary supports is rejected rather than silently misread.
func TestLoadRejectsNewerSchema(t *testing.T) {
	s := newStore(t)
	// Hand-write a session file with a future version.
	future := `{"version":9999,"id":"future","createdAt":"2026-07-10T00:00:00Z","updatedAt":"2026-07-10T00:00:00Z"}` + "\n"
	path := filepath.Join(s.Dir(), FileName("future"))
	if err := writeFile(path, future); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, _, err := s.Load("future"); err == nil {
		t.Error("Load must reject a newer schema version")
	}
}

// TestAppendGrowsSession verifies Append adds messages and bumps UpdatedAt.
func TestAppendGrowsSession(t *testing.T) {
	s := newStore(t)
	created := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	h := SessionHeader{ID: "grow", CreatedAt: created, UpdatedAt: created}
	initial := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("first")}}}
	if err := s.Save(h, initial); err != nil {
		t.Fatalf("Save: %v", err)
	}
	later := created.Add(time.Hour)
	more := agentcore.MessageList{agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("reply")}, StopReason: agentcore.StopReasonEndTurn}}
	if err := s.Append("grow", later, more); err != nil {
		t.Fatalf("Append: %v", err)
	}
	gotHeader, gotMsgs, err := s.Load("grow")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(gotMsgs) != 2 {
		t.Errorf("after append, message count = %d, want 2", len(gotMsgs))
	}
	if !gotHeader.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v, want %v", gotHeader.UpdatedAt, later)
	}
}
