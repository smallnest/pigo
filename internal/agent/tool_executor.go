// This file implements the three-phase tool execution (US-004): prepare →
// execute → finalize, with the beforeToolCall / afterToolCall hooks. It mirrors
// pi's agent-loop tool handling: a tool call is looked up in the registry, its
// arguments are (optionally) prepared and schema-validated, the beforeToolCall
// hook may block it, the tool runs (streaming partial updates), and the
// afterToolCall hook may override the result field-by-field (no deep merge).
//
// Every failure mode (unknown tool, validation failure, block, abort, tool
// error/panic) is turned into an error tool result rather than a Go error, so
// the loop always has a ToolResultMessage to feed back to the model.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// PrepareArgumentsFunc optionally rewrites a tool's raw arguments before schema
// validation (e.g. injecting defaults). An error aborts the call with an error
// result. Optional (nil = identity).
type PrepareArgumentsFunc func(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error)

// BeforeToolCallDecision is the optional result of the beforeToolCall hook. When
// Block is true the tool is not executed and an error result is produced;
// Content/Details override the default block message when set.
type BeforeToolCallDecision struct {
	Block   bool
	Content *ContentList
	Details *any
}

// BeforeToolCallFunc runs after validation and may block the call (permission /
// sandbox checks, FR-4/FR-26). Returning nil allows the call. Optional.
type BeforeToolCallFunc func(ctx context.Context, call AgentToolCall) *BeforeToolCallDecision

// AfterToolCallFunc runs after execution and may override the result
// field-by-field via AfterToolCallResult (FR-5, no deep merge). Optional.
type AfterToolCallFunc func(ctx context.Context, call AgentToolCall, result AgentToolResult, isError bool) *AfterToolCallResult

// ToolExecutorConfig holds the registry and the optional per-phase hooks. Every
// hook is optional (nil = default behavior).
type ToolExecutorConfig struct {
	Registry         *ToolRegistry
	PrepareArguments PrepareArgumentsFunc
	BeforeToolCall   BeforeToolCallFunc
	AfterToolCall    AfterToolCallFunc
}

// executeToolCall runs one tool call through prepare → execute → finalize and
// returns the resulting ToolResultMessage plus whether the batch should
// terminate. emit may be nil (no events). It never returns a Go error: every
// failure is encoded into the returned message with IsError=true.
func executeToolCall(ctx context.Context, cfg ToolExecutorConfig, call AgentToolCall, emit emitFunc) (ToolResultMessage, bool) {
	// 1. prepare: lookup, prepareArguments, validate, beforeToolCall.
	tool, args, prep, isError := prepareToolCall(ctx, cfg, call)
	if prep != nil {
		// Prepare short-circuited (unknown tool / prepare error / validation /
		// block / abort): finalize the error result without executing.
		return finalizeToolCall(ctx, cfg, call, *prep, isError, emit)
	}

	// 2. execute.
	if emit != nil {
		if err := emit(ctx, ToolExecutionStartEvent{ToolCallID: call.ID, ToolName: call.Name, Args: args}); err != nil {
			return errorToolResult(call, "aborted before execution: "+err.Error()), false
		}
	}
	result, isError := runTool(ctx, tool, call, args, emit)

	// 3. finalize: afterToolCall overrides.
	return finalizeToolCall(ctx, cfg, call, result, isError, emit)
}

// prepareToolCall performs the prepare phase. On success it returns the tool and
// the (possibly rewritten) arguments with a nil result. On any short-circuit it
// returns a non-nil *AgentToolResult and the isError flag.
func prepareToolCall(ctx context.Context, cfg ToolExecutorConfig, call AgentToolCall) (AgentTool, json.RawMessage, *AgentToolResult, bool) {
	if ctx.Err() != nil {
		r := errorResult(fmt.Sprintf("tool %q aborted before execution", call.Name))
		return nil, nil, &r, true
	}

	// Registry lookup.
	tool, ok := cfg.Registry.Get(call.Name)
	if !ok {
		r := errorResult(fmt.Sprintf("unknown tool %q", call.Name))
		return nil, nil, &r, true
	}

	// prepareArguments (optional).
	args := call.Arguments
	if cfg.PrepareArguments != nil {
		prepared, err := cfg.PrepareArguments(ctx, call.Name, args)
		if err != nil {
			r := errorResult(fmt.Sprintf("prepareArguments for %q failed: %v", call.Name, err))
			return nil, nil, &r, true
		}
		args = prepared
	}

	// JSON Schema validation.
	if errs := cfg.Registry.Validate(call.Name, args); len(errs) > 0 {
		r := ValidationErrorResult(call.Name, errs)
		return nil, nil, &r, true
	}

	// beforeToolCall hook (may block).
	if cfg.BeforeToolCall != nil {
		if dec := cfg.BeforeToolCall(ctx, AgentToolCall{ID: call.ID, Name: call.Name, Arguments: args}); dec != nil && dec.Block {
			r := AgentToolResult{}
			if dec.Content != nil {
				r.Content = *dec.Content
			} else {
				r.Content = ContentList{NewTextContent(fmt.Sprintf("tool %q blocked by beforeToolCall", call.Name))}
			}
			if dec.Details != nil {
				r.Details = *dec.Details
			}
			return nil, nil, &r, true
		}
	}

	return tool, args, nil, false
}

// runTool executes the tool, converting a returned error or a panic into an
// error result instead of propagating it (FR: never panic).
func runTool(ctx context.Context, tool AgentTool, call AgentToolCall, args json.RawMessage, emit emitFunc) (result AgentToolResult, isError bool) {
	defer func() {
		if r := recover(); r != nil {
			result = errorResult(fmt.Sprintf("tool %q panicked: %v", call.Name, r))
			isError = true
		}
	}()

	onUpdate := func(partial AgentToolResult) {
		if emit == nil {
			return
		}
		_ = emit(ctx, ToolExecutionUpdateEvent{ToolCallID: call.ID, ToolName: call.Name, PartialResult: partial})
	}

	res, err := tool.Execute(ctx, call.ID, args, onUpdate)
	if err != nil {
		return errorResult(fmt.Sprintf("tool %q failed: %v", call.Name, err)), true
	}
	return res, false
}

// finalizeToolCall applies the afterToolCall hook (field-level override, no deep
// merge), emits the tool_execution_end event, and builds the ToolResultMessage.
// It returns the message and whether this result requests termination.
func finalizeToolCall(ctx context.Context, cfg ToolExecutorConfig, call AgentToolCall, result AgentToolResult, isError bool, emit emitFunc) (ToolResultMessage, bool) {
	if cfg.AfterToolCall != nil {
		if ov := cfg.AfterToolCall(ctx, call, result, isError); ov != nil {
			if ov.Content != nil {
				result.Content = *ov.Content
			}
			if ov.Details != nil {
				result.Details = *ov.Details
			}
			if ov.Terminate != nil {
				result.Terminate = ov.Terminate
			}
			if ov.IsError != nil {
				isError = *ov.IsError
			}
		}
	}

	if emit != nil {
		_ = emit(ctx, ToolExecutionEndEvent{ToolCallID: call.ID, ToolName: call.Name, Result: result, IsError: isError})
	}

	terminate := result.Terminate != nil && *result.Terminate
	return ToolResultMessage{
		RoleField:  RoleToolResult,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    result.Content,
		Details:    result.Details,
		IsError:    isError,
	}, terminate
}

// errorResult builds an error AgentToolResult carrying a single text block.
func errorResult(msg string) AgentToolResult {
	return AgentToolResult{Content: ContentList{NewTextContent(msg)}}
}

// errorToolResult builds an error ToolResultMessage directly (used when a call
// is aborted outside the normal finalize path).
func errorToolResult(call AgentToolCall, msg string) ToolResultMessage {
	return ToolResultMessage{
		RoleField:  RoleToolResult,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    ContentList{NewTextContent(msg)},
		IsError:    true,
	}
}
