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
// agentLoop starts a fresh run from a prompt already appended to the context.
package runtime

import (
	"context"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/provider"
)

// nowMillis returns the current Unix time in milliseconds, the timestamp unit
// used for CompactionMessage checkpoints.
func nowMillis() int64 { return time.Now().UnixMilli() }

// TurnUpdate is the optional result of PrepareNextTurn: any non-nil field
// replaces the corresponding piece of loop state before the next turn. It lets
// a caller swap the trimmed context, system prompt, tool set, model, or
// thinking level between turns (FR-6).
type TurnUpdate struct {
	Messages      *agentcore.MessageList
	SystemPrompt  *string
	Tools         *[]agentcore.AgentTool
	Model         *string
	ThinkingLevel *agentcore.ThinkingLevel
}

// RunConfig is the full configuration for a loop run: the per-turn streaming
// config (embedded LoopConfig), the batch tool-execution config, and the four
// loop-level hooks. Every hook is optional (nil = default behavior).
type RunConfig struct {
	LoopConfig
	// Batch holds the tool registry and the prepare/before/after hooks used to
	// execute each assistant message's tool calls.
	Batch agenttool.BatchConfig

	// GetFollowUpMessages is consulted after the inner loop settles (an assistant
	// message with no tool calls). Returning messages continues the outer loop
	// with them as the next input; returning none ends the run (FR-9).
	GetFollowUpMessages func(ctx context.Context, agentCtx *agentcore.AgentContext) []agentcore.AgentMessage
	// GetSteeringMessages is pulled after each turn's tool execution and injected
	// before the next turn (pi per-turn semantics, FR-8).
	GetSteeringMessages func(ctx context.Context) []agentcore.AgentMessage
	// PrepareNextTurn runs after each turn_end and may swap context / model /
	// thinkingLevel for the next turn (FR-6).
	PrepareNextTurn func(ctx context.Context, agentCtx *agentcore.AgentContext) *TurnUpdate
	// ShouldStopAfterTurn runs after each turn_end; true ends the run with an
	// agent_end event (FR-7).
	ShouldStopAfterTurn func(ctx context.Context, agentCtx *agentcore.AgentContext) bool

	// Reminders holds the per-turn system-reminder providers (US-002, FR-1/FR-2).
	// When non-empty, ephemeral <system-reminder> messages are injected into each
	// turn's LLM request through the existing TransformContext seam, so they never
	// enter the persisted history. nil / empty = no injection.
	Reminders *ReminderRegistry

	// EventBuffer is the buffer size of the emitted EventStream. 0 gives fully
	// synchronous back-pressure (matching pi's awaited emit).
	EventBuffer int

	// SessionID, when set, is carried in the run's agent_start event so a
	// stream-json consumer sees the backing session id in the first event and can
	// resume the run later (对标 pi/Claude Code).
	SessionID string
}

// LoopEventStream is the stream returned by the loop entry points: it carries
// AgentEvents and yields the messages newly produced during the run.
type LoopEventStream = agentcore.EventStream[agentcore.AgentEvent, []agentcore.AgentMessage]

// agentLoop starts a fresh run. The caller has already appended the initiating
// user message(s) to agentCtx.Messages. It returns immediately with an
// EventStream; a producer goroutine drives the loop and closes the stream when
// the run ends.
func agentLoop(ctx context.Context, agentCtx *agentcore.AgentContext, cfg RunConfig) *LoopEventStream {
	stream := agentcore.NewEventStream[agentcore.AgentEvent, []agentcore.AgentMessage](cfg.EventBuffer)
	go runLoop(ctx, agentCtx, cfg, stream)
	return stream
}

// StartRun is the exported entry point for a fresh run, used by out-of-package
// drivers (the interactive REPL, US-022). It is a thin wrapper over agentLoop so
// the loop internals stay unexported while callers outside the package can
// still launch a run and consume its event stream.
func StartRun(ctx context.Context, agentCtx *agentcore.AgentContext, cfg RunConfig) *LoopEventStream {
	return agentLoop(ctx, agentCtx, cfg)
}

// runLoop is the producer: it drives the two-layer loop, emitting events onto
// stream and setting the stream result to the messages produced during the run.
func runLoop(ctx context.Context, agentCtx *agentcore.AgentContext, cfg RunConfig, stream *LoopEventStream) {
	// Wire per-turn system-reminder injection (US-002) onto the TransformContext
	// seam. Reminders are appended to the request-shaped copy only, so they stay
	// ephemeral: never written back to agentCtx.Messages, never persisted, never
	// swept into a compaction summary.
	if !cfg.Reminders.Empty() {
		cfg.TransformContext = cfg.Reminders.wrapTransform(cfg.TransformContext)
	}
	startIdx := len(agentCtx.Messages)
	// newMessages returns the messages appended since the run began.
	newMessages := func() []agentcore.AgentMessage {
		if len(agentCtx.Messages) <= startIdx {
			return nil
		}
		out := make([]agentcore.AgentMessage, len(agentCtx.Messages)-startIdx)
		copy(out, agentCtx.Messages[startIdx:])
		return out
	}
	emit := func(ev agentcore.AgentEvent) error { return stream.Emit(ctx, ev) }

	// finish emits agent_end (unless suppressed by a prior emit error), records
	// the run result, and closes the stream exactly once.
	finish := func() {
		msgs := newMessages()
		_ = emit(agentcore.AgentEndEvent{Messages: msgs})
		stream.SetResult(msgs)
		stream.Close()
	}

	if err := emit(agentcore.AgentStartEvent{SessionID: cfg.SessionID}); err != nil {
		finish()
		return
	}

	for { // outer loop: pending / follow-up messages
		for { // inner loop: turns until no tool calls
			if err := emit(agentcore.TurnStartEvent{}); err != nil {
				finish()
				return
			}

			assistant, err := streamAssistantResponse(ctx, agentCtx, cfg.LoopConfig, func(c context.Context, ev agentcore.AgentEvent) error {
				return stream.Emit(c, ev)
			})
			if err != nil {
				// emit was cancelled mid-stream; end the run.
				finish()
				return
			}

			switch assistant.StopReason {
			case agentcore.StopReasonLength:
				// Truncated by the token cap: fail every tool call so the model
				// resends, then continue feeding back.
				toolResults := failToolCallsFromTruncatedMessage(agentCtx, assistant)
				if err := emit(agentcore.TurnEndEvent{Message: assistant, ToolResults: toolResults}); err != nil {
					finish()
					return
				}
				if afterTurn(ctx, agentCtx, &cfg, true, emit) {
					finish()
					return
				}
				continue
			case agentcore.StopReasonError, agentcore.StopReasonAborted:
				// Terminal failure: emit the turn end and stop.
				_ = emit(agentcore.TurnEndEvent{Message: assistant})
				finish()
				return
			}

			calls := toAgentToolCalls(assistant.ToolCalls())
			if len(calls) == 0 {
				// Natural turn end: no tools to run.
				if err := emit(agentcore.TurnEndEvent{Message: assistant}); err != nil {
					finish()
					return
				}
				if afterTurn(ctx, agentCtx, &cfg, false, emit) {
					finish()
					return
				}
				break // exit inner loop → consult follow-up messages
			}

			toolResults, allTerminate := agenttool.ExecuteToolCalls(ctx, cfg.Batch, calls, func(c context.Context, ev agentcore.AgentEvent) error {
				return stream.Emit(c, ev)
			})
			for _, tr := range toolResults {
				agentCtx.Messages = append(agentCtx.Messages, tr)
			}
			if err := emit(agentcore.TurnEndEvent{Message: assistant, ToolResults: toolResults}); err != nil {
				finish()
				return
			}
			if allTerminate {
				// Every tool asked to terminate the run.
				finish()
				return
			}
			if afterTurn(ctx, agentCtx, &cfg, true, emit) {
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
// (pi per-turn semantics). It then applies prepareNextTurn, runs auto-compaction
// when the context has outgrown its window, and finally consults
// shouldStopAfterTurn, returning true when the run should end.
func afterTurn(ctx context.Context, agentCtx *agentcore.AgentContext, cfg *RunConfig, hadToolExecution bool, emit func(agentcore.AgentEvent) error) (stop bool) {
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
	maybeAutoCompact(ctx, agentCtx, cfg, emit)
	if cfg.ShouldStopAfterTurn != nil {
		return cfg.ShouldStopAfterTurn(ctx, agentCtx)
	}
	return false
}

// maybeAutoCompact checks whether the context has outgrown its usable window and,
// if so, compacts it in place and emits a CompactionEvent. Compaction is a no-op
// when disabled, when the context window is unknown (<= 0), or when usage is
// under threshold. A compaction failure is non-fatal: the original context is
// preserved and a CompactionEvent carrying ErrorMessage is emitted so the failure
// is observable without aborting the run (US-004).
func maybeAutoCompact(ctx context.Context, agentCtx *agentcore.AgentContext, cfg *RunConfig, emit func(agentcore.AgentEvent) error) {
	if !cfg.Compaction.Enabled || cfg.ContextWindow <= 0 {
		return
	}
	before := compaction.EstimateContextTokens(agentCtx.Messages).Tokens
	if !compaction.ShouldCompact(before, cfg.ContextWindow, cfg.Compaction) {
		return
	}
	res, err := runCompaction(ctx, agentCtx.Messages, cfg)
	kept := len(agentCtx.Messages)
	if err != nil {
		_ = emit(agentcore.CompactionEvent{
			Reason:       "threshold",
			TokensBefore: before,
			TokensAfter:  before,
			KeptCount:    kept,
			ErrorMessage: err.Error(),
		})
		return
	}
	if res == nil {
		// Nothing to summarize (cut point left no prefix); leave context as-is.
		return
	}
	now := nowMillis()
	rebuilt := res.RebuildContext(agentCtx.Messages, now)
	summarized := len(agentCtx.Messages) - (len(rebuilt) - 1)
	agentCtx.Messages = rebuilt
	after := compaction.EstimateContextTokens(rebuilt).Tokens
	_ = emit(agentcore.CompactionEvent{
		Reason:          "threshold",
		TokensBefore:    before,
		TokensAfter:     after,
		SummarizedCount: summarized,
		KeptCount:       len(rebuilt) - 1,
	})
}

// runCompaction invokes compaction.Compact with the loop's summarization config,
// falling back to the primary Stream/Model when the summary-specific fields are
// unset. Compact derives the cut point from settings.KeepRecentTokens.
func runCompaction(ctx context.Context, msgs agentcore.MessageList, cfg *RunConfig) (*compaction.CompactionResult, error) {
	stream := cfg.SummaryStream
	if stream == nil {
		stream = cfg.Stream
	}
	model := cfg.SummaryModel
	if model.ID == "" {
		model = provider.Model{Provider: cfg.Provider, ID: cfg.Model, ContextWindow: cfg.ContextWindow}
	}
	// Resolve the API key the same way the primary turn does (dynamic key wins,
	// static APIKey is the fallback) so the summarization stream authenticates
	// against auth-requiring providers instead of failing with "missing API key".
	key := cfg.APIKey
	if cfg.GetAPIKey != nil {
		if dyn := cfg.GetAPIKey(ctx, cfg.Provider); dyn != "" {
			key = dyn
		}
	}
	scfg := provider.StreamConfig{APIKey: key, ThinkingLevel: cfg.ThinkingLevel}
	return compaction.Compact(ctx, stream, model, msgs, cfg.Compaction, -1, nil, "", scfg)
}

// applyTurnUpdate applies a non-nil TurnUpdate to the mutable loop state: any
// set field replaces the current context / config value for the next turn.
func applyTurnUpdate(agentCtx *agentcore.AgentContext, cfg *RunConfig, upd *TurnUpdate) {
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
func failToolCallsFromTruncatedMessage(agentCtx *agentcore.AgentContext, assistant agentcore.AssistantMessage) []agentcore.ToolResultMessage {
	calls := assistant.ToolCalls()
	if len(calls) == 0 {
		return nil
	}
	results := make([]agentcore.ToolResultMessage, 0, len(calls))
	for _, c := range calls {
		results = append(results, agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: c.ID,
			ToolName:   c.Name,
			Content: agentcore.ContentList{agentcore.NewTextContent(
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
func toAgentToolCalls(blocks []agentcore.ToolCallContent) []agentcore.AgentToolCall {
	if len(blocks) == 0 {
		return nil
	}
	calls := make([]agentcore.AgentToolCall, len(blocks))
	for i, b := range blocks {
		calls[i] = agentcore.AgentToolCall{ID: b.ID, Name: b.Name, Arguments: b.Arguments}
	}
	return calls
}
