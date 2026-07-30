// This file provides the cli-layer helper that runs the UserPromptSubmit hook
// event (US-007, FR-9) at the two prompt entry points (REPL + headless) before a
// prompt is submitted to the loop. It lives in the cli layer alongside the other
// hook assembly (hooks_install.go) because it bridges the resolved Dispatcher and
// the runtime.RunConfig, which runtime itself must not know about.
//
// Two hook effects are supported, with block taking priority over injection:
//
//   - block (decision=block / exit 2): the prompt is NOT submitted. The caller
//     decides how to surface the reason — the REPL returns to its input state and
//     shows it, the headless driver exits non-zero. DispatchUserPromptSubmit only
//     reports (block, reason); it does not itself abort.
//   - additionalContext: registered as a ONE-SHOT reminder on cfg.Reminders so the
//     text is injected into THIS turn's provider context via the existing
//     TransformContext seam, then never again. It is not written to the persisted
//     message history (the reminder mechanism is ephemeral by construction).
package run

import (
	"context"

	"github.com/smallnest/pigo/internal/hooks"
	"github.com/smallnest/pigo/internal/runtime"
)

// DispatchUserPromptSubmit runs the UserPromptSubmit event for a prompt about to
// be submitted. It returns (block, reason): when block is true the caller must
// NOT submit the prompt and should surface reason. When block is false and the
// hook returned additionalContext, that context is registered as a one-shot
// reminder on cfg.Reminders (allocating a registry when cfg.Reminders is nil) so
// it is injected into this turn only. Block takes priority: a blocking decision
// never also injects.
//
// A nil dispatcher (no hooks configured) is a no-op that returns (false, "").
func DispatchUserPromptSubmit(ctx context.Context, d *hooks.Dispatcher, cfg *runtime.RunConfig, deps HookDeps, prompt string) (block bool, reason string) {
	if d == nil {
		return false, ""
	}
	dec := d.Dispatch(ctx, "UserPromptSubmit", "", hooks.HookInput{
		EventType:  "UserPromptSubmit",
		SessionID:  deps.SessionID,
		ProjectDir: deps.ProjectDir,
		Prompt:     prompt,
	})
	if dec.Block {
		return true, hookReason(dec.Reason, "UserPromptSubmit")
	}
	if dec.AdditionalContext != "" && cfg != nil {
		if cfg.Reminders == nil {
			cfg.Reminders = runtime.NewReminderRegistry()
		}
		cfg.Reminders.Register(runtime.NewOneShotReminder("user-prompt-submit", dec.AdditionalContext))
	}
	return false, ""
}
