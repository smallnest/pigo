package main

// Tests for the line-based REPL (#106): slash-command dispatch (action prints
// message and does NOT run; prompt runs; unknown command errors and does NOT
// run; /exit and EOF exit cleanly), multi-turn history accumulation, and
// streaming assistant text. The REPL is driven with a fake provider so no
// network is involved — the whole read → run → stream-print loop runs over an
// in-memory input reader and output buffer.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
)

// replProvider is a minimal Provider that streams one scripted text turn per
// StreamCompletion call and records how many times it was called, so a test can
// assert whether a run was launched.
type replProvider struct {
	reply string
	calls int
}

func (p *replProvider) Name() string { return "faux" }
func (p *replProvider) Models() []provider.Model {
	return []provider.Model{{Provider: "faux", ID: "faux"}}
}

func (p *replProvider) StreamCompletion(ctx context.Context, req provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	p.calls++
	partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	withText := partial
	withText.Content = agentcore.ContentList{agentcore.NewTextContent(p.reply)}
	final := withText
	final.StopReason = agentcore.StopReasonEndTurn
	s := provider.NewAssistantMessageEventStream(0)
	go func() {
		_ = s.Emit(ctx, provider.StreamStartEvent{Partial: partial})
		_ = s.Emit(ctx, provider.StreamTextEvent{Partial: withText})
		_ = s.Emit(ctx, provider.StreamDoneEvent{Message: final})
		s.Close()
	}()
	return s, nil
}

// newTestDeps builds replDeps wired to the fake provider and a temp session
// store, with a registry carrying one action command and one prompt command so
// slash dispatch can be exercised. actionRuns/promptResolved report whether each
// command fired.
func newTestDeps(t *testing.T, p *replProvider) (replDeps, *session.Store) {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	live := &liveRunConfig{model: "faux", providerName: "faux", provider: p}
	reg := runtime.NewSlashRegistry()
	reg.AddBuiltin(runtime.SlashCommand{
		Name:   "ping",
		Action: func(string) string { return "pong" },
	})
	reg.AddUser(runtime.SlashCommand{
		Name:   "echo",
		Expand: func(args string) string { return "expanded: " + args },
	})
	deps := replDeps{
		store:    store,
		header:   session.SessionHeader{ID: session.NewID(time.Now().UTC()), Model: "faux", Provider: "faux"},
		agentCtx: &agentcore.AgentContext{},
		live:     live,
		reg:      agenttool.NewToolRegistry(),
		slash:    reg,
		creds:    provider.NewCredentialStore(nil),
	}
	return deps, store
}

// TestREPLExitCommand verifies /exit ends the loop cleanly with no error and no
// agent run.
func TestREPLExitCommand(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL returned error: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("/exit must not launch a run, got %d calls", p.calls)
	}
}

// TestREPLQuitCommand verifies /quit is an alias for /exit.
func TestREPLQuitCommand(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/quit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL returned error: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("/quit must not launch a run, got %d calls", p.calls)
	}
}

// TestREPLEOFExits verifies EOF (no /exit) ends the loop cleanly.
func TestREPLEOFExits(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	var out bytes.Buffer
	// Empty input → immediate EOF.
	if err := runREPL(strings.NewReader(""), &out, deps); err != nil {
		t.Fatalf("EOF should exit cleanly, got: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("EOF with no input must not run, got %d calls", p.calls)
	}
}

// TestREPLEmptyLineIgnored verifies blank lines are skipped without running.
func TestREPLEmptyLineIgnored(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("\n   \n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("blank lines must not run, got %d calls", p.calls)
	}
}

// TestREPLActionCommandNoRun verifies an action slash command prints its message
// and does NOT launch an agent run.
func TestREPLActionCommandNoRun(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/ping\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("action command must not run, got %d calls", p.calls)
	}
	if !strings.Contains(out.String(), "pong") {
		t.Errorf("action command message not printed, out=%q", out.String())
	}
}

// TestREPLPromptCommandRuns verifies a prompt slash command expands and launches
// a run.
func TestREPLPromptCommandRuns(t *testing.T) {
	p := &replProvider{reply: "ack"}
	deps, _ := newTestDeps(t, p)
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/echo hello\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("prompt command should launch exactly 1 run, got %d", p.calls)
	}
	// The expanded prompt must have been appended as the user message.
	if len(deps.agentCtx.Messages) == 0 {
		t.Fatal("expected messages in context after run")
	}
	first, ok := deps.agentCtx.Messages[0].(agentcore.UserMessage)
	if !ok || agentcore.ContentToText(first.Content) != "expanded: hello" {
		t.Errorf("first message = %T %q, want expanded prompt", deps.agentCtx.Messages[0], agentcore.ContentToText(first.Content))
	}
}

// TestREPLUnknownCommandNoRun verifies an unknown slash command prints an error
// and does NOT run or crash.
func TestREPLUnknownCommandNoRun(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/nope\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("unknown command must not run, got %d calls", p.calls)
	}
	if !strings.Contains(strings.ToLower(out.String()), "unknown") {
		t.Errorf("expected an unknown-command error line, out=%q", out.String())
	}
}

// TestREPLModelSwitchTakesEffect verifies the /model action command switches the
// live model mid-session (via registerLiveCommands + resolveProvider) without
// launching a run, and that the switch is reflected in live for the next turn.
func TestREPLModelSwitchTakesEffect(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	// Register the real live action commands (/model, /models, /help) against the
	// same live config the REPL runs on, so /model mutates it.
	registerLiveCommands(deps.slash, deps.live)

	var out bytes.Buffer
	// /model with no arg reports the current model; /model <id> switches to an
	// Ollama preset (no API key required); /exit ends the loop.
	in := strings.NewReader("/model\n/model ollama/llama3.3\n/exit\n")
	if err := runREPL(in, &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("/model actions must not launch a run, got %d calls", p.calls)
	}
	if deps.live.model != "ollama/llama3.3" || deps.live.providerName != "ollama" {
		t.Errorf("live not switched: model=%q provider=%q", deps.live.model, deps.live.providerName)
	}
	s := out.String()
	if !strings.Contains(s, "faux") {
		t.Errorf("/model (no arg) should report the current model, out=%q", s)
	}
	if !strings.Contains(s, "ollama/llama3.3") {
		t.Errorf("/model switch should confirm the new model, out=%q", s)
	}
}

// TestREPLPersistsModelIntoHeader verifies that after a run the session header
// records the live model/provider (US-006: a /model switch is persisted with the
// session), by reloading the saved session and inspecting its header.
func TestREPLPersistsModelIntoHeader(t *testing.T) {
	p := &replProvider{reply: "ok"}
	deps, store := newTestDeps(t, p)
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("hello\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	headers, err := store.List()
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(headers) != 1 {
		t.Fatalf("expected 1 saved session, got %d", len(headers))
	}
	if headers[0].Model != "faux" || headers[0].Provider != "faux" {
		t.Errorf("header model/provider = %q/%q, want live faux/faux", headers[0].Model, headers[0].Provider)
	}
}

// TestReplayTranscriptRendersRoles verifies a resumed session's prior messages
// are echoed by role (user / assistant / tool result) before the first new
// prompt (US-006 acceptance: resumed conversation is replayed).
func TestReplayTranscriptRendersRoles(t *testing.T) {
	msgs := []agentcore.AgentMessage{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("what is 2+2")}},
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content:   agentcore.ContentList{agentcore.NewTextContent("Let me compute."), agentcore.NewToolCallContent("c1", "calc", []byte(`{"expr":"2+2"}`))},
		},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "c1", ToolName: "calc", Content: agentcore.ContentList{agentcore.NewTextContent("4")}},
	}
	var out bytes.Buffer
	replayTranscript(&out, msgs)
	s := out.String()
	for _, want := range []string{"> what is 2+2", "Let me compute.", `→ tool: calc {"expr":"2+2"}`, "← result: 4"} {
		if !strings.Contains(s, want) {
			t.Errorf("replay missing %q, out=%q", want, s)
		}
	}
}

// TestREPLStreamsAndAccumulatesHistory verifies a plain prompt runs, its reply
// is printed, and history accumulates across two turns in the shared context.
func TestREPLStreamsAndAccumulatesHistory(t *testing.T) {
	p := &replProvider{reply: "the answer"}
	deps, store := newTestDeps(t, p)
	var out bytes.Buffer
	if err := runREPL(strings.NewReader("first question\nsecond question\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 2 {
		t.Fatalf("two prompts should launch 2 runs, got %d", p.calls)
	}
	// The streamed reply text must appear in the output.
	if !strings.Contains(out.String(), "the answer") {
		t.Errorf("assistant reply not streamed to output, out=%q", out.String())
	}
	// History: user(first) + assistant + user(second) + assistant = 4 messages.
	if len(deps.agentCtx.Messages) != 4 {
		t.Fatalf("expected 4 accumulated messages, got %d", len(deps.agentCtx.Messages))
	}
	u0, _ := deps.agentCtx.Messages[0].(agentcore.UserMessage)
	u2, _ := deps.agentCtx.Messages[2].(agentcore.UserMessage)
	if agentcore.ContentToText(u0.Content) != "first question" || agentcore.ContentToText(u2.Content) != "second question" {
		t.Errorf("history not accumulated in order: %q, %q", agentcore.ContentToText(u0.Content), agentcore.ContentToText(u2.Content))
	}
	// The session must have been persisted after the runs.
	headers, err := store.List()
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(headers) != 1 {
		t.Errorf("expected 1 saved session, got %d", len(headers))
	}
}
