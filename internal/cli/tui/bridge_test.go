package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
)

// drain collects every msg queued on ch without blocking, stopping at the first
// would-block. The bridge sends are synchronous into a buffered channel, so once
// the fake sequence has been driven the msgs are all present and this returns
// them in order.
func drain(ch chan tea.Msg) []tea.Msg {
	var out []tea.Msg
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		default:
			return out
		}
	}
}

// TestStreamHandlerConversion drives a synthetic event sequence directly through
// the StreamHandler the bridge builds and asserts each callback produces the
// matching tea.Msg, in order. It fakes the run entirely (no provider), exercising
// the callback→msg conversion + channel ordering in isolation.
func TestStreamHandlerConversion(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch)

	// A representative sequence: two text deltas, a tool start/update/end, a
	// telemetry summary, a compaction, and a turn end.
	h.OnText("Hello ")
	h.OnText("world")
	h.OnEvent(agentcore.ToolExecutionStartEvent{
		ToolCallID: "call-1",
		ToolName:   "read_file",
		Args:       map[string]any{"path": "/tmp/x"},
	})
	h.OnEvent(agentcore.ToolExecutionUpdateEvent{
		ToolCallID:    "call-1",
		ToolName:      "read_file",
		PartialResult: agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("partial")}},
	})
	h.OnEvent(agentcore.ToolExecutionEndEvent{
		ToolCallID: "call-1",
		ToolName:   "read_file",
		Result:     agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("done")}},
		IsError:    false,
	})
	h.OnEvent(agentcore.TelemetryEvent{Turns: 3})
	h.OnEvent(agentcore.CompactionEvent{Reason: "threshold"})
	h.OnTurnEnd(
		agentcore.AssistantMessage{Content: agentcore.ContentList{agentcore.NewTextContent("Hello world")}},
		[]agentcore.ToolResultMessage{{ToolCallID: "call-1"}},
	)
	// Simulate drain-done: the pump appends runEndMsg after DrainStream returns.
	ch <- runEndMsg{err: nil}

	got := drain(ch)

	if len(got) != 9 {
		t.Fatalf("expected 9 msgs, got %d: %#v", len(got), got)
	}

	if m, ok := got[0].(textDeltaMsg); !ok || m.delta != "Hello " {
		t.Errorf("msg[0] = %#v, want textDeltaMsg{delta:%q}", got[0], "Hello ")
	}
	if m, ok := got[1].(textDeltaMsg); !ok || m.delta != "world" {
		t.Errorf("msg[1] = %#v, want textDeltaMsg{delta:%q}", got[1], "world")
	}
	if m, ok := got[2].(toolStartMsg); !ok || m.id != "call-1" || m.name != "read_file" || m.input["path"] != "/tmp/x" {
		t.Errorf("msg[2] = %#v, want toolStartMsg for call-1", got[2])
	}
	if m, ok := got[3].(toolUpdateMsg); !ok || m.id != "call-1" || m.partial != "partial" {
		t.Errorf("msg[3] = %#v, want toolUpdateMsg{partial:%q}", got[3], "partial")
	}
	if m, ok := got[4].(toolEndMsg); !ok || m.id != "call-1" || !m.ok || m.result != "done" {
		t.Errorf("msg[4] = %#v, want toolEndMsg{ok:true, result:%q}", got[4], "done")
	}
	if m, ok := got[5].(telemetryMsg); !ok || m.ev.Turns != 3 {
		t.Errorf("msg[5] = %#v, want telemetryMsg{Turns:3}", got[5])
	}
	if _, ok := got[6].(compactionMsg); !ok {
		t.Errorf("msg[6] = %#v, want compactionMsg", got[6])
	}
	if m, ok := got[7].(turnEndMsg); !ok || len(m.results) != 1 || m.results[0].ToolCallID != "call-1" {
		t.Errorf("msg[7] = %#v, want turnEndMsg with one result", got[7])
	}
	if m, ok := got[8].(runEndMsg); !ok || m.err != nil {
		t.Errorf("msg[8] = %#v, want runEndMsg{err:nil}", got[8])
	}
}

// TestToolEndError verifies the ok flag inverts IsError.
func TestToolEndError(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch)
	h.OnEvent(agentcore.ToolExecutionEndEvent{
		ToolCallID: "c",
		Result:     agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("boom")}},
		IsError:    true,
	})
	got := drain(ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 msg, got %d", len(got))
	}
	m, ok := got[0].(toolEndMsg)
	if !ok || m.ok || m.result != "boom" {
		t.Errorf("got %#v, want toolEndMsg{ok:false, result:%q}", got[0], "boom")
	}
}

// TestArgsToMap covers the object / non-object coercion of tool call args.
func TestArgsToMap(t *testing.T) {
	if m := argsToMap(map[string]any{"k": "v"}); m == nil || m["k"] != "v" {
		t.Errorf("object args: got %#v, want map with k=v", m)
	}
	if m := argsToMap("not-an-object"); m != nil {
		t.Errorf("non-object args: got %#v, want nil", m)
	}
	if m := argsToMap(nil); m != nil {
		t.Errorf("nil args: got %#v, want nil", m)
	}
}

// TestWaitForEvent verifies the pump Cmd returns the next queued msg.
func TestWaitForEvent(t *testing.T) {
	ch := newEventChan()
	want := runEndMsg{err: errors.New("stop")}
	ch <- want
	cmd := waitForEvent(ch)
	if cmd == nil {
		t.Fatal("waitForEvent returned nil Cmd")
	}
	got, ok := cmd().(runEndMsg)
	if !ok || got.err == nil || got.err.Error() != "stop" {
		t.Errorf("cmd() = %#v, want runEndMsg{err:stop}", got)
	}
}
