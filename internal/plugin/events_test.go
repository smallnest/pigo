// Tests for plugin lifecycle event subscription and delivery (US-017, #133):
// a plugin declares subscribed event types in its manifest, pigo delivers only
// those via one-way `event` notifications, and a slow/hung plugin is isolated by
// the per-event timeout rather than blocking the caller.
package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// eventPluginSrc declares a subscription to two event types and appends every
// received `event` notification (as one JSON line: {"type":...,"data":...}) to a
// file whose path is passed via the PIGO_EVENT_LOG env var. It lets the test
// assert exactly which events were delivered, in order.
const eventPluginSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type req struct {
	ID     *json.RawMessage ` + "`json:\"id\"`" + `
	Method string           ` + "`json:\"method\"`" + `
	Params json.RawMessage  ` + "`json:\"params\"`" + `
}

func main() {
	logPath := os.Getenv("PIGO_EVENT_LOG")
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	w := bufio.NewWriter(os.Stdout)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		switch r.Method {
		case "initialize":
			reply(w, r.ID, json.RawMessage(` + "`" + `{"name":"watcher","version":"1.0","events":["agent_start","tool_execution_end"]}` + "`" + `))
		case "event":
			if logPath != "" {
				f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if f != nil {
					fmt.Fprintf(f, "%s\n", r.Params)
					f.Close()
				}
			}
		case "shutdown":
			return
		}
	}
}

func reply(w *bufio.Writer, id *json.RawMessage, result json.RawMessage) {
	if id == nil {
		return
	}
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Fprintf(w, "%s\n", out)
	w.Flush()
}
`

// TestPluginSubscribesReportsManifestEvents checks Subscribes reflects exactly
// the manifest's declared event types.
func TestPluginSubscribesReportsManifestEvents(t *testing.T) {
	p := &Plugin{Manifest: Manifest{Name: "w", Events: []string{"agent_start", "tool_execution_end"}}}
	if !p.Subscribes("agent_start") {
		t.Error("should subscribe to agent_start")
	}
	if !p.Subscribes("tool_execution_end") {
		t.Error("should subscribe to tool_execution_end")
	}
	if p.Subscribes("turn_end") {
		t.Error("should NOT subscribe to unlisted turn_end")
	}
}

// TestEventNotifierDeliversSubscribedOnly runs a real event-logging plugin and
// verifies the notifier delivers a subscribed event but drops an unsubscribed
// one — the full path: manifest events → Subscribers gate → payload → RPC.
func TestEventNotifierDeliversSubscribedOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell/exec plugin test is unix-oriented")
	}
	logPath := filepath.Join(t.TempDir(), "events.log")
	t.Setenv("PIGO_EVENT_LOG", logPath)

	bin := buildTestPlugin(t, "watcher", eventPluginSrc)
	p, err := Load(bin, nil, os.Stderr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer p.Close()

	m := &Manager{plugins: []*Plugin{p}}
	if !m.Subscribers("agent_start") {
		t.Fatal("manager should report a subscriber for agent_start")
	}
	if m.Subscribers("turn_end") {
		t.Fatal("manager should report NO subscriber for turn_end")
	}

	n := NewEventNotifier(m, os.Stderr)
	if n == nil {
		t.Fatal("NewEventNotifier should be non-nil with a subscribing plugin")
	}
	// Subscribed → delivered.
	n.Handle(agentcore.AgentStartEvent{})
	// Unsubscribed → dropped (never written).
	n.Handle(agentcore.TurnEndEvent{Message: agentcore.AssistantMessage{}})
	// Subscribed → delivered with a payload.
	n.Handle(agentcore.ToolExecutionEndEvent{ToolCallID: "c1", ToolName: "grep", IsError: false})

	// The plugin writes asynchronously; poll briefly for the two expected lines.
	var lines []string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(logPath)
		lines = splitNonEmpty(string(data))
		if len(lines) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 delivered events, got %d: %q", len(lines), lines)
	}
	var first EventParams
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode first event: %v", err)
	}
	if first.Type != "agent_start" {
		t.Errorf("first event type = %q, want agent_start", first.Type)
	}
	var second EventParams
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("decode second event: %v", err)
	}
	if second.Type != "tool_execution_end" {
		t.Errorf("second event type = %q, want tool_execution_end", second.Type)
	}
	if !containsField(second.Data, "toolName", "grep") {
		t.Errorf("second event data missing toolName=grep: %s", second.Data)
	}
}

// TestNewEventNotifierNilWhenNoSubscribers checks the notifier is nil when there
// are no plugins, so the caller can skip wiring OnEvent entirely.
func TestNewEventNotifierNilWhenNoSubscribers(t *testing.T) {
	if NewEventNotifier(nil, os.Stderr) != nil {
		t.Error("nil manager should yield nil notifier")
	}
	if NewEventNotifier(&Manager{}, os.Stderr) != nil {
		t.Error("empty manager should yield nil notifier")
	}
}

// TestSendEventTimesOutOnHungPlugin verifies event delivery is bounded: a plugin
// that initializes then stops reading stdin does not block SendEvent beyond the
// timeout — it returns a timeout error instead of hanging.
func TestSendEventTimesOutOnHungPlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell/exec plugin test is unix-oriented")
	}
	// hungPluginSrc initializes, then blocks forever without reading further
	// stdin, so the OS pipe buffer fills and a Notify write eventually blocks.
	const hungPluginSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type req struct {
	ID     *json.RawMessage ` + "`json:\"id\"`" + `
	Method string           ` + "`json:\"method\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	if sc.Scan() {
		var r req
		json.Unmarshal(sc.Bytes(), &r)
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": json.RawMessage(` + "`" + `{"name":"hung","events":["agent_start"]}` + "`" + `)})
		fmt.Fprintf(w, "%s\n", out)
		w.Flush()
	}
	time.Sleep(60 * time.Second) // never reads stdin again
}
`
	bin := buildTestPlugin(t, "hung", hungPluginSrc)
	p, err := Load(bin, nil, os.Stderr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer p.Close()

	// A single small event will not fill the pipe; force the write to block by
	// sending a large payload many times until SendEvent reports the timeout. The
	// key assertion is that SendEvent RETURNS (bounded) rather than hanging.
	big := make([]byte, 256*1024)
	for i := range big {
		big[i] = 'x'
	}
	payload, _ := json.Marshal(map[string]any{"blob": string(big)})

	start := time.Now()
	var lastErr error
	for range 50 {
		lastErr = p.SendEvent(EventParams{Type: "agent_start", Data: payload})
		if lastErr != nil {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("SendEvent never reported a timeout on a hung plugin")
		}
	}
	if lastErr == nil {
		t.Fatal("expected a timeout error from a hung plugin, got nil")
	}
	// The bounded return is the contract; the elapsed time per call is ~eventTimeout.
	if elapsed := time.Since(start); elapsed > 12*time.Second {
		t.Errorf("SendEvent took too long overall (%s) — not bounded", elapsed)
	}
}

// splitNonEmpty splits s on newlines, dropping empty lines.
func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// containsField reports whether raw JSON object has key == value (string).
func containsField(raw json.RawMessage, key, value string) bool {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	s, ok := m[key].(string)
	return ok && s == value
}
