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

// TestLoadV1FileStillReadable verifies the v2 schema bump is backward-compatible:
// an old v1 session file (no compaction lines) loads without error.
func TestLoadV1FileStillReadable(t *testing.T) {
	s := newStore(t)
	v1 := `{"version":1,"id":"old","createdAt":"2026-07-10T00:00:00Z","updatedAt":"2026-07-10T00:00:00Z"}` + "\n" +
		`{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":0}` + "\n"
	path := filepath.Join(s.Dir(), FileName("old"))
	if err := writeFile(path, v1); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	h, msgs, err := s.Load("old")
	if err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	if h.Version != 1 {
		t.Fatalf("version: got %d, want 1", h.Version)
	}
	if len(msgs) != 1 || msgs[0].Role() != agentcore.RoleUser {
		t.Fatalf("messages: got %+v", msgs)
	}
}

// TestSaveWritesV3TreeEntries verifies Save persists each message as a wrapped
// v3 entry: the file header is version 3, every entry carries a non-empty id,
// the first entry is a root (empty parentId), and each subsequent entry's
// parentId chains to the previous entry's id — a linear tree.
func TestSaveWritesV3TreeEntries(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	h, entries, err := s.LoadEntries(header.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if h.Version != 3 {
		t.Fatalf("version = %d, want 3", h.Version)
	}
	if len(entries) != 4 {
		t.Fatalf("entry count = %d, want 4", len(entries))
	}
	if entries[0].ParentID != "" {
		t.Errorf("root entry parentId = %q, want empty", entries[0].ParentID)
	}
	seen := map[string]bool{}
	for i, e := range entries {
		if e.ID == "" {
			t.Errorf("entry[%d] has empty id", i)
		}
		if seen[e.ID] {
			t.Errorf("entry[%d] id %q is duplicated", i, e.ID)
		}
		seen[e.ID] = true
		if i > 0 && e.ParentID != entries[i-1].ID {
			t.Errorf("entry[%d] parentId = %q, want %q (previous entry)", i, e.ParentID, entries[i-1].ID)
		}
	}
}

// TestPathToLeaf verifies PathToLeaf walks the parentId chain from a leaf back
// to the root and returns the entries in root→leaf order.
func TestPathToLeaf(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, entries, err := s.LoadEntries(header.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	leaf := entries[len(entries)-1]
	path := PathToLeaf(entries, leaf.ID)
	if len(path) != len(entries) {
		t.Fatalf("path length = %d, want %d (linear session)", len(path), len(entries))
	}
	for i := range entries {
		if path[i].ID != entries[i].ID {
			t.Errorf("path[%d].ID = %q, want %q", i, path[i].ID, entries[i].ID)
		}
	}
	// A mid-chain leaf yields only its ancestors + itself.
	mid := PathToLeaf(entries, entries[1].ID)
	if len(mid) != 2 || mid[0].ID != entries[0].ID || mid[1].ID != entries[1].ID {
		t.Errorf("PathToLeaf(entries[1]) = %+v, want [root, entries[1]]", mid)
	}
	// Unknown / empty leaf ids yield nil.
	if got := PathToLeaf(entries, "nope"); got != nil {
		t.Errorf("PathToLeaf(unknown) = %+v, want nil", got)
	}
	if got := PathToLeaf(entries, ""); got != nil {
		t.Errorf("PathToLeaf(empty) = %+v, want nil", got)
	}
}

// TestLoadV2FileMigratesToEntries verifies a v2 file (bare message lines, no
// id/parentId) still loads and resumes: readSession back-fills a synthesized id
// per line and chains parentId to the previous entry, so the migrated entries
// form a linear tree while the flat Load view is unchanged.
func TestLoadV2FileMigratesToEntries(t *testing.T) {
	s := newStore(t)
	v2 := `{"version":2,"id":"legacy","createdAt":"2026-07-10T00:00:00Z","updatedAt":"2026-07-10T00:00:00Z"}` + "\n" +
		`{"role":"user","content":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","content":[{"type":"text","text":"hello"}],"stopReason":"end_turn"}` + "\n"
	path := filepath.Join(s.Dir(), FileName("legacy"))
	if err := writeFile(path, v2); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Flat Load view is unchanged by the migration.
	h, msgs, err := s.Load("legacy")
	if err != nil {
		t.Fatalf("Load v2: %v", err)
	}
	if h.Version != 2 {
		t.Fatalf("version = %d, want 2", h.Version)
	}
	if len(msgs) != 2 || msgs[0].Role() != agentcore.RoleUser || msgs[1].Role() != agentcore.RoleAssistant {
		t.Fatalf("messages: got %+v", msgs)
	}
	// Entry view is back-filled into a linear tree.
	_, entries, err := s.LoadEntries("legacy")
	if err != nil {
		t.Fatalf("LoadEntries v2: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if entries[0].ID == "" || entries[0].ParentID != "" {
		t.Errorf("root entry = %+v, want non-empty id and empty parentId", entries[0])
	}
	if entries[1].ParentID != entries[0].ID {
		t.Errorf("entry[1].parentId = %q, want %q", entries[1].ParentID, entries[0].ID)
	}
	// The migrated entries reconstruct the full conversation via PathToLeaf.
	path2 := PathToLeaf(entries, entries[1].ID)
	if len(path2) != 2 {
		t.Errorf("PathToLeaf on migrated v2 = %d entries, want 2", len(path2))
	}
}

// TestAppendPreservesChain verifies Append grows the linear tree: after
// appending, the file still loads with a valid root and an unbroken parentId
// chain across the combined message set.
func TestAppendPreservesChain(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	h := SessionHeader{ID: "grow", CreatedAt: now, UpdatedAt: now}
	if err := s.Save(h, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	extra := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("more")}}}
	if err := s.Append("grow", now.Add(time.Minute), extra); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_, entries, err := s.LoadEntries("grow")
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("entry count = %d, want 5", len(entries))
	}
	if entries[0].ParentID != "" {
		t.Errorf("root parentId = %q, want empty", entries[0].ParentID)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ParentID != entries[i-1].ID {
			t.Errorf("entry[%d] parentId = %q, want %q", i, entries[i].ParentID, entries[i-1].ID)
		}
	}
	if path := PathToLeaf(entries, entries[4].ID); len(path) != 5 {
		t.Errorf("PathToLeaf after append = %d, want 5", len(path))
	}
}

// through the JSONL store as a first-class message line under schema v2.
func TestSaveLoadCompactionEntry(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	msgs := agentcore.MessageList{
		agentcore.CompactionMessage{
			RoleField:    agentcore.RoleCompaction,
			Summary:      "## Goal\nship #119",
			TokensBefore: 12345,
			Details:      []byte(`{"readFiles":["a.go"],"modifiedFiles":["b.go"]}`),
			Timestamp:    now.UnixMilli(),
		},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("continue")}},
	}
	if err := s.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, back, err := s.Load(header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != SchemaVersion {
		t.Fatalf("version: got %d, want %d", loaded.Version, SchemaVersion)
	}
	if len(back) != 2 || back[0].Role() != agentcore.RoleCompaction {
		t.Fatalf("messages: got %+v", back)
	}
	cm, ok := back[0].(agentcore.CompactionMessage)
	if !ok {
		t.Fatalf("first message is not a CompactionMessage: %T", back[0])
	}
	if cm.Summary != "## Goal\nship #119" || cm.TokensBefore != 12345 {
		t.Fatalf("compaction fields: %+v", cm)
	}
}
