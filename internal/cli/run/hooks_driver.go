// This file provides the single convergence entry point every driver calls to
// wire hooks into its run (#425, FR-16). Before this, each of the six RunConfig
// assembly sites (repl/goal/btw/tui/headless/subagent_rpc) built the loop config
// independently, which was the main risk of a hook point being silently dropped
// in one mode but not another. Routing them all through InstallDriverHooks makes
// "which hook points a run has" a single decision rather than six.
//
// InstallDriverHooks resolves the trust-gated hook set (FR-14), installs the
// tool-execution and Stop seams via InstallHooks, dispatches SessionStart inline
// so injected context reaches turn one (#423), and chains the observer notifier
// (SessionEnd/PreCompact, #424) onto the driver's event seam. It returns the
// Dispatcher so a caller can additionally run UserPromptSubmit (prompt entry) or
// InstallSubagentStop (sub-agent), and the possibly-wrapped event handler to
// install on whatever OnEvent seam the driver owns (HeadlessConfig.OnEvent for
// headless, the DrainStream handler for the REPL/TUI).
package run

import (
	"context"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// InstallDriverHooks is the uniform hook-wiring seam for every driver. It
// installs the tool-execution + Stop hook points onto cfg, dispatches
// SessionStart (registering any additionalContext as a one-shot reminder so it
// lands in turn one), and chains the SessionEnd/PreCompact observer onto onEvent.
//
// set is the already-resolved, trust-gated hook set (see ResolveHookSet). When
// it is empty NewDispatcher returns nil, so this is a no-op that returns
// (nil, onEvent) and the hot path pays nothing (FR-18): the run behaves exactly
// as it did before hooks existed.
//
// The returned dispatcher (nil when no hooks) lets the caller wire the remaining
// prompt-scoped / sub-agent hooks. The returned handler is onEvent unchanged when
// there are no hooks, or onEvent with the notifier chained after it otherwise —
// so the driver installs one handler regardless.
func InstallDriverHooks(ctx context.Context, cfg *runtime.RunConfig, set hooks.HookSet, deps HookDeps, source string, onEvent func(agentcore.AgentEvent)) (*hooks.Dispatcher, func(agentcore.AgentEvent)) {
	d := InstallHooks(cfg, set, deps)
	if d == nil {
		return nil, onEvent
	}
	DispatchSessionStart(ctx, d, cfg, deps, source)
	n := hooks.NewHookNotifier(d, deps.SessionID, deps.ProjectDir)
	return d, chainEvent(onEvent, n.Handle)
}

// chainEvent composes two AgentEvent observers into one that calls prev then
// next. A nil operand is identity, so chaining onto an unset seam returns the
// other unchanged (and returns nil when both are nil, keeping the seam unset).
func chainEvent(prev, next func(agentcore.AgentEvent)) func(agentcore.AgentEvent) {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ev agentcore.AgentEvent) {
		prev(ev)
		next(ev)
	}
}
