package status

import (
	"bytes"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/runtime"
)

func TestRunStatus(t *testing.T) {
	host := newFakeHost()
	host.live.Model = "test-model"
	host.live.ProviderName = "test-provider"
	host.live.BaseURL = "https://api.example.com"
	host.live.Protocol = "anthropic"
	host.live.ContextWindow = 128000

	host.agentCtx.Messages = append(host.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}},
	)

	var buf bytes.Buffer
	RunStatus(&buf, host)
	output := buf.String()

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
	host := newFakeHost()
	host.live.ContextWindow = 128000

	host.agentCtx.Messages = append(host.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}},
		agentcore.CompactionMessage{Summary: "compacted history"},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("more")}},
	)

	var buf bytes.Buffer
	RunStatus(&buf, host)
	output := buf.String()

	if !strings.Contains(output, "compactions: 1") {
		t.Error("expected output to contain 'compactions: 1'")
	}
}

func TestRunStatusUnknownContextWindow(t *testing.T) {
	host := newFakeHost()
	host.live.ContextWindow = 0 // unknown

	var buf bytes.Buffer
	RunStatus(&buf, host)
	output := buf.String()

	if !strings.Contains(output, "context window: unknown") {
		t.Error("expected output to contain 'context window: unknown'")
	}
	if !strings.Contains(output, "auto-compaction disabled") {
		t.Error("expected output to contain 'auto-compaction disabled'")
	}
}

func TestBeforeCompactCalculation(t *testing.T) {
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

func TestRunStatusEnvAndCreds(t *testing.T) {
	host := newFakeHost()
	host.cwd = "/tmp/test-cwd"
	host.live.ProviderName = "test-provider"
	host.live.BaseURL = "https://api.example.com"

	host.slash.AddSkill(runtime.SlashCommand{Name: "my-skill", Expand: func(string) string { return "" }})
	host.slash.AddPlugin(runtime.SlashCommand{Name: "my-plugin", Run: func(string) (string, string) { return "", "" }})

	host.creds.SetOverride("test-provider", "sk-secretkey-wxyz")

	var buf bytes.Buffer
	RunStatus(&buf, host)
	output := buf.String()

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

	if !strings.Contains(output, "credentials & connectivity:") {
		t.Error("expected 'credentials & connectivity:' section")
	}
	if !strings.Contains(output, "api key: set") {
		t.Error("expected 'api key: set'")
	}
	if !strings.Contains(output, "••••wxyz") {
		t.Error("expected masked key '••••wxyz'")
	}
	if strings.Contains(output, "sk-secretkey-wxyz") {
		t.Error("full API key leaked into /status output")
	}
	if !strings.Contains(output, "endpoint: https://api.example.com") {
		t.Error("expected 'endpoint: https://api.example.com'")
	}
}

func TestRunStatusTelemetryNoData(t *testing.T) {
	host := newFakeHost()
	// host.telemetry is nil.

	var buf bytes.Buffer
	RunStatus(&buf, host)
	output := buf.String()

	if !strings.Contains(output, "telemetry:") {
		t.Error("expected 'telemetry:' section")
	}
	if n := strings.Count(output, "no telemetry yet"); n != 2 {
		t.Errorf("expected 2 'no telemetry yet' (cumulative + last run), got %d", n)
	}
}

func TestRunStatusTelemetryPopulated(t *testing.T) {
	host := newFakeHost()
	host.live.ContextWindow = 128000

	holder := cli.NewTelemetryHolder()
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
	host.telemetry = holder

	var buf bytes.Buffer
	RunStatus(&buf, host)
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
	RunStatus(&buf, host)
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
