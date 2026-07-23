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
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// toolResultMaxBytes is the executor-layer budget for a single tool result's
// combined text, applied uniformly to EVERY tool right before its result enters
// the AgentToolResult / message list. Individual tools also impose their own,
// stricter inner caps (read: readToolMaxLines, search: searchMaxResults,
// webfetch: webFetchMaxBytes, bash: bashMaxOutputBytes); those still run first
// and clip a tool below this outer budget. This budget is the last line of
// defense so a tool with no (or a looser) inner cap cannot blow the model's
// context. Override per-executor via ToolExecutorConfig.MaxResultBytes.
const toolResultMaxBytes = 100_000

// ToolExecutorConfig holds the registry and the optional per-phase hooks. Every
// hook is optional (nil = default behavior).
type ToolExecutorConfig struct {
	Registry         *ToolRegistry
	PrepareArguments agentcore.PrepareArgumentsFunc
	BeforeToolCall   agentcore.BeforeToolCallFunc
	AfterToolCall    agentcore.AfterToolCallFunc
	// MaxResultBytes overrides the executor-layer per-result text budget. Zero
	// (the default) uses toolResultMaxBytes; a negative value disables the
	// budget entirely.
	MaxResultBytes int
}

// executeToolCall runs one tool call through prepare → execute → finalize and
// returns the resulting ToolResultMessage plus whether the batch should
// terminate. emit may be nil (no events). It never returns a Go error: every
// failure is encoded into the returned message with IsError=true.
func executeToolCall(ctx context.Context, cfg ToolExecutorConfig, call agentcore.AgentToolCall, emit agentcore.EmitFunc) (agentcore.ToolResultMessage, bool) {
	// 1. prepare: lookup, prepareArguments, validate, beforeToolCall.
	tool, args, prep, isError := prepareToolCall(ctx, cfg, call)
	if prep != nil {
		// Prepare short-circuited (unknown tool / prepare error / validation /
		// block / abort): finalize the error result without executing.
		return finalizeToolCall(ctx, cfg, call, *prep, isError, emit)
	}

	// 2. execute.
	if emit != nil {
		if err := emit(ctx, agentcore.ToolExecutionStartEvent{ToolCallID: call.ID, ToolName: call.Name, Args: args}); err != nil {
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
func prepareToolCall(ctx context.Context, cfg ToolExecutorConfig, call agentcore.AgentToolCall) (agentcore.AgentTool, json.RawMessage, *agentcore.AgentToolResult, bool) {
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
		if dec := cfg.BeforeToolCall(ctx, agentcore.AgentToolCall{ID: call.ID, Name: call.Name, Arguments: args}); dec != nil && dec.Block {
			r := agentcore.AgentToolResult{}
			if dec.Content != nil {
				r.Content = *dec.Content
			} else {
				r.Content = agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf("tool %q blocked by beforeToolCall", call.Name))}
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
func runTool(ctx context.Context, tool agentcore.AgentTool, call agentcore.AgentToolCall, args json.RawMessage, emit agentcore.EmitFunc) (result agentcore.AgentToolResult, isError bool) {
	defer func() {
		if r := recover(); r != nil {
			result = errorResult(fmt.Sprintf("tool %q panicked: %v", call.Name, r))
			isError = true
		}
	}()

	onUpdate := func(partial agentcore.AgentToolResult) {
		if emit == nil {
			return
		}
		_ = emit(ctx, agentcore.ToolExecutionUpdateEvent{ToolCallID: call.ID, ToolName: call.Name, PartialResult: partial})
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
func finalizeToolCall(ctx context.Context, cfg ToolExecutorConfig, call agentcore.AgentToolCall, result agentcore.AgentToolResult, isError bool, emit agentcore.EmitFunc) (agentcore.ToolResultMessage, bool) {
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

	// Result-shaping seam: every tool's output funnels through here before it
	// becomes a ToolResultMessage, so this is the single point where the
	// executor-layer byte budget is enforced uniformly for ALL tools.
	result.Content = clipToolResultContent(result.Content, cfg.MaxResultBytes)

	if emit != nil {
		_ = emit(ctx, agentcore.ToolExecutionEndEvent{ToolCallID: call.ID, ToolName: call.Name, Result: result, IsError: isError})
	}

	terminate := result.Terminate != nil && *result.Terminate
	return agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    result.Content,
		Details:    result.Details,
		IsError:    isError,
	}, terminate
}

// errorResult builds an error AgentToolResult carrying a single text block.
func errorResult(msg string) agentcore.AgentToolResult {
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(msg)}}
}

// clipToolResultContent enforces the executor-layer byte budget on a tool
// result's text, uniformly for every tool. budget<=0 with the sentinel meaning:
// 0 => toolResultMaxBytes default, <0 => disabled. Non-text blocks (e.g. images)
// pass through untouched and keep their order; the combined text of all text
// blocks is measured against the budget and, when over, collapsed into a single
// truncated text block via truncateToBudget (head + "[truncated N bytes]" +
// tail, matching the bash idiom). Per-tool inner caps have already run, so this
// only bites when a tool's own cap is looser or absent.
func clipToolResultContent(content agentcore.ContentList, cfgMax int) agentcore.ContentList {
	budget := cfgMax
	if budget == 0 {
		budget = toolResultMaxBytes
	}
	if budget < 0 {
		return content
	}

	total := 0
	textBlocks := 0
	for _, c := range content {
		if t, ok := c.(agentcore.TextContent); ok {
			total += len(t.Text)
			textBlocks++
		}
	}
	if textBlocks == 0 || total <= budget {
		return content
	}

	// Over budget: gather all text (in order) and non-text blocks separately,
	// then emit the non-text blocks followed by one truncated text block.
	var sb strings.Builder
	out := make(agentcore.ContentList, 0, len(content))
	for _, c := range content {
		if t, ok := c.(agentcore.TextContent); ok {
			sb.WriteString(t.Text)
			continue
		}
		out = append(out, c)
	}
	out = append(out, agentcore.NewTextContent(truncateToBudget(sb.String(), budget)))
	return out
}

// truncateToBudget caps s at budget bytes. When s is longer it keeps a head and
// a tail preview (split evenly) joined by a "[truncated N bytes]" marker, so
// both the start and the end of the text survive. Cut points are pulled back to
// UTF-8 rune boundaries so no partial rune is emitted; N counts the raw bytes
// dropped from the middle. This is the single shared truncation idiom reused by
// both the bash tool's inner cap and the executor-layer budget.
func truncateToBudget(s string, budget int) string {
	if budget <= 0 || len(s) <= budget {
		return s
	}
	half := budget / 2
	head := trimUTF8Prefix(s[:half])
	tail := trimUTF8Suffix(s[len(s)-half:])
	removed := len(s) - len(head) - len(tail)
	return head + fmt.Sprintf("\n[truncated %d bytes]\n", removed) + tail
}

// decodeArgs unmarshals a tool's JSON arguments into T. On failure it returns an
// error result already shaped as "<tool>: invalid arguments: ...", so a tool's
// Execute can decode and bail in one line:
//
//	a, bad := decodeArgs[readToolArgs](args, "read")
//	if bad != nil {
//		return *bad, nil
//	}
//
// The ok flag distinguishes the failure case without comparing the zero value.
func decodeArgs[T any](args json.RawMessage, tool string) (T, *agentcore.AgentToolResult) {
	var a T
	if err := json.Unmarshal(args, &a); err != nil {
		res := errorResult(fmt.Sprintf("%s: invalid arguments: %v", tool, err))
		return a, &res
	}
	return a, nil
}

// errorToolResult builds an error ToolResultMessage directly (used when a call
// is aborted outside the normal finalize path).
func errorToolResult(call agentcore.AgentToolCall, msg string) agentcore.ToolResultMessage {
	return agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    agentcore.ContentList{agentcore.NewTextContent(msg)},
		IsError:    true,
	}
}
