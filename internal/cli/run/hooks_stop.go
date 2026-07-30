// This file provides the cli-layer decorator that runs the Stop and SubagentStop
// hook events (US-008/009, FR-10/12) at the loop's natural-end seam
// (runtime.RunConfig.OnStop). It lives in the cli layer because it bridges the
// resolved Dispatcher and runtime, which must not depend on hooks.
//
// A Stop hook may block the run from ending and force a continuation, feeding
// its reason back as guidance. Left unchecked a hook that always blocks would
// loop forever, so the decorator holds a per-run consecutive-block counter and
// force-stops after maxConsecutiveStopBlocks (FR-12). The counter resets on any
// natural (non-blocking) stop, so an occasional block does not erode the budget.
package run

import (
	"context"
	"fmt"
	"os"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// maxConsecutiveStopBlocks is the default FR-12 ceiling on how many times a Stop
// (or SubagentStop) hook may consecutively block the run from ending before the
// decorator forces a stop and warns. Overridable via the builder.
const maxConsecutiveStopBlocks = 5

// stopHook builds a runtime.OnStop seam that dispatches the given event
// ("Stop" for a top-level run, "SubagentStop" inside a sub-agent) each time the
// loop is about to end. On a blocking decision it returns a StopDecision that
// keeps the run alive with the hook's reason as guidance, up to maxBlocks
// consecutive blocks; past that it forces a stop and warns. maxBlocks <= 0 uses
// maxConsecutiveStopBlocks. A nil dispatcher yields a nil seam (no wrapping).
func stopHook(d *hooks.Dispatcher, deps HookDeps, event string, maxBlocks int) func(context.Context, *agentcore.AgentContext) *runtime.StopDecision {
	if d == nil {
		return nil
	}
	if maxBlocks <= 0 {
		maxBlocks = maxConsecutiveStopBlocks
	}
	warn := deps.WarnLog
	if warn == nil {
		warn = os.Stderr
	}
	consecutive := 0
	return func(ctx context.Context, _ *agentcore.AgentContext) *runtime.StopDecision {
		dec := d.Dispatch(ctx, event, "", hooks.HookInput{
			EventType:  event,
			SessionID:  deps.SessionID,
			ProjectDir: deps.ProjectDir,
			StopReason: "end_turn",
		})
		if !dec.Block {
			consecutive = 0
			return nil
		}
		consecutive++
		if consecutive > maxBlocks {
			fmt.Fprintf(warn, "%s hook blocked the run %d times consecutively; forcing stop\n", event, consecutive-1)
			consecutive = 0
			return nil
		}
		return &runtime.StopDecision{Block: true, Guidance: hookReason(dec.Reason, event)}
	}
}

// InstallSubagentStop wires the SubagentStop hook onto a sub-agent's RunConfig,
// so a SubagentStop hook can block a sub-agent from ending (same semantics as
// Stop, evaluated in the sub-agent context, with its own consecutive-block
// budget). The sub-agent assembly calls this on the child runCfg (converged in
// #425). A nil dispatcher is a no-op.
func InstallSubagentStop(cfg *runtime.RunConfig, d *hooks.Dispatcher, deps HookDeps) {
	installStopHook(cfg, d, deps, "SubagentStop")
}

// installStopHook wires a Stop-family decorator onto cfg.OnStop, chaining onto
// any existing seam (an earlier OnStop is consulted first and its block wins,
// mirroring the block-short-circuit combinator convention). It is called by the
// tool-execution wiring for the top-level run and by the sub-agent assembly for
// the SubagentStop variant.
func installStopHook(cfg *runtime.RunConfig, d *hooks.Dispatcher, deps HookDeps, event string) {
	next := stopHook(d, deps, event, 0)
	if next == nil {
		return
	}
	cfg.OnStop = chainOnStop(cfg.OnStop, next)
}

// chainOnStop composes two OnStop seams: prev is consulted first and a blocking
// decision short-circuits (next does not run), so an earlier gate stays
// authoritative. A nil operand is identity.
func chainOnStop(prev, next func(context.Context, *agentcore.AgentContext) *runtime.StopDecision) func(context.Context, *agentcore.AgentContext) *runtime.StopDecision {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ctx context.Context, agentCtx *agentcore.AgentContext) *runtime.StopDecision {
		if dec := prev(ctx, agentCtx); dec != nil && dec.Block {
			return dec
		}
		return next(ctx, agentCtx)
	}
}
