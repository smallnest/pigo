package agenttool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluateReadAllowed(t *testing.T) {
	dir := t.TempDir()
	p := NewPolicy(dir)
	if got := p.Evaluate(context.Background(), Request{Kind: AccessRead, Path: "any/file.txt"}); got != DecisionAllow {
		t.Errorf("read should be allowed, got %v", got)
	}
	// Reads outside the jail are still allowed (read-all posture).
	if got := p.Evaluate(context.Background(), Request{Kind: AccessRead, Path: "/etc/hosts"}); got != DecisionAllow {
		t.Errorf("read outside jail should be allowed (read-all), got %v", got)
	}
}

func TestEvaluateReadDenied(t *testing.T) {
	dir := t.TempDir()
	p := NewPolicy(dir)
	p.DenyPaths = []string{"secrets"}
	if got := p.Evaluate(context.Background(), Request{Kind: AccessRead, Path: "secrets/key.pem"}); got != DecisionDeny {
		t.Errorf("denied path should deny read, got %v", got)
	}
}

func TestEvaluateWriteJail(t *testing.T) {
	dir := t.TempDir()
	p := NewPolicy(dir)
	// Inside the jail: allowed.
	if got := p.Evaluate(context.Background(), Request{Kind: AccessWrite, Path: "out/result.txt"}); got != DecisionAllow {
		t.Errorf("write inside jail should be allowed, got %v", got)
	}
	// Outside the jail: denied (write-jail posture).
	if got := p.Evaluate(context.Background(), Request{Kind: AccessWrite, Path: "/tmp/escape.txt"}); got != DecisionDeny {
		t.Errorf("write outside jail should be denied, got %v", got)
	}
	// Escaping via ".." must be denied.
	if got := p.Evaluate(context.Background(), Request{Kind: AccessWrite, Path: "../escape.txt"}); got != DecisionDeny {
		t.Errorf("write escaping via .. should be denied, got %v", got)
	}
}

func TestEvaluateWriteAllowWritePaths(t *testing.T) {
	dir := t.TempDir()
	extra := t.TempDir()
	p := NewPolicy(dir)
	p.AllowWritePaths = []string{extra}
	if got := p.Evaluate(context.Background(), Request{Kind: AccessWrite, Path: filepath.Join(extra, "f.txt")}); got != DecisionAllow {
		t.Errorf("write to allow-listed path should be allowed, got %v", got)
	}
}

func TestEvaluateProtectGit(t *testing.T) {
	dir := t.TempDir()
	p := NewPolicy(dir)
	if got := p.Evaluate(context.Background(), Request{Kind: AccessWrite, Path: ".git/config"}); got != DecisionDeny {
		t.Errorf("write under .git should be denied, got %v", got)
	}
}

func TestEvaluateWriteSymlinkEscape(t *testing.T) {
	if os.Getenv("SKIP_SYMLINK") != "" {
		t.Skip("symlink test skipped")
	}
	jail := t.TempDir()
	outside := t.TempDir()
	// A symlink inside the jail pointing outside must not grant a write escape.
	link := filepath.Join(jail, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	p := NewPolicy(jail)
	got := p.Evaluate(context.Background(), Request{Kind: AccessWrite, Path: "escape/pwned.txt"})
	if got != DecisionDeny {
		t.Errorf("write through symlink escaping jail should be denied, got %v", got)
	}
}

func TestEvaluateExecDangerousDenied(t *testing.T) {
	p := NewPolicy(t.TempDir())
	for _, cmd := range []string{"rm -rf /", "dd if=/dev/zero of=/dev/sda", "echo hi && rm foo"} {
		if got := p.Evaluate(context.Background(), Request{Kind: AccessExec, Command: cmd}); got != DecisionDeny {
			t.Errorf("dangerous command %q should be denied, got %v", cmd, got)
		}
	}
}

func TestEvaluateExecRiskyPrompts(t *testing.T) {
	p := NewPolicy(t.TempDir())
	if got := p.Evaluate(context.Background(), Request{Kind: AccessExec, Command: "git push origin main"}); got != DecisionPrompt {
		t.Errorf("risky command should prompt, got %v", got)
	}
	p.PromptOnRiskyExec = false
	if got := p.Evaluate(context.Background(), Request{Kind: AccessExec, Command: "git push"}); got != DecisionDeny {
		t.Errorf("risky command with prompt disabled should deny, got %v", got)
	}
}

func TestEvaluateExecSafeAllowed(t *testing.T) {
	p := NewPolicy(t.TempDir())
	for _, cmd := range []string{"ls -la", "cat file.txt | grep foo", "echo hello"} {
		if got := p.Evaluate(context.Background(), Request{Kind: AccessExec, Command: cmd}); got != DecisionAllow {
			t.Errorf("safe command %q should be allowed, got %v", cmd, got)
		}
	}
}

func TestEvaluateExecDenyCommand(t *testing.T) {
	p := NewPolicy(t.TempDir())
	p.DenyCommands = []string{"curl"}
	if got := p.Evaluate(context.Background(), Request{Kind: AccessExec, Command: "curl http://evil"}); got != DecisionDeny {
		t.Errorf("deny-listed command should be denied, got %v", got)
	}
}

func TestEvaluateExecAllowList(t *testing.T) {
	p := NewPolicy(t.TempDir())
	p.AllowCommands = []string{"go", "ls"}
	if got := p.Evaluate(context.Background(), Request{Kind: AccessExec, Command: "go test ./..."}); got != DecisionAllow {
		t.Errorf("allow-listed command should be allowed, got %v", got)
	}
	if got := p.Evaluate(context.Background(), Request{Kind: AccessExec, Command: "python evil.py"}); got != DecisionDeny {
		t.Errorf("non-allow-listed command should be denied, got %v", got)
	}
}

func TestEvaluateExecUnparseableFailsClosed(t *testing.T) {
	p := NewPolicy(t.TempDir())
	// An unterminated quote cannot be parsed → risky → prompt (never allow).
	got := p.Evaluate(context.Background(), Request{Kind: AccessExec, Command: `echo "unterminated`})
	if got == DecisionAllow {
		t.Errorf("unparseable command must never be allowed, got %v", got)
	}
}

func TestEvaluateNetwork(t *testing.T) {
	p := NewPolicy(t.TempDir())
	// Empty allow list → deny all.
	if got := p.Evaluate(context.Background(), Request{Kind: AccessNetwork, Host: "example.com"}); got != DecisionDeny {
		t.Errorf("network with empty allow list should deny, got %v", got)
	}
	p.AllowHosts = []string{"example.com"}
	if got := p.Evaluate(context.Background(), Request{Kind: AccessNetwork, Host: "example.com"}); got != DecisionAllow {
		t.Errorf("allow-listed host should be allowed, got %v", got)
	}
	if got := p.Evaluate(context.Background(), Request{Kind: AccessNetwork, Host: "evil.com"}); got != DecisionDeny {
		t.Errorf("non-allow-listed host should be denied, got %v", got)
	}
}

func TestEvaluateUnknownKindDenies(t *testing.T) {
	p := NewPolicy(t.TempDir())
	if got := p.Evaluate(context.Background(), Request{Kind: AccessKind(99)}); got != DecisionDeny {
		t.Errorf("unknown access kind must fail closed, got %v", got)
	}
}

func TestClassifyCommandRisk(t *testing.T) {
	cases := []struct {
		cmd  string
		want riskLevel
	}{
		{"ls", riskSafe},
		{"rm foo", riskDangerous},
		{"sudo ls", riskRisky},
		{"cat a | rm b", riskDangerous},
		{"echo hi; git commit", riskRisky},
		{"/bin/rm -rf x", riskDangerous},
	}
	for _, c := range cases {
		if got := classifyCommandRisk(c.cmd); got != c.want {
			t.Errorf("classifyCommandRisk(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestCommandNamesResolvesBase(t *testing.T) {
	names := commandNames("/usr/bin/git status")
	if len(names) != 1 || names[0] != "git" {
		t.Errorf("expected [git], got %v", names)
	}
	if commandNames(`"unterminated`) != nil {
		t.Errorf("unparseable command should return nil")
	}
}
