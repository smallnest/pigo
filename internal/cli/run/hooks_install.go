// This file provides the single cli-layer assembly helper that composes hook
// dispatch into a runtime.RunConfig. It lives in the cli layer (not runtime) to
// avoid a runtime→hooks→runtime import cycle: runtime stays hook-agnostic and
// only exposes the generic seams (BeforeToolCall/AfterToolCall/ShouldStopAfterTurn),
// while this helper knows about both the resolved Config.Hooks and the seams.
//
// #419 establishes the skeleton: InstallHooks builds a Dispatcher (or short-
// circuits to nil when no hooks are configured, FR-18) and offers generic
// decorator combinators for the seams. The concrete per-event wiring is filled
// in by later issues (#420–#424); this file deliberately wires no event yet.
package run

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// HookDeps carries the run-scoped context a Dispatcher needs: the session id and
// project directory that populate each HookInput / the hook process environment,
// and the writer that receives isolation warnings. WarnLog may be nil (defaults
// to os.Stderr), matching the plugin.EventNotifier convention.
type HookDeps struct {
	SessionID  string
	ProjectDir string
	WarnLog    io.Writer
}

// InstallHooks builds the Dispatcher for a run from the resolved hook set and
// wires the tool-execution hook points into cfg. It short-circuits when no hooks
// are configured: NewDispatcher returns nil for an empty set, so the hot path
// pays nothing (FR-18) and nothing is wrapped. The returned dispatcher is used
// by later per-event wiring (#421–#424).
//
// PreToolUse is CHAINED onto the existing BeforeToolCall seam (occupied by the
// trust gate) rather than replacing it: trust runs first and stays authoritative
// (a trust block short-circuits before the user hook runs). PostToolUse is
// chained onto AfterToolCall as a last-writer so it can append feedback to an
// already-executed tool's result without undoing it.
func InstallHooks(cfg *runtime.RunConfig, set hooks.HookSet, deps HookDeps) *hooks.Dispatcher {
	warn := deps.WarnLog
	if warn == nil {
		warn = os.Stderr
	}
	d := hooks.NewDispatcher(set, deps.ProjectDir, warn)
	if d == nil {
		return nil
	}
	InstallSeams(cfg, d, deps)
	return d
}

// BuildDispatcher builds the run's Dispatcher from the resolved hook set without
// touching a RunConfig. It is the entry point for the multi-turn drivers (REPL /
// TUI) that resolve hooks and fire SessionStart once per session, then install
// the per-turn seams (InstallSeams) on each turn's freshly-built RunConfig. It
// returns nil for an empty set (FR-18), so callers gate all hook work on non-nil.
func BuildDispatcher(set hooks.HookSet, deps HookDeps) *hooks.Dispatcher {
	warn := deps.WarnLog
	if warn == nil {
		warn = os.Stderr
	}
	return hooks.NewDispatcher(set, deps.ProjectDir, warn)
}

// InstallSeams wires the tool-execution and Stop hook points onto cfg from an
// already-built dispatcher. It is the shared body of InstallHooks and the
// per-turn install path for multi-turn drivers. A nil dispatcher is a no-op, so
// callers can invoke it unconditionally.
//
// PreToolUse is CHAINED onto the existing BeforeToolCall seam (occupied by the
// trust gate) rather than replacing it: trust runs first and stays authoritative
// (a trust block short-circuits before the user hook runs). PostToolUse is
// chained onto AfterToolCall as a last-writer so it can append feedback to an
// already-executed tool's result without undoing it. The Stop hook is chained
// onto the loop's natural-end seam (FR-10), bounded by the decorator's
// consecutive-block counter (FR-12).
func InstallSeams(cfg *runtime.RunConfig, d *hooks.Dispatcher, deps HookDeps) {
	if d == nil {
		return
	}
	tec := &cfg.Batch.ToolExecutorConfig
	tec.BeforeToolCall = chainBeforeToolCall(tec.BeforeToolCall, preToolCallHook(d, deps))
	tec.AfterToolCall = chainAfterToolCall(tec.AfterToolCall, postToolCallHook(d, deps))
	installStopHook(cfg, d, deps, "Stop")
}

// preToolCallHook adapts the dispatcher's PreToolUse event to the BeforeToolCall
// seam. It dispatches with the tool name and raw arguments; a block becomes a
// blocking decision whose reason is surfaced as the tool's error result, and an
// updatedInput becomes an argument rewrite (re-validated by the executor).
func preToolCallHook(d *hooks.Dispatcher, deps HookDeps) agentcore.BeforeToolCallFunc {
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		dec := d.Dispatch(ctx, hooks.EventPreToolUse, call.Name, hooks.HookInput{
			EventType:  hooks.EventPreToolUse,
			SessionID:  deps.SessionID,
			ProjectDir: deps.ProjectDir,
			ToolName:   call.Name,
			ToolInput:  call.Arguments,
		})
		if dec.Block {
			content := agentcore.ContentList{agentcore.NewTextContent(hookReason(dec.Reason, call.Name))}
			return &agentcore.BeforeToolCallDecision{Block: true, Content: &content}
		}
		if len(dec.UpdatedInput) > 0 {
			return &agentcore.BeforeToolCallDecision{UpdatedInput: dec.UpdatedInput}
		}
		return nil
	}
}

// postToolCallHook adapts the dispatcher's PostToolUse event to the AfterToolCall
// seam. It dispatches with the tool name, raw arguments, and the tool's response,
// then appends any reason/additionalContext to the result content as a new text
// block (the executed tool is never undone). A block on Post is treated as
// feedback only: it cannot retract an already-run tool, so we surface the reason.
func postToolCallHook(d *hooks.Dispatcher, deps HookDeps) agentcore.AfterToolCallFunc {
	return func(ctx context.Context, call agentcore.AgentToolCall, result agentcore.AgentToolResult, isError bool) *agentcore.AfterToolCallResult {
		var resp json.RawMessage
		if b, err := json.Marshal(result.Content); err == nil {
			resp = b
		}
		dec := d.Dispatch(ctx, "PostToolUse", call.Name, hooks.HookInput{
			EventType:    "PostToolUse",
			SessionID:    deps.SessionID,
			ProjectDir:   deps.ProjectDir,
			ToolName:     call.Name,
			ToolInput:    call.Arguments,
			ToolResponse: resp,
		})
		feedback := joinHookText(dec.Reason, dec.AdditionalContext)
		if feedback == "" {
			return nil
		}
		content := append(agentcore.ContentList{}, result.Content...)
		content = append(content, agentcore.NewTextContent(feedback))
		return &agentcore.AfterToolCallResult{Content: &content}
	}
}

// hookReason returns the block reason, falling back to a generic message keyed on
// the tool name when the hook gave no reason.
func hookReason(reason, toolName string) string {
	if reason != "" {
		return reason
	}
	return "tool " + toolName + " blocked by PreToolUse hook"
}

// joinHookText joins two hook text fields with a newline, dropping empties.
func joinHookText(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
}

// chainBeforeToolCall composes two BeforeToolCall seams into one that runs prev
// first, then next. A blocking decision from prev short-circuits (next does not
// run), so an earlier gate (e.g. trust) is authoritative over a later hook. A
// nil operand is treated as identity, so composing onto an unset seam returns
// the other unchanged.
func chainBeforeToolCall(prev, next agentcore.BeforeToolCallFunc) agentcore.BeforeToolCallFunc {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if dec := prev(ctx, call); dec != nil && dec.Block {
			return dec
		}
		return next(ctx, call)
	}
}

// chainAfterToolCall composes two AfterToolCall seams into one that runs prev
// first, then next. next's non-nil result wins (last writer), so a hook layered
// after an existing seam can override it; when next returns nil, prev's result
// is preserved. A nil operand is identity.
func chainAfterToolCall(prev, next agentcore.AfterToolCallFunc) agentcore.AfterToolCallFunc {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ctx context.Context, call agentcore.AgentToolCall, result agentcore.AgentToolResult, isError bool) *agentcore.AfterToolCallResult {
		prevRes := prev(ctx, call, result, isError)
		if nextRes := next(ctx, call, result, isError); nextRes != nil {
			return nextRes
		}
		return prevRes
	}
}

// chainShouldStop composes two ShouldStopAfterTurn seams with OR semantics: the
// run stops after a turn if either predicate says so. prev runs first and
// short-circuits when true. A nil operand is identity.
func chainShouldStop(prev, next func(context.Context, *agentcore.AgentContext) bool) func(context.Context, *agentcore.AgentContext) bool {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ctx context.Context, agentCtx *agentcore.AgentContext) bool {
		if prev(ctx, agentCtx) {
			return true
		}
		return next(ctx, agentCtx)
	}
}
