// This file implements the Dispatcher: for one event it matches the configured
// hooks, runs them in order via the Runner, and merges their outputs into a
// single HookDecision. It owns the fail-open policy (a hook that fails to run
// is warned about and skipped, never blocking the agent) and the PreToolUse
// short-circuit (the first block stops further hooks so a blocked call is not
// also rewritten).
package hooks

import (
	"context"
	"io"
	"strings"
)

// EventPreToolUse is the one event whose first block short-circuits the rest of
// the chain. Declared here (rather than importing a shared constants file) so
// the hooks package stays a leaf; the string must match what callers dispatch.
const EventPreToolUse = "PreToolUse"

// Dispatcher runs the hooks configured for an event and merges their results.
// A nil *Dispatcher is a valid no-op (Dispatch returns an empty decision), so
// callers can hold a possibly-nil dispatcher without guarding every call.
type Dispatcher struct {
	set     HookSet
	runner  *Runner
	warnLog io.Writer
}

// NewDispatcher builds a dispatcher over the given hook set. It returns nil when
// the set is empty, so the common no-hooks case costs nothing and callers can
// treat nil as "hooks disabled" (FR-18). projectDir is where hook commands run;
// warnLog receives isolation warnings (may be nil).
func NewDispatcher(set HookSet, projectDir string, warnLog io.Writer) *Dispatcher {
	if len(set) == 0 {
		return nil
	}
	return &Dispatcher{
		set:     set,
		runner:  &Runner{ProjectDir: projectDir, WarnLog: warnLog},
		warnLog: warnLog,
	}
}

// Dispatch runs every hook matching (eventType, toolName) in order and returns
// the merged decision. On a nil dispatcher or no matched hooks it returns the
// zero HookDecision. A hook that fails to run is warned about and skipped
// (fail-open, FR-15). For PreToolUse the first block stops the chain so a
// blocked call is not subsequently rewritten.
func (d *Dispatcher) Dispatch(ctx context.Context, eventType, toolName string, input HookInput) HookDecision {
	var dec HookDecision
	if d == nil {
		return dec
	}
	matched := d.set.MatchHooks(eventType, toolName, d.warnLog)
	for _, h := range matched {
		out, err := d.runner.Run(ctx, h, input)
		if err != nil {
			warnf(d.warnLog, "pigo: hooks: %s: %v\n", eventType, err)
			continue // fail-open
		}
		if out.blocks() {
			dec.Block = true
			dec.Reason = joinNonEmpty(dec.Reason, out.Reason, "\n")
		}
		if out.AdditionalContext != "" {
			dec.AdditionalContext = joinNonEmpty(dec.AdditionalContext, out.AdditionalContext, "\n")
		}
		if len(out.UpdatedInput) > 0 {
			dec.UpdatedInput = out.UpdatedInput // last writer wins (§5.4)
		}
		if dec.Block && eventType == EventPreToolUse {
			break // blocked tool call is not also rewritten
		}
	}
	return dec
}

// joinNonEmpty joins a and b with sep, dropping empty operands so the result
// never has a leading or dangling separator.
func joinNonEmpty(a, b, sep string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + sep + b
	}
}
