package main

// Tests for the project-trust REPL integration (US-018, #134): the first-run
// prompt, the tool-call confirmation, the summary preview, and the
// BeforeToolCall gating hook. The trust store itself is covered in
// internal/trust; here we exercise the interactive glue.

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/trust"
)

// newTrustManagerAt builds a Manager backed by path.
func newTrustManagerAt(t *testing.T, path string) *trust.Manager {
	t.Helper()
	m, err := trust.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// newTrustManager builds a Manager backed by a fresh temp file and returns the
// manager plus its path (so a test can reload to verify persistence).
func newTrustManager(t *testing.T) (*trust.Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trust.json")
	return newTrustManagerAt(t, path), path
}

// readerOf wraps a string in the *bufio.Reader the trust prompts expect.
func readerOf(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

// TestEnsureTrustPromptTrustedThisDir verifies choice 1 + scope 1 (this
// directory) persists a Trusted decision for cwd.
func TestEnsureTrustPromptTrustedThisDir(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	var out bytes.Buffer
	ensureTrustPrompt(&out, readerOf("1\n1\n"), mgr, cwd)

	if got := mgr.NearestTrustDecision(cwd); !got.Found || got.Decision != trust.Trusted || got.Path != cwd {
		t.Errorf("after prompt, NearestTrustDecision(%q) = %+v, want Trusted/@cwd", cwd, got)
	}
	if !strings.Contains(out.String(), "First time in this directory") {
		t.Errorf("output missing prompt: %q", out.String())
	}
}

// TestEnsureTrustPromptRejectParent verifies choice 3 (reject) + scope 2
// (parent) persists an Untrusted decision for the parent directory.
func TestEnsureTrustPromptRejectParent(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	parent := filepath.Dir(cwd)
	ensureTrustPrompt(&bytes.Buffer{}, readerOf("3\n2\n"), mgr, cwd)

	if got := mgr.NearestTrustDecision(cwd); !got.Found || got.Decision != trust.Untrusted || got.Path != parent {
		t.Errorf("after reject-parent, NearestTrustDecision(%q) = %+v, want Untrusted/@parent", cwd, got)
	}
}

// TestEnsureTrustPromptJustOnce verifies choice 2 (just once) grants session
// trust without persisting: IsTrusted is true now but false after a reload.
func TestEnsureTrustPromptJustOnce(t *testing.T) {
	mgr, path := newTrustManager(t)
	cwd := t.TempDir()
	ensureTrustPrompt(&bytes.Buffer{}, readerOf("2\n"), mgr, cwd)

	if !mgr.IsTrusted(cwd) {
		t.Error("IsTrusted(cwd) = false after just-once, want true")
	}
	// Reload from the same file: session trust must not survive.
	m2 := newTrustManagerAt(t, path)
	if m2.IsTrusted(cwd) {
		t.Error("reload IsTrusted(cwd) = true, want false (session trust must not persist)")
	}
}

// TestEnsureTrustPromptSkipsWhenDecided verifies that when a decision already
// exists, ensureTrustPrompt is a no-op: it writes nothing and reads nothing.
func TestEnsureTrustPromptSkipsWhenDecided(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	if err := mgr.SetDecision(cwd, trust.Trusted); err != nil {
		t.Fatalf("SetDecision: %v", err)
	}
	var out bytes.Buffer
	// Empty reader: if the prompt tried to read it would block/EOF; it must not.
	ensureTrustPrompt(&out, readerOf(""), mgr, cwd)
	if out.Len() != 0 {
		t.Errorf("ensureTrustPrompt wrote %q when a decision existed, want no output", out.String())
	}
}

// TestEstablishTrustApprove verifies --approve grants session trust up front
// without prompting: IsTrusted is true immediately, nothing is written, and the
// (empty) reader is never consumed. Session trust must not persist across a
// reload — --approve is per-run, not a saved decision.
func TestEstablishTrustApprove(t *testing.T) {
	mgr, path := newTrustManager(t)
	cwd := t.TempDir()
	var out bytes.Buffer
	establishTrust(&out, readerOf(""), mgr, cwd, true)

	if !mgr.IsTrusted(cwd) {
		t.Error("IsTrusted(cwd) = false after --approve, want true")
	}
	if out.Len() != 0 {
		t.Errorf("establishTrust wrote %q with --approve, want no prompt", out.String())
	}
	m2 := newTrustManagerAt(t, path)
	if m2.IsTrusted(cwd) {
		t.Error("reload IsTrusted(cwd) = true, want false (--approve must not persist)")
	}
}

// TestEstablishTrustWithoutApprove verifies that without --approve, establishTrust
// defers to the first-launch prompt (which runs for an undecided directory).
func TestEstablishTrustWithoutApprove(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	var out bytes.Buffer
	// "2\n" answers the just-once prompt; if the prompt did not run, this input
	// would be left unread and IsTrusted would stay false.
	establishTrust(&out, readerOf("2\n"), mgr, cwd, false)

	if !strings.Contains(out.String(), "First time in this directory") {
		t.Errorf("without --approve, expected the trust prompt; got %q", out.String())
	}
	if !mgr.IsTrusted(cwd) {
		t.Error("IsTrusted(cwd) = false after just-once via establishTrust, want true")
	}
}

// TestConfirmToolCall verifies the y/n/a responses.
func TestConfirmToolCall(t *testing.T) {
	call := agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"ls"}`)}
	cases := []struct {
		in     string
		allow  bool
		always bool
	}{
		{"y\n", true, false},
		{"yes\n", true, false},
		{"a\n", true, true},
		{"always\n", true, true},
		{"n\n", false, false},
		{"\n", false, false},
		{"garbage\n", false, false},
	}
	for _, c := range cases {
		var out bytes.Buffer
		allow, always := confirmToolCall(&out, readerOf(c.in), call)
		if allow != c.allow || always != c.always {
			t.Errorf("confirmToolCall(%q) = (%v,%v), want (%v,%v)", c.in, allow, always, c.allow, c.always)
		}
	}
}

// TestToolCallSummary verifies the one-line preview for each side-effect tool.
func TestToolCallSummary(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"bash", `{"command":"rm -rf /tmp/x"}`, "command: rm -rf /tmp/x"},
		{"write", `{"path":"/a/b.txt","content":"..."}`, "path: /a/b.txt"},
		{"edit", `{"path":"/a/b.txt","old_string":"x"}`, "path: /a/b.txt"},
		{"bash", `{}`, ""},
		{"bash", ``, ""},
		{"bash", `not-json`, "not-json"},
	}
	for _, c := range cases {
		got := toolCallSummary(agentcore.AgentToolCall{Name: c.name, Arguments: []byte(c.args)})
		if got != c.want {
			t.Errorf("toolCallSummary(%s,%s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

// TestToolCallSummaryTruncates verifies a very long command is truncated.
func TestToolCallSummaryTruncates(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := toolCallSummary(agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"` + long + `"}`)})
	if !strings.HasSuffix(got, " …") {
		t.Errorf("expected truncated output ending with ' …', got %q", got)
	}
	if len([]rune(got)) > 250 {
		t.Errorf("output not truncated: len=%d", len([]rune(got)))
	}
}

// TestTrustBeforeToolCallGating verifies the hook: nil manager -> nil hook;
// trusted dir -> allow; untrusted + deny -> block; untrusted + always -> allow
// and grant session trust so the next call is allowed without a prompt.
func TestTrustBeforeToolCallGating(t *testing.T) {
	cwd := t.TempDir()
	mu := &sync.Mutex{}
	call := agentcore.AgentToolCall{Name: "write", Arguments: []byte(`{"path":"/tmp/x"}`)}

	// nil manager -> no hook.
	if h := trustBeforeToolCall(nil, cwd, nil, nil, mu); h != nil {
		t.Error("nil manager should yield nil hook")
	}

	// Trusted dir: hook returns nil (allow) without prompting.
	mgr, _ := newTrustManager(t)
	if err := mgr.SetDecision(cwd, trust.Trusted); err != nil {
		t.Fatalf("SetDecision: %v", err)
	}
	hook := trustBeforeToolCall(mgr, cwd, readerOf(""), &bytes.Buffer{}, mu)
	if dec := hook(context.Background(), call); dec != nil {
		t.Errorf("trusted dir: hook returned %+v, want nil", dec)
	}

	// Untrusted dir, user denies: hook blocks with an error result.
	mgr2, _ := newTrustManager(t)
	var out bytes.Buffer
	hook2 := trustBeforeToolCall(mgr2, cwd, readerOf("n\n"), &out, mu)
	dec := hook2(context.Background(), call)
	if dec == nil || !dec.Block {
		t.Errorf("untrusted + deny: hook returned %+v, want a Block decision", dec)
	}

	// Untrusted dir, user says "always": hook allows and grants session trust,
	// so a second call is allowed WITHOUT reading any more input.
	mgr3, _ := newTrustManager(t)
	var out3 bytes.Buffer
	// Only one line of input ("a\n"); a second prompt would block on EOF.
	hook3 := trustBeforeToolCall(mgr3, cwd, readerOf("a\n"), &out3, mu)
	if dec := hook3(context.Background(), call); dec != nil {
		t.Errorf("untrusted + always: hook returned %+v, want nil (allow)", dec)
	}
	if !mgr3.IsTrusted(cwd) {
		t.Error("after 'always', IsTrusted(cwd) = false, want true (session trust granted)")
	}
	// Second call: no input left, but session trust means no prompt.
	if dec := hook3(context.Background(), call); dec != nil {
		t.Errorf("second call after 'always': hook returned %+v, want nil (no re-prompt)", dec)
	}
}

// TestTrustBeforeToolCallSkipsNonSideEffect verifies read-only tools are never
// gated, even in an untrusted directory.
func TestTrustBeforeToolCallSkipsNonSideEffect(t *testing.T) {
	cwd := t.TempDir()
	mgr, _ := newTrustManager(t)
	mu := &sync.Mutex{}
	hook := trustBeforeToolCall(mgr, cwd, readerOf(""), &bytes.Buffer{}, mu)
	for _, name := range []string{"read", "grep", "find", "todo", "webfetch"} {
		if dec := hook(context.Background(), agentcore.AgentToolCall{Name: name}); dec != nil {
			t.Errorf("non-side-effect tool %q was gated (%+v), want nil", name, dec)
		}
	}
}

// TestRegisterTrustCommand exercises the /trust action closure: status on an
// undecided dir, on (default) persists Trusted, once grants session trust, off
// revokes session trust AND persists Untrusted (the M2 regression - without
// ClearSessionTrust, an active "always"/once grant would keep IsTrusted true
// until restart), and an unknown arg yields usage.
func TestRegisterTrustCommand(t *testing.T) {
	cwd := t.TempDir()
	mgr, _ := newTrustManager(t)
	reg := runtime.NewSlashRegistry()
	registerTrustCommand(reg, mgr, cwd)
	cmd, ok := reg.Lookup("trust")
	if !ok {
		t.Fatal("/trust command not registered")
	}

	if got := cmd.Action("status"); !strings.Contains(got, "undecided") {
		t.Errorf("status on undecided dir = %q, want 'undecided'", got)
	}

	// on (default arg) persists Trusted for cwd.
	if got := cmd.Action(""); !strings.Contains(got, "trusted") {
		t.Errorf("on = %q, want 'trusted'", got)
	}
	if res := mgr.NearestTrustDecision(cwd); !res.Found || res.Decision != trust.Trusted {
		t.Errorf("after on, nearest = %+v, want Trusted/@cwd", res)
	}

	// Fresh dir: once grants session trust (in-memory), off must revoke it and
	// persist Untrusted so IsTrusted is false immediately.
	cwd2 := t.TempDir()
	mgr2, _ := newTrustManager(t)
	reg2 := runtime.NewSlashRegistry()
	registerTrustCommand(reg2, mgr2, cwd2)
	cmd2, _ := reg2.Lookup("trust")
	cmd2.Action("once")
	if !mgr2.IsTrusted(cwd2) {
		t.Error("after once, IsTrusted = false, want true")
	}
	if got := cmd2.Action("off"); !strings.Contains(got, "untrusted") {
		t.Errorf("off = %q, want 'untrusted'", got)
	}
	if mgr2.IsTrusted(cwd2) {
		t.Error("after off, IsTrusted = true, want false (session grant must be revoked)")
	}
	if res := mgr2.NearestTrustDecision(cwd2); !res.Found || res.Decision != trust.Untrusted {
		t.Errorf("after off, nearest = %+v, want Untrusted/@cwd", res)
	}

	// Unknown arg yields usage text.
	if got := cmd.Action("bogus"); !strings.Contains(got, "usage") {
		t.Errorf("unknown arg = %q, want 'usage'", got)
	}
}
