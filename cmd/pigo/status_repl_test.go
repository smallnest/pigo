// This file holds the REPL-integration and headless-flag tests for /status
// (US-005, #295) that drive the package-main REPL harness (runREPL/newTestDeps)
// or inspect CLI flags. The direct-call rendering tests live in
// internal/cli/status alongside RunStatus.
package main

import (
	"bytes"
	"strings"
	"testing"

	flag "github.com/spf13/pflag"
)

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

func TestRunStatusViaREPL(t *testing.T) {
	// Verify that /status is intercepted in the REPL loop and doesn't invoke the model
	p := &replProvider{reply: "should not be called"}
	deps, _ := newTestDeps(t, p)

	var out bytes.Buffer
	in := strings.NewReader("/status\n/exit\n")
	if err := runREPL(in, &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}

	if p.calls != 0 {
		t.Errorf("expected 0 model calls for /status, got %d", p.calls)
	}

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

	if p.calls != 0 {
		t.Errorf("expected 0 model calls for /statusfoo, got %d", p.calls)
	}

	output := out.String()
	if strings.Contains(output, "runtime config:") {
		t.Error("expected /statusfoo to NOT run the status command")
	}
}

// TestStatusNotInHeadless verifies /status is REPL-only: there is no --status
// CLI flag. (The /status intercept lives in runREPL only; headless print mode
// never runs the REPL loop, so "/status" there is treated as an ordinary
// prompt, not a command.)
func TestStatusNotInHeadless(t *testing.T) {
	if f := flag.Lookup("status"); f != nil {
		t.Errorf("--status flag should not exist (headless must not expose /status), got %v", f)
	}
}

// TestStatusE2EViaREPL drives /status through the REPL loop intercept and
// asserts every section is present and the model is never invoked.
func TestStatusE2EViaREPL(t *testing.T) {
	p := &replProvider{reply: "should not be called"}
	deps, _ := newTestDeps(t, p)
	deps.cwd = "/tmp/e2e-repl"
	deps.live.Model = "e2e-model"
	deps.live.ProviderName = "e2e-prov"
	deps.live.ContextWindow = 128000

	var out bytes.Buffer
	in := strings.NewReader("/status\n/exit\n")
	if err := runREPL(in, &out, deps); err != nil {
		t.Fatalf("runREPL: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("expected 0 model calls for /status, got %d", p.calls)
	}
	got := out.String()
	for _, want := range []string{
		"runtime config:",
		"model: e2e-model",
		"context:",
		"project & environment:",
		"credentials & connectivity:",
		"telemetry:",
		"no telemetry yet",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("REPL /status: expected output to contain %q", want)
		}
	}
}
