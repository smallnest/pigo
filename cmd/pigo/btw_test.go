package main

// Tests for the /btw side-thread command (#279): a side question must run an
// agent stream but MUST NOT mutate or persist the main conversation. These
// drive the whole runREPL loop with the fake replProvider, then assert the main
// context and persistence state are unchanged.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestBtwDoesNotPolluteMainContext verifies that "/btw <q>" launches a run
// (provider called) yet appends nothing to deps.agentCtx.Messages.
func TestBtwDoesNotPolluteMainContext(t *testing.T) {
	p := &replProvider{reply: "side answer"}
	deps, _ := newTestDeps(t, p)

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/btw why pointers?\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("expected side question to launch exactly 1 run, got %d", p.calls)
	}
	if len(deps.agentCtx.Messages) != 0 {
		t.Fatalf("main context must be untouched by /btw, got %d messages", len(deps.agentCtx.Messages))
	}
	if !strings.Contains(out.String(), btwHeader) {
		t.Errorf("expected side-thread header %q in output", btwHeader)
	}
	if !strings.Contains(out.String(), "side answer") {
		t.Errorf("expected the side answer to be printed, got: %q", out.String())
	}
}

// TestBtwDoesNotPersist verifies /btw writes nothing to disk: deps.persisted and
// deps.curLeaf are unchanged, and no session entries were appended.
func TestBtwDoesNotPersist(t *testing.T) {
	p := &replProvider{reply: "answer"}
	deps, store := newTestDeps(t, p)

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/btw quick q\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if deps.persisted != 0 {
		t.Fatalf("deps.persisted must stay 0 after /btw, got %d", deps.persisted)
	}
	if deps.curLeaf != "" {
		t.Fatalf("deps.curLeaf must stay empty after /btw, got %q", deps.curLeaf)
	}
	if _, entries, err := store.LoadEntries(deps.header.ID); err == nil && len(entries) != 0 {
		t.Fatalf("no session entries should be persisted by /btw, got %d", len(entries))
	}
}

// TestBtwBareUsage verifies bare "/btw" with no prior side thread does not
// launch a run and prints usage guidance.
func TestBtwBareUsage(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/btw\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 0 {
		t.Fatalf("bare /btw must not launch a run, got %d calls", p.calls)
	}
	if !strings.Contains(out.String(), "usage: /btw") {
		t.Errorf("expected usage hint for bare /btw, got: %q", out.String())
	}
}

// TestBtwBareReopensLastThread verifies that after a side thread exists, a bare
// "/btw" reopens it, replays the prior side Q&A, and lets the user keep asking
// in the SAME thread. The main context stays untouched throughout.
func TestBtwBareReopensLastThread(t *testing.T) {
	p := &replProvider{reply: "side answer"}
	deps, _ := newTestDeps(t, p)

	var out bytes.Buffer
	// Open a side thread and ask once, leave it, then bare /btw reopens it and
	// asks a follow-up, then leave again and exit the REPL.
	in := strings.NewReader("/btw first question?\n/exit\n/btw\nsecond question?\n/exit\n/exit\n")
	if err := runREPL(in, &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 side runs (1 initial + 1 after reopen), got %d", p.calls)
	}
	if len(deps.agentCtx.Messages) != 0 {
		t.Fatalf("main context must stay untouched, got %d messages", len(deps.agentCtx.Messages))
	}
	s := out.String()
	// The reopen must not print the bare-/btw usage hint (a thread existed).
	if strings.Contains(s, "usage: /btw") {
		t.Errorf("bare /btw with an existing thread must not print usage, got: %q", s)
	}
	// The replay must echo the earlier question.
	if !strings.Contains(s, "first question?") {
		t.Errorf("reopen should replay the earlier side question, got: %q", s)
	}
}

// TestBtwFollowUpsShareThread verifies that after "/btw <q>" the user can ask
// follow-ups at the btw prompt (without retyping /btw), each launching a run,
// and that none of them pollute the main context. "/exit" leaves the thread.
func TestBtwFollowUpsShareThread(t *testing.T) {
	p := &replProvider{reply: "ok"}
	deps, _ := newTestDeps(t, p)

	var out bytes.Buffer
	// First /btw asks once; then two bare follow-ups; then /exit leaves the side
	// thread; then /exit ends the REPL.
	in := strings.NewReader("/btw first?\nsecond?\nthird?\n/exit\n/exit\n")
	if err := runREPL(in, &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 3 {
		t.Fatalf("expected 3 side runs (1 initial + 2 follow-ups), got %d", p.calls)
	}
	if len(deps.agentCtx.Messages) != 0 {
		t.Fatalf("follow-ups must not pollute main context, got %d messages", len(deps.agentCtx.Messages))
	}
	if !strings.Contains(out.String(), "left side thread") {
		t.Errorf("expected 'left side thread' on /exit from the side thread")
	}
}

// TestBtwFollowUpLoopAccumulates verifies the side context grows across
// follow-ups so a later question sees the earlier Q&A.
func TestBtwFollowUpLoopAccumulates(t *testing.T) {
	side := &agentcore.AgentContext{}
	deps, _ := newTestDeps(t, &replProvider{reply: "a"})
	setCancel := func(context.CancelFunc) {}
	settings := resolveBtwSettings(&bytes.Buffer{}, &deps)
	askSide(setCancel, &bytes.Buffer{}, &deps, side, settings, "q1")
	n1 := len(side.Messages)
	askSide(setCancel, &bytes.Buffer{}, &deps, side, settings, "q2")
	if len(side.Messages) <= n1 {
		t.Fatalf("side context should accumulate across follow-ups: %d then %d", n1, len(side.Messages))
	}
}

// appending to the side thread cannot reach the main slice.
func TestNewSideContextIsolated(t *testing.T) {
	main := &agentcore.AgentContext{
		SystemPrompt: "sys",
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		},
	}
	side := newSideContext(main)
	if side.SystemPrompt != "sys" {
		t.Errorf("side thread should inherit the system prompt")
	}
	if len(side.Messages) != 1 {
		t.Fatalf("side thread should be seeded with the main messages, got %d", len(side.Messages))
	}
	side.Messages = append(side.Messages, agentcore.UserMessage{RoleField: agentcore.RoleUser})
	if len(main.Messages) != 1 {
		t.Fatalf("appending to the side thread must not grow the main context, got %d", len(main.Messages))
	}
}
