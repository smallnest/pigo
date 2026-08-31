package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/agent"
)

// hermetic points provider/skill/plugin discovery at throwaway dirs and supplies
// a dummy key so New resolves fully without ever contacting a network. None of
// these tests call Prompt/Stream, so no real request is made.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("PIGO_HOME", t.TempDir())
}

func contains(set []string, name string) bool {
	for _, n := range set {
		if n == name {
			return true
		}
	}
	return false
}

// TestNewDefaults is the zero-config path: the full built-in tool set is
// advertised and the model id resolves to the openrouter provider.
func TestNewDefaults(t *testing.T) {
	hermetic(t)
	sess, err := agent.New(agent.WithModel("openrouter/free"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sess.Close()

	if got := sess.Model(); got != "openrouter/free" {
		t.Errorf("Model() = %q, want %q", got, "openrouter/free")
	}
	if got := sess.Provider(); got != "openrouter" {
		t.Errorf("Provider() = %q, want %q", got, "openrouter")
	}
	for _, want := range []string{"read", "write", "edit", "grep", "find", "bash", "task"} {
		if !contains(sess.ToolNames(), want) {
			t.Errorf("default tool set missing %q: %q", want, sess.ToolNames())
		}
	}
}

// TestWithToolsAllowlist confirms an allowlist narrows the set to exactly the
// named tools, in order.
func TestWithToolsAllowlist(t *testing.T) {
	hermetic(t)
	sess, err := agent.New(
		agent.WithModel("openrouter/free"),
		agent.WithTools("read", "grep"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sess.Close()

	if got := strings.Join(sess.ToolNames(), ","); got != "read,grep" {
		t.Errorf("ToolNames() = %q, want %q", got, "read,grep")
	}
}

// TestDenyWinsOverAllow is the fail-closed guarantee at the SDK layer: a tool on
// both lists is removed.
func TestDenyWinsOverAllow(t *testing.T) {
	hermetic(t)
	sess, err := agent.New(
		agent.WithModel("openrouter/free"),
		agent.WithTools("read", "bash"),
		agent.WithDisallowedTools("bash"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sess.Close()

	if contains(sess.ToolNames(), "bash") {
		t.Errorf("bash was on both lists and must be removed: %q", sess.ToolNames())
	}
	if !contains(sess.ToolNames(), "read") {
		t.Errorf("read was allowed and not denied, so must survive: %q", sess.ToolNames())
	}
}

// TestWithoutTools yields an empty set — a pure text completion.
func TestWithoutTools(t *testing.T) {
	hermetic(t)
	sess, err := agent.New(
		agent.WithModel("openrouter/free"),
		agent.WithoutTools(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sess.Close()

	if len(sess.ToolNames()) != 0 {
		t.Errorf("WithoutTools must leave no tools, got %q", sess.ToolNames())
	}
}

// TestUnknownToolIsError confirms a misspelled tool name fails construction
// rather than silently dropping the boundary.
func TestUnknownToolIsError(t *testing.T) {
	hermetic(t)
	_, err := agent.New(
		agent.WithModel("openrouter/free"),
		agent.WithTools("raed"),
	)
	if err == nil {
		t.Fatal("New = nil error, want a failure for the misspelled tool name")
	}
}

// TestInvalidThinkingLevelIsError confirms the level is validated up front.
func TestInvalidThinkingLevelIsError(t *testing.T) {
	hermetic(t)
	_, err := agent.New(
		agent.WithModel("openrouter/free"),
		agent.WithThinkingLevel("supersonic"),
	)
	if err == nil {
		t.Fatal("New = nil error, want a failure for the invalid thinking level")
	}
}

// TestValidThinkingLevels accepts every documented level.
func TestValidThinkingLevels(t *testing.T) {
	hermetic(t)
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		sess, err := agent.New(
			agent.WithModel("openrouter/free"),
			agent.WithThinkingLevel(level),
		)
		if err != nil {
			t.Errorf("WithThinkingLevel(%q): %v", level, err)
			continue
		}
		sess.Close()
	}
}

// TestCloseHermetic confirms Close is a no-op (nil) when the session holds no
// plugin manager or memory store, and is safe to call.
func TestCloseHermetic(t *testing.T) {
	hermetic(t)
	sess, err := agent.New(agent.WithModel("openrouter/free"), agent.WithoutTools())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func publicEchoTool(name string) agent.Tool {
	return agent.Tool{
		Name:        name,
		Description: "echo arguments",
		Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		Execute: func(_ context.Context, call agent.ToolCall, _ func(agent.ToolResult)) (agent.ToolResult, error) {
			return agent.ToolResult{
				Content: []agent.ToolResultContent{{Type: agent.ToolResultContentText, Text: string(call.Arguments)}},
			}, nil
		},
	}
}

func TestWithoutToolsKeepsOnlyExplicitCustomTools(t *testing.T) {
	hermetic(t)
	sess, err := agent.New(
		agent.WithModel("openrouter/free"),
		agent.WithoutTools(),
		agent.WithCustomTools(publicEchoTool("piflow_echo")),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sess.Close()

	if got := strings.Join(sess.ToolNames(), ","); got != "piflow_echo" {
		t.Fatalf("ToolNames() = %q, want %q", got, "piflow_echo")
	}
}

func TestCustomToolCannotShadowEnabledBuiltin(t *testing.T) {
	hermetic(t)
	_, err := agent.New(
		agent.WithModel("openrouter/free"),
		agent.WithCustomTools(publicEchoTool("READ")),
	)
	if err == nil {
		t.Fatal("New succeeded, want case-insensitive built-in collision error")
	}
}

func TestCustomToolNamesAreUniqueCaseInsensitively(t *testing.T) {
	hermetic(t)
	_, err := agent.New(
		agent.WithModel("openrouter/free"),
		agent.WithoutTools(),
		agent.WithCustomTools(publicEchoTool("lookup"), publicEchoTool("LOOKUP")),
	)
	if err == nil {
		t.Fatal("New succeeded, want duplicate custom-tool error")
	}
}

func TestInvalidCustomToolsFailConstruction(t *testing.T) {
	cases := []struct {
		name string
		edit func(*agent.Tool)
	}{
		{name: "blank name", edit: func(tool *agent.Tool) { tool.Name = "" }},
		{name: "surrounding whitespace", edit: func(tool *agent.Tool) { tool.Name = " lookup " }},
		{name: "nil execute", edit: func(tool *agent.Tool) { tool.Execute = nil }},
		{name: "invalid schema", edit: func(tool *agent.Tool) { tool.Schema = json.RawMessage(`{"type":`) }},
		{name: "non object schema", edit: func(tool *agent.Tool) { tool.Schema = json.RawMessage(`[]`) }},
		{name: "invalid execution mode", edit: func(tool *agent.Tool) { tool.ExecutionMode = agent.ToolExecutionMode("serial") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hermetic(t)
			tool := publicEchoTool("lookup")
			tc.edit(&tool)
			_, err := agent.New(
				agent.WithModel("openrouter/free"),
				agent.WithoutTools(),
				agent.WithCustomTools(tool),
			)
			if err == nil {
				t.Fatal("New succeeded, want invalid custom-tool error")
			}
		})
	}
}
