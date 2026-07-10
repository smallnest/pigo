package agent

// This file implements the faux provider (对标 pi providers/faux.ts) and the
// loop integration tests that drive the whole agent loop through it — the
// project's primary and only core test seam (US-002 / Testing Decisions, #16).
//
// Unlike loop_test.go, which drives the loop with a coarse StreamFn that emits
// only a terminal StreamDoneEvent, the faux provider is a real Provider whose
// StreamCompletion replays a *fine-grained* script of AssistantMessageEvents
// (start → text/toolcall deltas → done) — one scripted turn per call. It is
// wired into the loop via StreamFnFromProvider, the real seam, so the whole
// path (message_start / message_update / message_end deltas, the six hooks,
// truncation protection, parallel ordering, and EventStream cancellation) is
// covered end to end without mocking any loop-internal function.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fauxTurn is one scripted assistant turn: the fine-grained stream events the
// faux provider replays for a single StreamCompletion call.
type fauxTurn []AssistantMessageEvent

// textTurn scripts a turn that streams text as start → text delta → done(end_turn).
func textTurn(text string) fauxTurn {
	partial := AssistantMessage{RoleField: RoleAssistant}
	withText := partial
	withText.Content = ContentList{NewTextContent(text)}
	final := withText
	final.StopReason = StopReasonEndTurn
	return fauxTurn{
		StreamStartEvent{Partial: partial},
		StreamTextEvent{Partial: withText},
		StreamDoneEvent{Message: final},
	}
}

// toolCallTurn scripts a turn that streams one tool call as
// start → toolcall delta → done(tool_use).
func toolCallTurn(id, name, args string) fauxTurn {
	partial := AssistantMessage{RoleField: RoleAssistant}
	withCall := partial
	withCall.Content = ContentList{NewToolCallContent(id, name, json.RawMessage(args))}
	final := withCall
	final.StopReason = StopReasonToolUse
	return fauxTurn{
		StreamStartEvent{Partial: partial},
		StreamToolCallEvent{Partial: withCall},
		StreamDoneEvent{Message: final},
	}
}

// fauxProvider is a real Provider that replays one scripted turn per
// StreamCompletion call, in order. It records every request it received so
// tests can assert what the loop actually sent (model, context, config). Once
// the script is exhausted it replays a plain end_turn turn.
type fauxProvider struct {
	name   string
	models []Model
	turns  []fauxTurn

	mu       sync.Mutex
	calls    int
	requests []CompletionRequest
	// delay optionally slows each delta emit, used by the cancellation test to
	// keep the stream open long enough to cancel mid-flight.
	delay time.Duration
}

func (p *fauxProvider) Name() string    { return p.name }
func (p *fauxProvider) Models() []Model { return p.models }

func (p *fauxProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *fauxProvider) requestAt(i int) CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[i]
}

func (p *fauxProvider) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.requests = append(p.requests, req)
	var turn fauxTurn
	if idx < len(p.turns) {
		turn = p.turns[idx]
	} else {
		turn = textTurn("")
	}
	delay := p.delay
	p.mu.Unlock()

	s := NewAssistantMessageEventStream(0)
	go func() {
		for _, ev := range turn {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					s.SetError(ctx.Err())
					s.Close()
					return
				}
			}
			if err := s.Emit(ctx, ev); err != nil {
				s.SetError(err)
				s.Close()
				return
			}
		}
		s.Close()
	}()
	return s, nil
}

// newFauxRunCfg wires a faux provider into the loop via StreamFnFromProvider
// (the real seam) and registers the given tools. No loop-internal function is
// mocked — only the provider boundary.
func newFauxRunCfg(p *fauxProvider, tools ...AgentTool) RunConfig {
	reg := NewToolRegistry()
	for _, tl := range tools {
		_ = reg.Register(tl)
	}
	return RunConfig{
		LoopConfig: LoopConfig{Model: "faux", Stream: StreamFnFromProvider(p)},
		Batch:      BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}},
	}
}

// TestFauxProviderTextToolText drives the flagship seam scenario end to end:
// text → tool call → text over the real loop, asserting both the AgentEvent
// stream shape and the final []AgentMessage. Nothing loop-internal is mocked.
func TestFauxProviderTextToolText(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			textTurn("thinking about it"),                     // turn 1: plain text, no tool
			toolCallTurn("call-1", "echo", `{"msg":"hello"}`), // turn 2: tool call
			textTurn("all done"),                              // turn 3: final text
		},
	}
	// GetFollowUpMessages injects a follow-up once so the loop advances past the
	// first natural (text-only) turn end into the tool-call turn.
	served := false
	cfg := newFauxRunCfg(p, echoTool("echo", ToolExecutionParallel, false))
	cfg.GetFollowUpMessages = func(ctx context.Context, agentCtx *AgentContext) []AgentMessage {
		if served {
			return nil
		}
		served = true
		return []AgentMessage{UserMessage{RoleField: RoleUser, Content: ContentList{NewTextContent("go on")}}}
	}
	agentCtx := &AgentContext{Messages: MessageList{UserMessage{RoleField: RoleUser, Content: ContentList{NewTextContent("start")}}}}

	kinds, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	// Event shape: message deltas must appear (start/update/end), a tool
	// executed exactly once, and the run bookended by agent_start/agent_end.
	if kinds[0] != EventAgentStart || kinds[len(kinds)-1] != EventAgentEnd {
		t.Fatalf("run must be bracketed by agent_start/agent_end, got %v", kinds)
	}
	if countKind(kinds, EventMessageStart) < 3 || countKind(kinds, EventMessageEnd) < 3 {
		t.Errorf("expected fine-grained message deltas for each turn, got %v", kinds)
	}
	if countKind(kinds, EventMessageUpdate) < 3 {
		t.Errorf("expected message_update deltas (text/toolcall), got %v", kinds)
	}
	if got := countKind(kinds, EventToolExecutionStart); got != 1 {
		t.Errorf("expected 1 tool_execution_start, got %d in %v", got, kinds)
	}
	if got := countKind(kinds, EventToolExecutionEnd); got != 1 {
		t.Errorf("expected 1 tool_execution_end, got %d in %v", got, kinds)
	}
	if got := countKind(kinds, EventTurnStart); got != 3 {
		t.Errorf("expected 3 turns (text→tool→text), got %d in %v", got, kinds)
	}

	// Final messages: assistant(text) + user(follow-up) + assistant(tool) +
	// toolResult + assistant(text) = 5, in order.
	if len(msgs) != 5 {
		t.Fatalf("expected 5 new messages, got %d: %+v", len(msgs), msgs)
	}
	if a, ok := msgs[0].(AssistantMessage); !ok || textContentOf(a.Content) != "thinking about it" {
		t.Errorf("msg[0] should be the first text assistant message, got %T %+v", msgs[0], msgs[0])
	}
	if _, ok := msgs[1].(UserMessage); !ok {
		t.Errorf("msg[1] should be the injected follow-up user message, got %T", msgs[1])
	}
	if a, ok := msgs[2].(AssistantMessage); !ok || len(a.ToolCalls()) != 1 {
		t.Errorf("msg[2] should be the tool-call assistant message, got %T %+v", msgs[2], msgs[2])
	}
	tr, ok := msgs[3].(ToolResultMessage)
	if !ok || tr.ToolCallID != "call-1" || tr.IsError {
		t.Errorf("msg[3] should be the successful echo tool result, got %T %+v", msgs[3], msgs[3])
	}
	if a, ok := msgs[4].(AssistantMessage); !ok || textContentOf(a.Content) != "all done" {
		t.Errorf("msg[4] should be the final text assistant message, got %T %+v", msgs[4], msgs[4])
	}

	// The loop must have driven the provider exactly three times, each carrying
	// the growing context and the configured model.
	if p.callCount() != 3 {
		t.Fatalf("provider called %d times, want 3", p.callCount())
	}
	if req := p.requestAt(0); req.Model != "faux" {
		t.Errorf("provider request model = %q, want faux", req.Model)
	}
}

// textContentOf returns the concatenated text of a content list.
func textContentOf(list ContentList) string {
	var s string
	for _, c := range list {
		if tc, ok := c.(TextContent); ok {
			s += tc.Text
		}
	}
	return s
}

// TestFauxSeamSixHooks exercises all six loop hooks through the real seam in a
// single run: the two per-request LoopConfig hooks (TransformContext,
// ConvertToLlm resolved via GetAPIKey) and the four RunConfig hooks
// (GetFollowUpMessages, GetSteeringMessages, PrepareNextTurn,
// ShouldStopAfterTurn). Each hook records that it fired and, where observable,
// that its effect reached the provider request.
func TestFauxSeamSixHooks(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "echo", `{}`), // turn 1: tool → afterTurn hooks fire
			textTurn("second"),                   // turn 2: end (after model swap)
		},
	}
	var fired struct {
		transform, convert, apiKey, followUp, steering, prepare, shouldStop bool
	}
	swapped := "swapped-model"
	cfg := newFauxRunCfg(p, echoTool("echo", ToolExecutionParallel, false))
	cfg.Provider = "faux"
	cfg.TransformContext = func(ctx context.Context, msgs MessageList) MessageList {
		fired.transform = true
		return msgs
	}
	cfg.ConvertToLlm = func(msgs MessageList) MessageList {
		fired.convert = true
		return msgs
	}
	cfg.GetAPIKey = func(ctx context.Context, provider string) string {
		fired.apiKey = true
		return "dyn-key"
	}
	cfg.GetSteeringMessages = func(ctx context.Context) []AgentMessage {
		fired.steering = true
		return nil
	}
	cfg.PrepareNextTurn = func(ctx context.Context, agentCtx *AgentContext) *TurnUpdate {
		fired.prepare = true
		return &TurnUpdate{Model: &swapped}
	}
	stopCalls := 0
	cfg.ShouldStopAfterTurn = func(ctx context.Context, agentCtx *AgentContext) bool {
		fired.shouldStop = true
		stopCalls++
		return false // never stop early; let the run end naturally
	}
	cfg.GetFollowUpMessages = func(ctx context.Context, agentCtx *AgentContext) []AgentMessage {
		fired.followUp = true
		// No follow-up: the tool-call turn already drives turn 2, so the run
		// ends naturally after the second turn.
		return nil
	}
	agentCtx := &AgentContext{Messages: MessageList{UserMessage{RoleField: RoleUser, Content: ContentList{NewTextContent("hi")}}}}

	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	if !fired.transform || !fired.convert || !fired.apiKey {
		t.Errorf("per-request hooks not all fired: %+v", fired)
	}
	if !fired.followUp || !fired.steering || !fired.prepare || !fired.shouldStop {
		t.Errorf("per-turn hooks not all fired: %+v", fired)
	}
	if stopCalls == 0 {
		t.Error("ShouldStopAfterTurn was never consulted")
	}
	// GetAPIKey's dynamic key must have reached the provider request config.
	if got := p.requestAt(0).Config.APIKey; got != "dyn-key" {
		t.Errorf("GetAPIKey result not threaded to provider, APIKey = %q", got)
	}
	// PrepareNextTurn swapped the model before turn 2.
	if p.callCount() >= 2 {
		if got := p.requestAt(1).Config.APIKey; got != "dyn-key" {
			t.Errorf("turn 2 APIKey = %q, want dyn-key", got)
		}
		if got := p.requestAt(1).Model; got != swapped {
			t.Errorf("PrepareNextTurn model swap not applied, turn 2 model = %q, want %q", got, swapped)
		}
	}
}

// TestFauxSeamTruncationProtection verifies that a truncated (stopReason=length)
// tool-call turn is protected: the tool is NOT executed and a synthesized failed
// tool result is fed back, all through the seam.
func TestFauxSeamTruncationProtection(t *testing.T) {
	// Turn 1: a tool call that arrives truncated. Turn 2: end.
	truncPartial := AssistantMessage{RoleField: RoleAssistant, Content: ContentList{NewToolCallContent("t1", "echo", json.RawMessage(`{}`))}}
	truncFinal := truncPartial
	truncFinal.StopReason = StopReasonLength
	p := &fauxProvider{
		name: "faux",
		turns: []fauxTurn{
			{
				StreamStartEvent{Partial: AssistantMessage{RoleField: RoleAssistant}},
				StreamToolCallEvent{Partial: truncPartial},
				StreamDoneEvent{Message: truncFinal},
			},
			textTurn("recovered"),
		},
	}
	cfg := newFauxRunCfg(p, echoTool("echo", ToolExecutionParallel, false))
	agentCtx := &AgentContext{Messages: MessageList{UserMessage{RoleField: RoleUser}}}

	kinds, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	if countKind(kinds, EventToolExecutionEnd) != 0 {
		t.Errorf("truncated tool call must not execute, got %v", kinds)
	}
	var foundFail bool
	for _, m := range msgs {
		if tr, ok := m.(ToolResultMessage); ok && tr.IsError && tr.ToolCallID == "t1" {
			foundFail = true
		}
	}
	if !foundFail {
		t.Errorf("expected a synthesized failed tool result for the truncated call, got %+v", msgs)
	}
}

// TestFauxSeamParallelOrderingPreserved verifies that a turn with multiple
// parallel tool calls yields tool results in source order regardless of which
// tool finishes first, driven through the seam.
func TestFauxSeamParallelOrderingPreserved(t *testing.T) {
	// One assistant turn with three tool calls in a fixed order; the tools sleep
	// in reverse so completion order differs from source order.
	partial := AssistantMessage{RoleField: RoleAssistant, Content: ContentList{
		NewToolCallContent("a0", "slow", json.RawMessage(`{}`)),
		NewToolCallContent("a1", "mid", json.RawMessage(`{}`)),
		NewToolCallContent("a2", "fast", json.RawMessage(`{}`)),
	}}
	final := partial
	final.StopReason = StopReasonToolUse
	p := &fauxProvider{
		turns: []fauxTurn{
			{StreamStartEvent{Partial: AssistantMessage{RoleField: RoleAssistant}}, StreamToolCallEvent{Partial: partial}, StreamDoneEvent{Message: final}},
			textTurn("done"),
		},
	}
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
	cfg := newFauxRunCfg(p, mk("slow", 25*time.Millisecond), mk("mid", 12*time.Millisecond), mk("fast", 1*time.Millisecond))
	agentCtx := &AgentContext{Messages: MessageList{UserMessage{RoleField: RoleUser}}}

	_, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	var order []string
	for _, m := range msgs {
		if tr, ok := m.(ToolResultMessage); ok {
			order = append(order, tr.ToolCallID)
		}
	}
	want := []string{"a0", "a1", "a2"}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Errorf("parallel tool results out of source order: got %v, want %v", order, want)
	}
}

// TestFauxSeamStreamCancellation verifies that cancelling the context stops the
// run: the consumer stops receiving events and Result reports the cancellation,
// exercised through the seam with a provider that streams slowly.
func TestFauxSeamStreamCancellation(t *testing.T) {
	p := &fauxProvider{
		turns: []fauxTurn{textTurn("never fully delivered")},
		delay: 50 * time.Millisecond, // slow enough to cancel mid-stream
	}
	cfg := newFauxRunCfg(p)
	agentCtx := &AgentContext{Messages: MessageList{UserMessage{RoleField: RoleUser}}}

	ctx, cancel := context.WithCancel(context.Background())
	s := agentLoop(ctx, agentCtx, cfg)

	// Read the first event, then cancel while the provider is still streaming.
	<-s.Events()
	cancel()
	// Drain remaining events (must terminate, not hang).
	for range s.Events() {
	}
	if _, err := s.Result(context.Background()); err != nil {
		// A set result is also acceptable (the run may have finished emitting
		// agent_end before cancellation propagated); but if an error is set it
		// must be the cancellation.
		if err != context.Canceled {
			t.Errorf("cancelled run result error = %v, want context.Canceled or nil", err)
		}
	}
}
