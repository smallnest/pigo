package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// captureNotifier builds a HookNotifier whose hook for `event` appends its stdin
// payload to a file, returning the notifier and the capture-file path so a test
// can assert on the payload the hook received.
func captureNotifier(t *testing.T, event string) (*HookNotifier, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "capture.json")
	set := HookSet{
		event: {{Matcher: "*", Hooks: []HookConfig{{Command: "cat >> " + out}}}},
	}
	d := NewDispatcher(set, dir, nil)
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	return NewHookNotifier(d, "sess-1", dir), out
}

func readCapture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("capture not written: %v", err)
	}
	return string(b)
}

// TestSessionEndNaturalReason: an end_turn terminal message yields reason
// "natural"; an aborted terminal message yields "aborted".
func TestSessionEndReason(t *testing.T) {
	cases := []struct {
		name   string
		stop   string
		expect string
	}{
		{"natural", agentcore.StopReasonEndTurn, `"stop_reason":"natural"`},
		{"aborted", agentcore.StopReasonAborted, `"stop_reason":"aborted"`},
		{"error", agentcore.StopReasonError, `"stop_reason":"error"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, out := captureNotifier(t, "SessionEnd")
			n.Handle(agentcore.AgentEndEvent{Messages: []agentcore.AgentMessage{
				agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: tc.stop},
			}})
			if got := readCapture(t, out); !strings.Contains(got, tc.expect) {
				t.Fatalf("SessionEnd reason: want %q in %q", tc.expect, got)
			}
		})
	}
}

// TestPreCompactTrigger: a manual CompactionEvent maps to trigger "manual";
// threshold/overflow map to "auto".
func TestPreCompactTrigger(t *testing.T) {
	cases := []struct {
		reason string
		expect string
	}{
		{"manual", `"trigger":"manual"`},
		{"threshold", `"trigger":"auto"`},
		{"overflow", `"trigger":"auto"`},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			n, out := captureNotifier(t, "PreCompact")
			n.Handle(agentcore.CompactionEvent{Reason: tc.reason})
			if got := readCapture(t, out); !strings.Contains(got, tc.expect) {
				t.Fatalf("PreCompact trigger: want %q in %q", tc.expect, got)
			}
		})
	}
}

// TestNotification: Notify fires the Notification event carrying the message.
func TestNotification(t *testing.T) {
	n, out := captureNotifier(t, "Notification")
	n.Notify("approve bash in untrusted dir?")
	got := readCapture(t, out)
	if !strings.Contains(got, `"event_type":"Notification"`) || !strings.Contains(got, "approve bash in untrusted dir?") {
		t.Fatalf("Notification payload missing message, got %q", got)
	}
}

// TestNotifierNilSafe: a nil dispatcher yields a nil notifier whose methods are
// safe no-ops; an empty Notify message is dropped.
func TestNotifierNilSafe(t *testing.T) {
	if n := NewHookNotifier(nil, "s", "d"); n != nil {
		t.Fatal("nil dispatcher must yield a nil notifier")
	}
	var n *HookNotifier
	n.Handle(agentcore.AgentEndEvent{})
	n.Notify("x") // must not panic

	// A live notifier with an empty message writes nothing.
	live, out := captureNotifier(t, "Notification")
	live.Notify("")
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("empty Notify message must not fire the hook")
	}
}

// TestNotifierIgnoresUnmappedEvents: turn/message/tool events have no observer
// mapping and must not fire any hook.
func TestNotifierIgnoresUnmappedEvents(t *testing.T) {
	n, out := captureNotifier(t, "SessionEnd")
	n.Handle(agentcore.TurnStartEvent{})
	n.Handle(agentcore.ToolExecutionStartEvent{ToolName: "bash"})
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("unmapped events must not fire the SessionEnd hook")
	}
}
