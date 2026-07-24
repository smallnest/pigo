// Tests for the plugin system (US-016, #132). A test plugin is a tiny Go program
// compiled once per test run; it speaks the JSON-RPC protocol over stdio so the
// tests exercise the real subprocess transport, handshake, tool forwarding, and
// crash isolation — no network or mocks.
package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// buildTestPlugin compiles the given Go source into an executable under a temp
// dir and returns its path. The source is a standalone main package.
func buildTestPlugin(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, name+".go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write plugin source: %v", err)
	}
	bin := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, srcPath)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test plugin: %v\n%s", err, out)
	}
	return bin
}

// echoPluginSrc is a plugin that declares one "shout" tool which uppercases its
// "text" argument.
const echoPluginSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type req struct {
	ID     *json.RawMessage ` + "`json:\"id\"`" + `
	Method string           ` + "`json:\"method\"`" + `
	Params json.RawMessage  ` + "`json:\"params\"`" + `
}

func main() {
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
			reply(w, r.ID, json.RawMessage(` + "`" + `{"name":"echo","version":"1.0","tools":[{"name":"shout","description":"uppercase text","schema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]}` + "`" + `))
		case "tools/call":
			var p struct {
				Name      string          ` + "`json:\"name\"`" + `
				Arguments json.RawMessage ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(r.Params, &p)
			var a struct{ Text string ` + "`json:\"text\"`" + ` }
			json.Unmarshal(p.Arguments, &a)
			res, _ := json.Marshal(map[string]any{"content": strings.ToUpper(a.Text)})
			reply(w, r.ID, res)
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

// TestPluginLoadAndCall exercises the full path: build → load (handshake) →
// adapt tool → call → result.
func TestPluginLoadAndCall(t *testing.T) {
	bin := buildTestPlugin(t, "echo", echoPluginSrc)
	p, err := Load(bin, nil, os.Stderr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer p.Close()

	if p.Manifest.Name != "echo" {
		t.Errorf("manifest name = %q, want echo", p.Manifest.Name)
	}
	tools := p.Tools()
	if len(tools) != 1 || tools[0].Name() != "shout" {
		t.Fatalf("tools = %+v, want one 'shout'", tools)
	}
	if tools[0].ExecutionMode() != agentcore.ToolExecutionSequential {
		t.Errorf("plugin tool should be sequential")
	}

	res, err := tools[0].Execute(context.Background(), "c1", json.RawMessage(`{"text":"hello"}`), nil)
	if err != nil {
		t.Fatalf("Execute Go error: %v", err)
	}
	if txt := agentcore.ContentToText(res.Content); txt != "HELLO" {
		t.Errorf("result = %q, want HELLO", txt)
	}
}

// crashPluginSrc initializes fine but exits abruptly on the first tools/call.
const crashPluginSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type req struct {
	ID     *json.RawMessage ` + "`json:\"id\"`" + `
	Method string           ` + "`json:\"method\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		switch r.Method {
		case "initialize":
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": json.RawMessage(` + "`" + `{"name":"crash","tools":[{"name":"boom","description":"crashes"}]}` + "`" + `)})
			fmt.Fprintf(w, "%s\n", out)
			w.Flush()
		case "tools/call":
			os.Exit(1) // crash mid-call: no response is ever sent
		}
	}
}
`

// TestPluginCrashIsolation checks that a plugin crashing during a tool call
// degrades to an error result, not a panic or a hang.
func TestPluginCrashIsolation(t *testing.T) {
	bin := buildTestPlugin(t, "crash", crashPluginSrc)
	p, err := Load(bin, nil, os.Stderr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer p.Close()

	tools := p.Tools()
	if len(tools) != 1 {
		t.Fatalf("want one tool, got %d", len(tools))
	}
	res, err := tools[0].Execute(context.Background(), "c1", json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Execute must not return a Go error even on crash: %v", err)
	}
	txt := agentcore.ContentToText(res.Content)
	if !strings.Contains(txt, "plugin call failed") {
		t.Errorf("expected isolated error result, got %q", txt)
	}
}

// cmdPluginSrc declares two slash commands and answers commands/call by echoing
// the command name back as a prompt plus one notification. Its manifest command
// order (greet, then bye) lets tests assert manifest-order aggregation.
const cmdPluginSrc = `package main

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
			reply(w, r.ID, json.RawMessage(` + "`" + `{"name":"cmd","commands":[{"name":"greet","description":"greets"},{"name":"bye","description":"farewell"}]}` + "`" + `))
		case "commands/call":
			var p struct {
				Name string          ` + "`json:\"name\"`" + `
				Args json.RawMessage ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(r.Params, &p)
			res, _ := json.Marshal(map[string]any{
				"prompt": "did:" + p.Name,
				"notifications": []map[string]any{
					{"message": "ran " + p.Name, "type": "info"},
				},
			})
			reply(w, r.ID, res)
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

// TestPluginCallCommand checks CallCommand round-trips a prompt and its
// notifications from a plugin over the real subprocess transport.
func TestPluginCallCommand(t *testing.T) {
	bin := buildTestPlugin(t, "cmd", cmdPluginSrc)
	p, err := Load(bin, nil, os.Stderr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer p.Close()

	res, err := p.CallCommand(context.Background(), "greet", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("CallCommand: %v", err)
	}
	if res.Prompt != "did:greet" {
		t.Errorf("prompt = %q, want did:greet", res.Prompt)
	}
	if len(res.Notifications) != 1 {
		t.Fatalf("notifications = %+v, want one", res.Notifications)
	}
	if res.Notifications[0].Message != "ran greet" || res.Notifications[0].Type != "info" {
		t.Errorf("notification = %+v, want {ran greet, info}", res.Notifications[0])
	}
}

// TestPluginCallCommandTransportError checks that a transport error (the plugin
// crashed mid-call) surfaces as a returned error, never a panic.
func TestPluginCallCommandTransportError(t *testing.T) {
	bin := buildTestPlugin(t, "cmdcrash", cmdCrashPluginSrc)
	p, err := Load(bin, nil, os.Stderr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer p.Close()

	_, err = p.CallCommand(context.Background(), "greet", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("CallCommand must return an error when the plugin crashes mid-call")
	}
}

// cmdCrashPluginSrc initializes with one command then exits abruptly on the
// first commands/call, so no response is ever sent.
const cmdCrashPluginSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type req struct {
	ID     *json.RawMessage ` + "`json:\"id\"`" + `
	Method string           ` + "`json:\"method\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		switch r.Method {
		case "initialize":
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": json.RawMessage(` + "`" + `{"name":"cmdcrash","commands":[{"name":"greet","description":"greets"}]}` + "`" + `)})
			fmt.Fprintf(w, "%s\n", out)
			w.Flush()
		case "commands/call":
			os.Exit(1) // crash mid-call: no response is ever sent
		}
	}
}
`
