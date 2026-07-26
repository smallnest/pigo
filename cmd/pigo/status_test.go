package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/runtime"
)

func TestRunStatus(t *testing.T) {
	// Use the same test setup as other REPL tests
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)

	// Customize the live config for clearer test expectations
	deps.live.model = "test-model"
	deps.live.providerName = "test-provider"
	deps.live.baseURL = "https://api.example.com"
	deps.live.protocol = "anthropic"
	deps.live.contextWindow = 128000

	// Add a message to the context
	deps.agentCtx.Messages = append(deps.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}},
	)

	// Test with color disabled
	var buf bytes.Buffer
	runStatus(&buf, &deps)
	output := buf.String()

	// Check that the runtime config section appears
	if !strings.Contains(output, "runtime config:") {
		t.Error("expected output to contain 'runtime config:'")
	}
	if !strings.Contains(output, "model: test-model") {
		t.Error("expected output to contain 'model: test-model'")
	}
	if !strings.Contains(output, "provider: test-provider") {
		t.Error("expected output to contain 'provider: test-provider'")
	}
	if !strings.Contains(output, "base URL: https://api.example.com") {
		t.Error("expected output to contain 'base URL: https://api.example.com'")
	}
	if !strings.Contains(output, "protocol: anthropic") {
		t.Error("expected output to contain 'protocol: anthropic'")
	}
	if !strings.Contains(output, "context window: 128000 tokens") {
		t.Error("expected output to contain 'context window: 128000 tokens'")
	}

	// Check that the context section appears
	if !strings.Contains(output, "context:") {
		t.Error("expected output to contain 'context:'")
	}
	if !strings.Contains(output, "current:") {
		t.Error("expected output to contain 'current:'")
	}
	if !strings.Contains(output, "utilization:") {
		t.Error("expected output to contain 'utilization:'")
	}
	if !strings.Contains(output, "compactions: 0") {
		t.Error("expected output to contain 'compactions: 0'")
	}
	if !strings.Contains(output, "before compact:") {
		t.Error("expected output to contain 'before compact:'")
	}
}

func TestRunStatusWithCompaction(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)
	deps.live.contextWindow = 128000

	// Add messages including a compaction message
	deps.agentCtx.Messages = append(deps.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}},
		agentcore.CompactionMessage{Summary: "compacted history"},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("more")}},
	)

	var buf bytes.Buffer
	runStatus(&buf, &deps)
	output := buf.String()

	if !strings.Contains(output, "compactions: 1") {
		t.Error("expected output to contain 'compactions: 1'")
	}
}

func TestRunStatusUnknownContextWindow(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)
	deps.live.contextWindow = 0 // unknown

	var buf bytes.Buffer
	runStatus(&buf, &deps)
	output := buf.String()

	if !strings.Contains(output, "context window: unknown") {
		t.Error("expected output to contain 'context window: unknown'")
	}
	if !strings.Contains(output, "auto-compaction disabled") {
		t.Error("expected output to contain 'auto-compaction disabled'")
	}
}

func TestStatusGuard(t *testing.T) {
	// The guard logic is in the REPL loop: matches "/status" or "/status "
	// but not "/statusfoo" or "/statusbar"
	testCases := []struct {
		line string
		want bool
	}{
		{"/status", true},
		{"/status ", true},
		{"/status foo", true},
		{"/statusbar", false},
		{"/statusfoo", false},
		{"/status123", false},
		{"/stat", false},
		{"/session", false},
	}

	for _, tc := range testCases {
		got := (tc.line == "/status" || strings.HasPrefix(tc.line, "/status "))
		if got != tc.want {
			t.Errorf("line %q: got %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestBeforeCompactCalculation(t *testing.T) {
	// Test the threshold calculation logic
	// This tests the logic used in printContextStatus
	reserve := compaction.DefaultCompactionSettings.ReserveTokens
	if reserve != 16384 {
		t.Errorf("expected reserve tokens to be 16384, got %d", reserve)
	}

	contextWindow := 128000
	threshold := contextWindow - reserve
	if threshold != 128000-16384 {
		t.Errorf("expected threshold to be 128000-16384=%d, got %d", 128000-16384, threshold)
	}
}

func TestRunStatusViaREPL(t *testing.T) {
	// Verify that /status is intercepted in the REPL loop and doesn't invoke the model
	p := &replProvider{reply: "should not be called"}
	deps, _ := newTestDeps(t, p)

	var out bytes.Buffer
	in := strings.NewReader("/status\n/exit\n")
	if err := runREPL(in, &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}

	// Check that the provider was not called (since /status is intercepted)
	if p.calls != 0 {
		t.Errorf("expected 0 model calls for /status, got %d", p.calls)
	}

	// Check that the status output appears
	output := out.String()
	if !strings.Contains(output, "runtime config:") {
		t.Error("expected REPL /status output to contain 'runtime config:'")
	}
	if !strings.Contains(output, "context:") {
		t.Error("expected REPL /status output to contain 'context:'")
	}
}

func TestStatusFooNotIntercepted(t *testing.T) {
	// Verify that "/statusfoo" is NOT intercepted as "/status"
	p := &replProvider{reply: "model called"}
	deps, _ := newTestDeps(t, p)

	var out bytes.Buffer
	in := strings.NewReader("/statusfoo\n/exit\n")
	if err := runREPL(in, &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}

	// The provider should NOT be called because "/statusfoo" is treated as a
	// slash command that doesn't exist, not as a prompt
	if p.calls != 0 {
		t.Errorf("expected 0 model calls for /statusfoo, got %d", p.calls)
	}

	// But the key thing: "/statusfoo" was NOT intercepted as "/status"
	output := out.String()
	if strings.Contains(output, "runtime config:") {
		t.Error("expected /statusfoo to NOT run the status command")
	}
}

func TestRunStatusEnvAndCreds(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)
	deps.cwd = "/tmp/test-cwd"
	deps.live.providerName = "test-provider"
	deps.live.baseURL = "https://api.example.com"

	// Register a skill and a plugin command so /status can count them separately.
	deps.slash.AddSkill(runtime.SlashCommand{Name: "my-skill", Expand: func(string) string { return "" }})
	deps.slash.AddPlugin(runtime.SlashCommand{Name: "my-plugin", Run: func(string) (string, string) { return "", "" }})

	// Set an API key for the provider.
	deps.creds.SetOverride("test-provider", "sk-secretkey-wxyz")

	var buf bytes.Buffer
	runStatus(&buf, &deps)
	output := buf.String()

	// project & environment section
	if !strings.Contains(output, "project & environment:") {
		t.Error("expected 'project & environment:' section")
	}
	if !strings.Contains(output, "cwd: /tmp/test-cwd") {
		t.Error("expected 'cwd: /tmp/test-cwd'")
	}
	if !strings.Contains(output, "trust: disabled") {
		t.Error("expected 'trust: disabled' when trust manager is nil")
	}
	if !strings.Contains(output, "skills: 1 (my-skill)") {
		t.Error("expected 'skills: 1 (my-skill)'")
	}
	if !strings.Contains(output, "plugins: 1 (my-plugin)") {
		t.Error("expected 'plugins: 1 (my-plugin)'")
	}

	// credentials & connectivity section
	if !strings.Contains(output, "credentials & connectivity:") {
		t.Error("expected 'credentials & connectivity:' section")
	}
	if !strings.Contains(output, "api key: set") {
		t.Error("expected 'api key: set'")
	}
	if !strings.Contains(output, "••••wxyz") {
		t.Error("expected masked key '••••wxyz'")
	}
	// The full key must NEVER appear in the output.
	if strings.Contains(output, "sk-secretkey-wxyz") {
		t.Error("full API key leaked into /status output")
	}
	if !strings.Contains(output, "endpoint: https://api.example.com") {
		t.Error("expected 'endpoint: https://api.example.com'")
	}
}

func TestRunStatusTelemetryNoData(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)
	// deps.telemetry is nil from newTestDeps.

	var buf bytes.Buffer
	runStatus(&buf, &deps)
	output := buf.String()

	if !strings.Contains(output, "telemetry:") {
		t.Error("expected 'telemetry:' section")
	}
	if n := strings.Count(output, "no telemetry yet"); n != 2 {
		t.Errorf("expected 2 'no telemetry yet' (cumulative + last run), got %d", n)
	}
}

func TestRunStatusTelemetryPopulated(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)
	deps.live.contextWindow = 128000

	holder := NewTelemetryHolder()
	holder.Fold(agentcore.TelemetryEvent{
		Turns:              3,
		TruncationCount:    1,
		CompactionCount:    0,
		ContextUtilization: 0.42,
		ContextTokens:      53760,
		ContextWindow:      128000,
		ToolDurationsMs: map[string]agentcore.ToolTiming{
			"bash": {Count: 2, TotalMs: 150},
			"read": {Count: 4, TotalMs: 80},
		},
	})
	deps.telemetry = holder

	var buf bytes.Buffer
	runStatus(&buf, &deps)
	output := buf.String()

	if strings.Contains(output, "no telemetry yet") {
		t.Error("did not expect 'no telemetry yet' when telemetry is populated")
	}
	if !strings.Contains(output, "since session start:") {
		t.Error("expected 'since session start:' cumulative block")
	}
	if !strings.Contains(output, "last run:") {
		t.Error("expected 'last run:' block")
	}
	if !strings.Contains(output, "turns: 3") {
		t.Error("expected 'turns: 3' (last run == cumulative after one run)")
	}
	if !strings.Contains(output, "bash") || !strings.Contains(output, "2 calls") || !strings.Contains(output, "150ms") {
		t.Error("expected bash tool row with '2 calls' / '150ms'")
	}
	if !strings.Contains(output, "utilization: 42%") {
		t.Error("expected 'utilization: 42%'")
	}

	// Fold a second run: cumulative (5) must exceed last run (2).
	holder.Fold(agentcore.TelemetryEvent{
		Turns:              2,
		TruncationCount:    0,
		CompactionCount:    1,
		ContextUtilization: 0.5,
		ContextTokens:      64000,
		ContextWindow:      128000,
		ToolDurationsMs: map[string]agentcore.ToolTiming{
			"bash": {Count: 1, TotalMs: 40},
		},
	})
	buf.Reset()
	runStatus(&buf, &deps)
	output = buf.String()
	if !strings.Contains(output, "turns: 5") {
		t.Error("expected cumulative 'turns: 5' after two runs")
	}
	if !strings.Contains(output, "turns: 2") {
		t.Error("expected last-run 'turns: 2' after two runs")
	}
}

func TestMaskKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sk-secretkey-wxyz", "••••wxyz"},
		{"abcd", "••••"}, // exactly 4 -> masked entirely
		{"ab", "••"},
		{"", ""},
	}
	for _, c := range cases {
		if got := maskKey(c.in); got != c.want {
			t.Errorf("maskKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
