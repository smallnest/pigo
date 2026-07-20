package main

// Tests for headless session persistence and resume (session id in stream-json
// + --resume for headless runs). openHeadlessSession/persist are exercised
// directly against an isolated PIGO_HOME so a headless run's session round-trips
// without spawning a provider.

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func textUser(s string) agentcore.UserMessage {
	return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(s)}}
}

func textAssistant(s string) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent(s)}}
}

// TestOpenHeadlessSessionFresh verifies a fresh headless session gets a new id
// and empty prior messages, and that persist writes the run's messages so they
// can be resumed.
func TestOpenHeadlessSessionFresh(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())

	prior, hs, err := openHeadlessSession("", "faux-model", "faux", "sys prompt")
	if err != nil {
		t.Fatalf("openHeadlessSession fresh: %v", err)
	}
	if len(prior) != 0 {
		t.Errorf("fresh session must have no prior messages, got %d", len(prior))
	}
	if hs.header.ID == "" {
		t.Fatal("fresh session must have a non-empty id")
	}
	if hs.header.SystemPrompt != "sys prompt" {
		t.Errorf("header SystemPrompt = %q, want the passed prompt", hs.header.SystemPrompt)
	}

	// Simulate a completed run: prompt + assistant reply.
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{textUser("1+1=?"), textAssistant("2")}}
	if err := hs.persist(agentCtx); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// The session must now be loadable with both messages.
	_, msgs, err := hs.store.Load(hs.header.ID)
	if err != nil {
		t.Fatalf("Load persisted session: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted session has %d messages, want 2", len(msgs))
	}
}

// TestOpenHeadlessSessionResume verifies that resuming seeds the prior messages
// and that a subsequent run appends only the new tail as a branch, so the
// session grows rather than being rewritten.
func TestOpenHeadlessSessionResume(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())

	// First run: create and persist a session.
	_, hs1, err := openHeadlessSession("", "faux-model", "faux", "sys")
	if err != nil {
		t.Fatalf("first openHeadlessSession: %v", err)
	}
	ctx1 := &agentcore.AgentContext{Messages: agentcore.MessageList{textUser("first"), textAssistant("reply1")}}
	if err := hs1.persist(ctx1); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	sessID := hs1.header.ID

	// Second run: resume the session id.
	prior, hs2, err := openHeadlessSession(sessID, "faux-model", "faux", "sys")
	if err != nil {
		t.Fatalf("resume openHeadlessSession: %v", err)
	}
	if len(prior) != 2 {
		t.Fatalf("resume must seed %d prior messages, got %d", 2, len(prior))
	}
	if hs2.header.ID != sessID {
		t.Errorf("resumed session id = %q, want %q", hs2.header.ID, sessID)
	}
	if hs2.persisted != 2 {
		t.Errorf("resumed persisted cursor = %d, want 2", hs2.persisted)
	}

	// A second turn appends its new tail.
	ctx2 := &agentcore.AgentContext{Messages: append(prior, textUser("second"), textAssistant("reply2"))}
	if err := hs2.persist(ctx2); err != nil {
		t.Fatalf("second persist: %v", err)
	}
	_, msgs, err := hs2.store.Load(sessID)
	if err != nil {
		t.Fatalf("Load after second turn: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("session after two turns has %d messages, want 4", len(msgs))
	}
}

// TestHeadlessPersistNoop verifies persist is a no-op (no error, no growth) when
// the run produced nothing new past what was already persisted.
func TestHeadlessPersistNoop(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	_, hs, err := openHeadlessSession("", "m", "p", "s")
	if err != nil {
		t.Fatalf("openHeadlessSession: %v", err)
	}
	ctx := &agentcore.AgentContext{Messages: agentcore.MessageList{textUser("x"), textAssistant("y")}}
	if err := hs.persist(ctx); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	// Persisting again with no new messages must not error and must not duplicate.
	if err := hs.persist(ctx); err != nil {
		t.Fatalf("noop persist: %v", err)
	}
	_, msgs, err := hs.store.Load(hs.header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("noop persist changed message count to %d, want 2", len(msgs))
	}
}

// TestHeadlessPersistCompactionShrink verifies persist tolerates the context
// being rebuilt to fewer messages than were on disk before the run (mid-run
// compaction replaces agentCtx.Messages). The persisted cursor is clamped so
// the tail slice stays in bounds rather than panicking.
func TestHeadlessPersistCompactionShrink(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	_, hs, err := openHeadlessSession("", "m", "p", "s")
	if err != nil {
		t.Fatalf("openHeadlessSession: %v", err)
	}
	// Persist four messages, advancing the cursor to 4.
	ctx := &agentcore.AgentContext{Messages: agentcore.MessageList{textUser("a"), textAssistant("b"), textUser("c"), textAssistant("d")}}
	if err := hs.persist(ctx); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	if hs.persisted != 4 {
		t.Fatalf("cursor = %d, want 4", hs.persisted)
	}
	// Simulate compaction: the context is rebuilt to fewer messages than the
	// cursor. persist must not panic on the out-of-range slice.
	ctx.Messages = agentcore.MessageList{textAssistant("summary"), textUser("e")}
	if err := hs.persist(ctx); err != nil {
		t.Fatalf("persist after compaction shrink: %v", err)
	}
	if hs.persisted != 2 {
		t.Errorf("cursor after clamp = %d, want 2", hs.persisted)
	}
}
