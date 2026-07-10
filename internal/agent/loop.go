// This file implements pi's two-layer agent loop (US-006, FR-1). It strings
// together streaming assistant responses, batch tool execution, and the loop's
// six hooks with control flow kept faithful to pi's runLoop:
//
//   - Inner loop: one turn = stream an assistant response → execute its tool
//     calls → feed the results back, repeating until an assistant message has no
//     tool calls (a natural turn end).
//   - Outer loop: after the inner loop settles, pull getFollowUpMessages; if any
//     are returned they become the next pending input and the inner loop runs
//     again, otherwise the run ends.
//
// Per-turn hooks after each turn_end: getSteeringMessages (pulled after tool
// execution and injected before the next turn), prepareNextTurn (may swap
// context / model / thinkingLevel), shouldStopAfterTurn (true ⇒ agent_end +
// exit). Two stop reasons are handled specially: length (the response was
// truncated by the token cap) fails every tool call so the model resends
// (failToolCallsFromTruncatedMessage); error / aborted end the run immediately.
//
// agentLoop starts a fresh run from a prompt already appended to the context;
// agentLoopContinue resumes an existing context and validates that the last
// message is not an assistant message (nothing to continue from otherwise).
package agent

import (
	"context"
	"errors"
)

// TurnUpdate is the optional result of PrepareNextTurn: any non-nil field
// replaces the corresponding piece of loop state before the next turn. It lets
// a caller swap the trimmed context, system prompt, tool set, model, or
// thinking level between turns (FR-6).
type TurnUpdate struct {
	Messages      *MessageList
	SystemPrompt  *string
	Tools         *[]AgentTool
	Model         *string
	ThinkingLevel *ThinkingLevel
}

// RunConfig is the full configuration for a loop run: the per-turn streaming
// config (embedded LoopConfig), the batch tool-execution config, and the four
// loop-level hooks. Every hook is optional (nil = default behavior).
type RunConfig struct {
	LoopConfig
	// Batch holds the tool registry and the prepare/before/after hooks used to
	// execute each assistant message's tool calls.
	Batch BatchConfig

	// GetFollowUpMessages is consulted after the inner loop settles (an assistant
	// message with no tool calls). Returning messages continues the outer loop
	// with them as the next input; returning none ends the run (FR-9).
	GetFollowUpMessages func(ctx context.Context, agentCtx *AgentContext) []AgentMessage
	// GetSteeringMessages is pulled after each turn's tool execution and injected
	// before the next turn (pi per-turn semantics, FR-8).
	GetSteeringMessages func(ctx context.Context) []AgentMessage
	// PrepareNextTurn runs after each turn_end and may swap context / model /
	// thinkingLevel for the next turn (FR-6).
	PrepareNextTurn func(ctx context.Context, agentCtx *AgentContext) *TurnUpdate
	// ShouldStopAfterTurn runs after each turn_end; true ends the run with an
	// agent_end event (FR-7).
	ShouldStopAfterTurn func(ctx context.Context, agentCtx *AgentContext) bool

	// EventBuffer is the buffer size of the emitted EventStream. 0 gives fully
	// synchronous back-pressure (matching pi's awaited emit).
	EventBuffer int
}

// LoopEventStream is the stream returned by the loop entry points: it carries
// AgentEvents and yields the messages newly produced during the run.
type LoopEventStream = EventStream[AgentEvent, []AgentMessage]

// ErrContinueLastAssistant is the result error when agentLoopContinue is called
// on a context whose last message is an assistant message (nothing to respond
// to — the loop would immediately stream another assistant turn against a stale
// context).
var ErrContinueLastAssistant = errors.New("agent: cannot continue loop, last message is an assistant message")

// agentLoop starts a fresh run. The caller has already appended the initiating
// user message(s) to agentCtx.Messages. It returns immediately with an
// EventStream; a producer goroutine drives the loop and closes the stream when
// the run ends.
func agentLoop(ctx context.Context, agentCtx *AgentContext, cfg RunConfig) *LoopEventStream {
	stream := NewEventStream[AgentEvent, []AgentMessage](cfg.EventBuffer)
	go runLoop(ctx, agentCtx, cfg, stream)
	return stream
}

// agentLoopContinue resumes an existing context. It validates that the last
// message is not an assistant message before running; on violation it returns a
// stream that yields ErrContinueLastAssistant with no events.
func agentLoopContinue(ctx context.Context, agentCtx *AgentContext, cfg RunConfig) *LoopEventStream {
	stream := NewEventStream[AgentEvent, []AgentMessage](cfg.EventBuffer)
	if n := len(agentCtx.Messages); n > 0 {
		if _, isAssistant := agentCtx.Messages[n-1].(AssistantMessage); isAssistant {
			stream.SetError(ErrContinueLastAssistant)
			stream.Close()
			return stream
		}
	}
	go runLoop(ctx, agentCtx, cfg, stream)
	return stream
}

// runLoop is the producer: it drives the two-layer loop, emitting events onto
// stream and setting the stream result to the messages produced during the run.
func runLoop(ctx context.Context, agentCtx *AgentContext, cfg RunConfig, stream *LoopEventStream) {
	startIdx := len(agentCtx.Messages)
	// newMessages returns the messages appended since the run began.
	newMessages := func() []AgentMessage {
		if len(agentCtx.Messages) <= startIdx {
			return nil
		}
		out := make([]AgentMessage, len(agentCtx.Messages)-startIdx)
		copy(out, agentCtx.Messages[startIdx:])
		return out
	}
	emit := func(ev AgentEvent) error { return stream.Emit(ctx, ev) }

	// finish emits agent_end (unless suppressed by a prior emit error), records
	// the run result, and closes the stream exactly once.
	finish := func() {
		msgs := newMessages()
		_ = emit(AgentEndEvent{Messages: msgs})
		stream.SetResult(msgs)
		stream.Close()
	}

	if err := emit(AgentStartEvent{}); err != nil {
		finish()
		return
	}

	for { // outer loop: pending / follow-up messages
		for { // inner loop: turns until no tool calls
			if err := emit(TurnStartEvent{}); err != nil {
				finish()
				return
			}

			assistant, err := streamAssistantResponse(ctx, agentCtx, cfg.LoopConfig, func(c context.Context, ev AgentEvent) error {
				return stream.Emit(c, ev)
			})
			if err != nil {
				// emit was cancelled mid-stream; end the run.
				finish()
				return
			}

			switch assistant.StopReason {
			case StopReasonLength:
				// Truncated by the token cap: fail every tool call so the model
				// resends, then continue feeding back.
				toolResults := failToolCallsFromTruncatedMessage(agentCtx, assistant)
				if err := emit(TurnEndEvent{Message: assistant, ToolResults: toolResults}); err != nil {
					finish()
					return
				}
				if afterTurn(ctx, agentCtx, &cfg, true) {
					finish()
					return
				}
				continue
			case StopReasonError, StopReasonAborted:
				// Terminal failure: emit the turn end and stop.
				_ = emit(TurnEndEvent{Message: assistant})
				finish()
				return
			}

			calls := toAgentToolCalls(assistant.ToolCalls())
			if len(calls) == 0 {
				// Natural turn end: no tools to run.
				if err := emit(TurnEndEvent{Message: assistant}); err != nil {
					finish()
					return
				}
				if afterTurn(ctx, agentCtx, &cfg, false) {
					finish()
					return
				}
				break // exit inner loop → consult follow-up messages
			}

			toolResults, allTerminate := executeToolCalls(ctx, cfg.Batch, calls, func(c context.Context, ev AgentEvent) error {
				return stream.Emit(c, ev)
			})
			for _, tr := range toolResults {
				agentCtx.Messages = append(agentCtx.Messages, tr)
			}
			if err := emit(TurnEndEvent{Message: assistant, ToolResults: toolResults}); err != nil {
				finish()
				return
			}
			if allTerminate {
				// Every tool asked to terminate the run.
				finish()
				return
			}
			if afterTurn(ctx, agentCtx, &cfg, true) {
				finish()
				return
			}
			// Feed the tool results back into the next turn.
		}

		// Inner loop settled: consult follow-up messages.
		if cfg.GetFollowUpMessages != nil {
			if follow := cfg.GetFollowUpMessages(ctx, agentCtx); len(follow) > 0 {
				agentCtx.Messages = append(agentCtx.Messages, follow...)
				continue // outer loop with the follow-ups as new input
			}
		}
		break
	}

	finish()
}

// afterTurn runs the per-turn hooks after a turn_end. When hadToolExecution is
// true it first pulls getSteeringMessages and injects them before the next turn
// (pi per-turn semantics). It then applies prepareNextTurn and finally consults
// shouldStopAfterTurn, returning true when the run should end.
func afterTurn(ctx context.Context, agentCtx *AgentContext, cfg *RunConfig, hadToolExecution bool) (stop bool) {
	if hadToolExecution && cfg.GetSteeringMessages != nil {
		if steer := cfg.GetSteeringMessages(ctx); len(steer) > 0 {
			agentCtx.Messages = append(agentCtx.Messages, steer...)
		}
	}
	if cfg.PrepareNextTurn != nil {
		if upd := cfg.PrepareNextTurn(ctx, agentCtx); upd != nil {
			applyTurnUpdate(agentCtx, cfg, upd)
		}
	}
	if cfg.ShouldStopAfterTurn != nil {
		return cfg.ShouldStopAfterTurn(ctx, agentCtx)
	}
	return false
}

// applyTurnUpdate applies a non-nil TurnUpdate to the mutable loop state: any
// set field replaces the current context / config value for the next turn.
func applyTurnUpdate(agentCtx *AgentContext, cfg *RunConfig, upd *TurnUpdate) {
	if upd.Messages != nil {
		agentCtx.Messages = *upd.Messages
	}
	if upd.SystemPrompt != nil {
		agentCtx.SystemPrompt = *upd.SystemPrompt
	}
	if upd.Tools != nil {
		agentCtx.Tools = *upd.Tools
	}
	if upd.Model != nil {
		cfg.Model = *upd.Model
	}
	if upd.ThinkingLevel != nil {
		cfg.ThinkingLevel = *upd.ThinkingLevel
	}
}

// failToolCallsFromTruncatedMessage produces an error tool-result message for
// every tool call in a truncated (stopReason=length) assistant message, telling
// the model the response was cut off and to resend. The results are appended to
// the context and returned. Mirrors pi's failToolCallsFromTruncatedMessage.
func failToolCallsFromTruncatedMessage(agentCtx *AgentContext, assistant AssistantMessage) []ToolResultMessage {
	calls := assistant.ToolCalls()
	if len(calls) == 0 {
		return nil
	}
	results := make([]ToolResultMessage, 0, len(calls))
	for _, c := range calls {
		results = append(results, ToolResultMessage{
			RoleField:  RoleToolResult,
			ToolCallID: c.ID,
			ToolName:   c.Name,
			Content: ContentList{NewTextContent(
				"The previous response was truncated because it hit the output token limit, " +
					"so this tool call was not executed. Please send a shorter response and retry.")},
			IsError: true,
		})
	}
	for _, r := range results {
		agentCtx.Messages = append(agentCtx.Messages, r)
	}
	return results
}

// toAgentToolCalls converts the assistant message's ToolCallContent blocks into
// the loop-level AgentToolCall view executeToolCalls consumes.
func toAgentToolCalls(blocks []ToolCallContent) []AgentToolCall {
	if len(blocks) == 0 {
		return nil
	}
	calls := make([]AgentToolCall, len(blocks))
	for i, b := range blocks {
		calls[i] = AgentToolCall{ID: b.ID, Name: b.Name, Arguments: b.Arguments}
	}
	return calls
}
