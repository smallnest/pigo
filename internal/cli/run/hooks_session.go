// This file provides the cli-layer helper that runs the SessionStart hook event
// (US-010, FR-1) synchronously at the run-start seam. It lives in the cli layer
// alongside the other hook assembly (hooks_install.go) because it bridges the
// resolved Dispatcher and the runtime.RunConfig, which runtime itself must not
// know about.
//
// SessionStart is dispatched SYNCHRONOUSLY at run start rather than through the
// async OnEvent notifier: an async dispatch could land after the first turn's
// request is built, so its additionalContext would miss the first turn (SPEC
// §11.2). Dispatching inline before the loop starts guarantees the injected
// context is present for the very first turn.
//
// Only injection is supported (there is nothing to block at session start): any
// additionalContext is registered as a ONE-SHOT reminder on cfg.Reminders so the
// text is injected into the first turn's provider context via the existing
// TransformContext seam, then never again. It is not written to the persisted
// message history (the reminder mechanism is ephemeral by construction).
package run

import (
	"context"

	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// DispatchSessionStart runs the SessionStart event once at run start. source is
// "startup" for a fresh run or "resume" when continuing an existing session; it
// is carried in the HookInput so hooks can differentiate. Any additionalContext
// returned by the hook is registered as a one-shot reminder on cfg.Reminders
// (allocating a registry when cfg.Reminders is nil) so it is injected into the
// first turn only.
//
// A nil dispatcher (no hooks configured) is a no-op.
func DispatchSessionStart(ctx context.Context, d *hooks.Dispatcher, cfg *runtime.RunConfig, deps HookDeps, source string) {
	if d == nil {
		return
	}
	dec := d.Dispatch(ctx, "SessionStart", "", hooks.HookInput{
		EventType:  "SessionStart",
		SessionID:  deps.SessionID,
		ProjectDir: deps.ProjectDir,
		Source:     source,
	})
	if dec.AdditionalContext != "" && cfg != nil {
		if cfg.Reminders == nil {
			cfg.Reminders = runtime.NewReminderRegistry()
		}
		cfg.Reminders.Register(runtime.NewOneShotReminder("session-start", dec.AdditionalContext))
	}
}
