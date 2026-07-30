package run

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/hooks"
)

func stopDispatcher(t *testing.T, event, cmd string) *hooks.Dispatcher {
	t.Helper()
	set := hooks.HookSet{
		event: {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: cmd}}}},
	}
	d := hooks.NewDispatcher(set, t.TempDir(), nil)
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	return d
}

// TestStopHookBlocksThenForceStops: a Stop hook that always blocks (exit 2) is
// honored up to the limit, then the decorator force-stops (returns nil) so the
// run cannot loop forever (FR-12).
func TestStopHookBlocksThenForceStops(t *testing.T) {
	d := stopDispatcher(t, "Stop", `echo "not done yet" 1>&2; exit 2`)
	var warn strings.Builder
	seam := stopHook(d, HookDeps{SessionID: "s1", WarnLog: &warn}, "Stop", 3)
	if seam == nil {
		t.Fatal("expected non-nil seam")
	}

	// First 3 consultations block with the reason as guidance.
	for i := 0; i < 3; i++ {
		dec := seam(context.Background(), nil)
		if dec == nil || !dec.Block {
			t.Fatalf("consult %d: expected a blocking decision", i+1)
		}
		if !strings.Contains(dec.Guidance, "not done yet") {
			t.Fatalf("consult %d: block reason not surfaced as guidance, got %q", i+1, dec.Guidance)
		}
	}
	// 4th consultation exceeds the limit: force stop (nil) + a warning.
	if dec := seam(context.Background(), nil); dec != nil {
		t.Fatalf("expected force-stop (nil) past the limit, got %+v", dec)
	}
	if !strings.Contains(warn.String(), "forcing stop") {
		t.Fatalf("force-stop should warn, got %q", warn.String())
	}
}

// TestStopHookResetsCounterOnAllow: a non-blocking hook always returns nil, so
// the run is free to end and the consecutive-block counter never accrues.
func TestStopHookResetsCounterOnAllow(t *testing.T) {
	d := stopDispatcher(t, "Stop", `exit 0`)
	seam := stopHook(d, HookDeps{SessionID: "s1"}, "Stop", 2)
	for i := 0; i < 5; i++ {
		if dec := seam(context.Background(), nil); dec != nil {
			t.Fatalf("consult %d: a non-blocking hook must let the run end (nil), got %+v", i+1, dec)
		}
	}
}

// TestStopHookNilDispatcher: no hooks configured yields a nil seam (no wrapping).
func TestStopHookNilDispatcher(t *testing.T) {
	if seam := stopHook(nil, HookDeps{}, "Stop", 0); seam != nil {
		t.Fatal("nil dispatcher must yield a nil seam")
	}
}

// TestSubagentStopEvent: InstallSubagentStop wires a SubagentStop decorator whose
// block keeps a sub-agent running with the hook's reason.
func TestSubagentStopEvent(t *testing.T) {
	d := stopDispatcher(t, "SubagentStop", `echo "sub not done" 1>&2; exit 2`)
	seam := stopHook(d, HookDeps{SessionID: "child"}, "SubagentStop", 5)
	if seam == nil {
		t.Fatal("expected non-nil SubagentStop seam")
	}
	dec := seam(context.Background(), nil)
	if dec == nil || !dec.Block || !strings.Contains(dec.Guidance, "sub not done") {
		t.Fatalf("SubagentStop block not honored, got %+v", dec)
	}
}
