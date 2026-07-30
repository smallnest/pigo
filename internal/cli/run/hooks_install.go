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
// returns it for later per-event wiring. It short-circuits when no hooks are
// configured: NewDispatcher returns nil for an empty set, so the hot path pays
// nothing (FR-18) and callers can hold the possibly-nil dispatcher safely.
//
// This skeleton does not yet attach the dispatcher to any RunConfig seam; the
// cfg parameter is accepted now so the signature is stable for #420–#424, which
// will wrap cfg's seams via the chain* combinators below.
func InstallHooks(cfg *runtime.RunConfig, set hooks.HookSet, deps HookDeps) *hooks.Dispatcher {
	warn := deps.WarnLog
	if warn == nil {
		warn = os.Stderr
	}
	return hooks.NewDispatcher(set, deps.ProjectDir, warn)
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
