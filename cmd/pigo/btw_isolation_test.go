package main

// Isolation / zero-pollution tests for /btw (#284, PRD Success Metrics). These
// lock the feature's most important correctness guarantee: a side thread's
// question and answer NEVER touch the main conversation and NEVER hit disk. The
// tests drive the whole runREPL loop with the fake replProvider and assert, in
// one place, every observable that a leak would perturb: the main context's
// message count AND content, the session store on disk, and the persistence
// bookkeeping (deps.persisted / deps.curLeaf / deps.header.UpdatedAt).

import (
	"bytes"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// snapshotMessages returns the flattened text of every message so a test can
// assert the main context is byte-for-byte unchanged, not merely the same
// length (a leak could replace a message without changing the count). Content
// is a field on the concrete message types, not on the Message interface, so we
// type-switch on the two kinds /btw could ever append.
func snapshotMessages(msgs agentcore.MessageList) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		var text string
		switch v := m.(type) {
		case agentcore.UserMessage:
			text = agentcore.ContentToText(v.Content)
		case agentcore.AssistantMessage:
			text = agentcore.ContentToText(v.Content)
		}
		out[i] = m.Role() + ":" + text
	}
	return out
}

// seedMainContext appends a real user+assistant exchange to the main context so
// the isolation tests start from a non-empty conversation — proving /btw leaves
// existing history untouched, not just that it avoids growing an empty slice.
func seedMainContext(deps *replDeps) {
	deps.agentCtx.Messages = append(deps.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("main question")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("main answer")}},
	)
}

// TestBtwMainContextZeroGrowth verifies one /btw Q&A leaves the main context's
// message count AND content exactly as before (AC-1).
func TestBtwMainContextZeroGrowth(t *testing.T) {
	p := &replProvider{reply: "side answer"}
	deps, _ := newTestDeps(t, p)
	seedMainContext(&deps)
	before := snapshotMessages(deps.agentCtx.Messages)

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/btw a quick question?\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("expected exactly 1 side run, got %d", p.calls)
	}
	after := snapshotMessages(deps.agentCtx.Messages)
	if len(after) != len(before) {
		t.Fatalf("main context grew: %d → %d messages", len(before), len(after))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("main context message %d changed:\n before %q\n after  %q", i, before[i], after[i])
		}
	}
}

// TestBtwNoStoreWrites verifies /btw writes nothing to the session store: no
// entries are appended for the session on disk (AC-2).
func TestBtwNoStoreWrites(t *testing.T) {
	p := &replProvider{reply: "answer"}
	deps, store := newTestDeps(t, p)
	seedMainContext(&deps)

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/btw does this persist?\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	// The main loop persists only on a real turn; /btw must trigger none. Since
	// the seeded messages were never run through streamRun, the store should hold
	// no entries for this session at all.
	if _, entries, err := store.LoadEntries(deps.header.ID); err == nil && len(entries) != 0 {
		t.Fatalf("/btw must not write to the store, got %d entries", len(entries))
	}
}

// TestBtwFollowUpsLeaveContextUnchanged verifies that ≥3 follow-ups in the same
// side thread still leave the main context's count and content unchanged (AC-3).
func TestBtwFollowUpsLeaveContextUnchanged(t *testing.T) {
	p := &replProvider{reply: "ok"}
	deps, _ := newTestDeps(t, p)
	seedMainContext(&deps)
	before := snapshotMessages(deps.agentCtx.Messages)

	var out bytes.Buffer
	// One /btw plus three bare follow-ups, then leave the thread and exit.
	in := strings.NewReader("/btw first?\nsecond?\nthird?\nfourth?\n/exit\n/exit\n")
	if err := runREPL(in, &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 4 {
		t.Fatalf("expected 4 side runs (1 + 3 follow-ups), got %d", p.calls)
	}
	after := snapshotMessages(deps.agentCtx.Messages)
	if len(after) != len(before) {
		t.Fatalf("main context grew across follow-ups: %d → %d", len(before), len(after))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("follow-ups changed main message %d: %q → %q", i, before[i], after[i])
		}
	}
}

// TestBtwPersistenceBookkeepingUnchanged verifies /btw does not advance the
// persistence bookkeeping: deps.persisted, deps.curLeaf and deps.header.UpdatedAt
// are all identical before and after (AC-4).
func TestBtwPersistenceBookkeepingUnchanged(t *testing.T) {
	p := &replProvider{reply: "x"}
	deps, _ := newTestDeps(t, p)
	seedMainContext(&deps)
	// Give the bookkeeping non-zero starting values so the test would catch a
	// reset-to-zero as well as an increment.
	deps.persisted = 2
	deps.curLeaf = "leaf-abc"
	beforeUpdated := deps.header.UpdatedAt

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/btw hi\nmore?\n/exit\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if deps.persisted != 2 {
		t.Errorf("deps.persisted changed: 2 → %d", deps.persisted)
	}
	if deps.curLeaf != "leaf-abc" {
		t.Errorf("deps.curLeaf changed: %q → %q", "leaf-abc", deps.curLeaf)
	}
	if !deps.header.UpdatedAt.Equal(beforeUpdated) {
		t.Errorf("deps.header.UpdatedAt changed: %v → %v", beforeUpdated, deps.header.UpdatedAt)
	}
}
