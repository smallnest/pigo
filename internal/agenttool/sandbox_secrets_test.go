package agenttool

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func TestScrubRegisteredSecret(t *testing.T) {
	reg := NewSecretRegistry()
	reg.Register("OPENAI_API_KEY", "supersecretvalue123")
	out := scrubResultSecrets("token is supersecretvalue123 ok", reg)
	if strings.Contains(out, "supersecretvalue123") {
		t.Errorf("secret value leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:OPENAI_API_KEY]") {
		t.Errorf("expected redaction marker, got %q", out)
	}
}

func TestScrubPatternKeys(t *testing.T) {
	cases := []struct {
		in    string
		label string
	}{
		{"key sk-ant-abc123def456ghi789jkl0 here", "anthropic-key"},
		{"AKIAIOSFODNN7EXAMPLE cred", "aws-access-key"},
		{"token ghp_1234567890abcdefghijABCDEF here", "github-token"},
	}
	for _, c := range cases {
		out := scrubResultSecrets(c.in, nil)
		if !strings.Contains(out, "[REDACTED:"+c.label+"]") {
			t.Errorf("expected %s redaction for %q, got %q", c.label, c.in, out)
		}
	}
}

func TestScrubShortRegisteredValueIgnored(t *testing.T) {
	reg := NewSecretRegistry()
	reg.Register("X", "abc") // too short, ignored
	out := scrubResultSecrets("abc def abc", reg)
	if out != "abc def abc" {
		t.Errorf("short value should not be redacted, got %q", out)
	}
}

func TestScrubLongestMatchFirst(t *testing.T) {
	reg := NewSecretRegistry()
	reg.Register("short", "secretpart")
	reg.Register("long", "secretpartlonger")
	out := scrubResultSecrets("value secretpartlonger end", reg)
	if strings.Contains(out, "secretpart") {
		t.Errorf("value leaked or partially redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:long]") {
		t.Errorf("expected longest-match redaction, got %q", out)
	}
}

func TestScrubNoSecrets(t *testing.T) {
	if got := scrubResultSecrets("plain output text", nil); got != "plain output text" {
		t.Errorf("plain text should be unchanged, got %q", got)
	}
}

func TestScrubContentList(t *testing.T) {
	reg := NewSecretRegistry()
	reg.Register("KEY", "topsecret9999")
	list := agentcore.ContentList{agentcore.NewTextContent("has topsecret9999"), agentcore.NewImageContent("data", "image/png")}
	out := scrubContentList(list, reg)
	tc, ok := out[0].(agentcore.TextContent)
	if !ok || strings.Contains(tc.Text, "topsecret9999") {
		t.Errorf("text block not scrubbed: %+v", out[0])
	}
	if _, ok := out[1].(agentcore.ImageContent); !ok {
		t.Errorf("non-text block should pass through unchanged")
	}
}

func TestSandboxGateBeforeHookDenies(t *testing.T) {
	gate := &SandboxGate{
		Policy: NewPolicy(t.TempDir()),
		Classify: func(call agentcore.AgentToolCall) (Request, bool) {
			return Request{Kind: AccessExec, Command: "rm -rf /"}, true
		},
	}
	dec := gate.BeforeHook(context.Background(), agentcore.AgentToolCall{Name: "bash"})
	if dec == nil || !dec.Block {
		t.Fatalf("expected block decision, got %+v", dec)
	}
}

func TestSandboxGateBeforeHookPromptNoApproverBlocks(t *testing.T) {
	gate := &SandboxGate{
		Policy: NewPolicy(t.TempDir()),
		Classify: func(call agentcore.AgentToolCall) (Request, bool) {
			return Request{Kind: AccessExec, Command: "git push"}, true
		},
	}
	// No approver → prompt must fail closed (block).
	dec := gate.BeforeHook(context.Background(), agentcore.AgentToolCall{Name: "bash"})
	if dec == nil || !dec.Block {
		t.Fatalf("prompt without approver must block (never fail-open), got %+v", dec)
	}
}

func TestSandboxGateBeforeHookPromptApproved(t *testing.T) {
	gate := &SandboxGate{
		Policy: NewPolicy(t.TempDir()),
		Classify: func(call agentcore.AgentToolCall) (Request, bool) {
			return Request{Kind: AccessExec, Command: "git push"}, true
		},
		PromptApprover: func(ctx context.Context, call agentcore.AgentToolCall, req Request) bool { return true },
	}
	if dec := gate.BeforeHook(context.Background(), agentcore.AgentToolCall{Name: "bash"}); dec != nil {
		t.Fatalf("approved prompt should allow (nil), got %+v", dec)
	}
}

func TestSandboxGateBeforeHookAllows(t *testing.T) {
	gate := &SandboxGate{
		Policy: NewPolicy(t.TempDir()),
		Classify: func(call agentcore.AgentToolCall) (Request, bool) {
			return Request{Kind: AccessExec, Command: "ls"}, true
		},
	}
	if dec := gate.BeforeHook(context.Background(), agentcore.AgentToolCall{Name: "bash"}); dec != nil {
		t.Fatalf("safe command should allow (nil), got %+v", dec)
	}
}

func TestSandboxGateAfterHookScrubs(t *testing.T) {
	reg := NewSecretRegistry()
	reg.Register("KEY", "leakedsecret42")
	gate := &SandboxGate{Secrets: reg}
	res := agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("out leakedsecret42")}}
	over := gate.AfterHook(context.Background(), agentcore.AgentToolCall{}, res, false)
	if over == nil || over.Content == nil {
		t.Fatalf("expected content override")
	}
	tc := (*over.Content)[0].(agentcore.TextContent)
	if strings.Contains(tc.Text, "leakedsecret42") {
		t.Errorf("secret leaked through AfterHook: %q", tc.Text)
	}
}
