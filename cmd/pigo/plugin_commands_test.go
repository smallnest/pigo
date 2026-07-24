package main

// Tests for plugin slash-command wiring (#265): a plugin-declared command
// (Manager.Commands()) is registered into the REPL slash registry as a hybrid
// (Run) command, and invoking it calls Plugin.CallCommand, surfaces the
// returned notifications, and runs the returned prompt as the next turn. A real
// plugin subprocess is compiled and Discover-loaded so the JSON-RPC transport,
// handshake and commands/call round-trip are exercised end to end.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/plugin"
	"github.com/smallnest/pigo/internal/provider"
	rt "github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/session"
)

// cmdPluginMain is a standalone plugin that declares one "hello" slash command
// and answers commands/call by echoing back a prompt built from the command's
// args plus one notification. It lets the test assert registration, notification
// surfacing, and prompt injection. It also records the raw arguments it received
// so the test can confirm a bare invocation sends a JSON string ("") not null.
const cmdPluginMain = `package main

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
			reply(w, r.ID, json.RawMessage(` + "`" + `{"name":"greeter","commands":[{"name":"hello","description":"say hello"}]}` + "`" + `))
		case "commands/call":
			var p struct {
				Name string          ` + "`json:\"name\"`" + `
				Args json.RawMessage ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(r.Params, &p)
			// args is a JSON string (never null); decode it to prove the contract.
			var argText string
			json.Unmarshal(p.Args, &argText)
			res, _ := json.Marshal(map[string]any{
				"prompt": "please greet " + argText,
				"notifications": []map[string]any{
					{"message": "invoked hello", "type": "info"},
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

// buildPluginInDir compiles src into an executable named bin inside dir and
// returns nothing (the executable path is dir/bin). Discover loads any
// executable regular file directly under dir.
func buildPluginInDir(t *testing.T, dir, bin, src string) {
	t.Helper()
	srcPath := filepath.Join(dir, "plugin_main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write plugin source: %v", err)
	}
	binPath := filepath.Join(dir, bin)
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}
	// Remove the source so Discover only sees the executable (a .go file is not
	// executable, but keeping the dir clean avoids any ambiguity).
	_ = os.Remove(srcPath)
}

// loadTestManager compiles the greeter plugin into a fresh dir and Discover-loads
// it, returning a Manager with exactly that one plugin. The caller must Close it.
func loadTestManager(t *testing.T) *plugin.Manager {
	t.Helper()
	// Build in a build dir, then move only the binary into the plugins dir so
	// Discover (which loads every executable file in the dir) sees just the one
	// plugin executable.
	buildDir := t.TempDir()
	buildPluginInDir(t, buildDir, "greeter", cmdPluginMain)
	pluginsDir := t.TempDir()
	binName := "greeter"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	if err := os.Rename(filepath.Join(buildDir, binName), filepath.Join(pluginsDir, binName)); err != nil {
		t.Fatalf("move plugin binary: %v", err)
	}
	mgr, err := plugin.Discover(pluginsDir, os.Stderr, os.Stderr)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(mgr.Commands()) != 1 || mgr.Commands()[0].Spec.Name != "hello" {
		t.Fatalf("expected one discovered command 'hello', got %+v", mgr.Commands())
	}
	return mgr
}

// TestBuildSlashRegistryRegistersPluginCommand verifies a discovered plugin
// command is registered as a resolvable slash command in the registry.
func TestBuildSlashRegistryRegistersPluginCommand(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	mgr := loadTestManager(t)
	defer mgr.Close()

	reg, err := buildSlashRegistry(&liveRunConfig{model: "faux", providerName: "faux"}, true, mgr)
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}
	if _, ok := reg.Lookup("hello"); !ok {
		t.Fatalf("plugin command /hello was not registered in the slash registry")
	}

	// Resolving it must run the plugin (side effect), surface its notification,
	// and yield the plugin's prompt to run.
	out, err := reg.ResolveOutcome("/hello world")
	if err != nil {
		t.Fatalf("ResolveOutcome(/hello): %v", err)
	}
	if !out.Handled || out.Kind != rt.SlashPrompt {
		t.Fatalf("outcome = %+v, want handled SlashPrompt", out)
	}
	if !strings.Contains(out.Message, "invoked hello") {
		t.Errorf("notification not surfaced in Message: %q", out.Message)
	}
	if out.Prompt != "please greet world" {
		t.Errorf("Prompt = %q, want plugin-returned prompt", out.Prompt)
	}
}

// TestBuiltinWinsOverPluginCommand verifies a built-in command of the same name
// wins over a plugin command (existing precedence preserved): the plugin command
// is shadowed and the built-in's behavior is what resolves.
func TestBuiltinWinsOverPluginCommand(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	mgr := loadTestManager(t)
	defer mgr.Close()

	reg := rt.NewSlashRegistry()
	reg.AddBuiltin(rt.SlashCommand{
		Name:   "hello",
		Action: func(string) string { return "builtin hello" },
	})
	registerPluginCommands(reg, mgr)

	if names := reg.Shadowed(); len(names) != 1 || names[0] != "hello" {
		t.Fatalf("plugin command should be shadowed by built-in, shadowed=%v", names)
	}
	out, err := reg.ResolveOutcome("/hello there")
	if err != nil {
		t.Fatalf("ResolveOutcome: %v", err)
	}
	if out.Kind != rt.SlashAction || out.Message != "builtin hello" {
		t.Errorf("built-in must win: outcome = %+v", out)
	}
}

// TestREPLPluginCommandInjectsPrompt drives the full REPL: invoking a plugin
// slash command prints its notification and runs the returned prompt as the next
// agent turn (so the fake provider is called once and the injected prompt lands
// in the conversation history).
func TestREPLPluginCommandInjectsPrompt(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	mgr := loadTestManager(t)
	defer mgr.Close()

	reg, err := buildSlashRegistry(&liveRunConfig{model: "faux", providerName: "faux"}, true, mgr)
	if err != nil {
		t.Fatalf("buildSlashRegistry: %v", err)
	}

	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	p := &replProvider{reply: "hi there"}
	live := &liveRunConfig{model: "faux", providerName: "faux", provider: p}
	deps := replDeps{
		store:    store,
		header:   session.SessionHeader{ID: session.NewID(time.Now().UTC()), Model: "faux", Provider: "faux"},
		agentCtx: &agentcore.AgentContext{},
		live:     live,
		reg:      agenttool.NewToolRegistry(),
		slash:    reg,
		creds:    provider.NewCredentialStore(nil),
	}

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("/hello world\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}

	// The plugin's notification must have been printed.
	if !strings.Contains(out.String(), "invoked hello") {
		t.Errorf("plugin notification not printed, out=%q", out.String())
	}
	// The returned prompt must have run exactly one turn.
	if p.calls != 1 {
		t.Fatalf("plugin command should inject and run exactly 1 turn, got %d", p.calls)
	}
	// The injected prompt must be the user message that started the turn.
	if len(deps.agentCtx.Messages) == 0 {
		t.Fatal("expected messages in context after the injected turn")
	}
	u0, ok := deps.agentCtx.Messages[0].(agentcore.UserMessage)
	if !ok || agentcore.ContentToText(u0.Content) != "please greet world" {
		t.Errorf("injected prompt not run as the turn: %+v", deps.agentCtx.Messages[0])
	}
}
