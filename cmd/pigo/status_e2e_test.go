// This file holds the end-to-end and edge-case tests for /status (US-005, #295):
// fresh-session behavior, model-switch reflection, telemetry reset on session
// switch, headless non-exposure, and render timing.
package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/smallnest/pigo/internal/agentcore"
)

// TestStatusFreshSessionAllSections verifies /status renders every section on a
// brand-new session before any model turn, with "no telemetry yet", and does not
// panic.
func TestStatusFreshSessionAllSections(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)
	deps.cwd = "/tmp/e2e-fresh"

	var buf bytes.Buffer
	runStatus(&buf, &deps)
	output := buf.String()

	for _, want := range []string{
		"runtime config:",
		"context:",
		"project & environment:",
		"credentials & connectivity:",
		"telemetry:",
		"no telemetry yet",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("fresh-session /status: expected output to contain %q", want)
		}
	}
}

// TestStatusReflectsModelSwitch verifies /status reads the live run config each
// invocation, so a /model switch (which mutates live.model/providerName) is
// reflected on the next /status without a restart.
func TestStatusReflectsModelSwitch(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)
	deps.live.model = "model-a"
	deps.live.providerName = "prov-a"

	var buf bytes.Buffer
	runStatus(&buf, &deps)
	if out := buf.String(); !strings.Contains(out, "model: model-a") || !strings.Contains(out, "provider: prov-a") {
		t.Errorf("expected model-a/prov-a, got:\n%s", out)
	}

	// Simulate a /model switch mutating the live config.
	deps.live.model = "model-b"
	deps.live.providerName = "prov-b"
	buf.Reset()
	runStatus(&buf, &deps)
	out := buf.String()
	if !strings.Contains(out, "model: model-b") || !strings.Contains(out, "provider: prov-b") {
		t.Errorf("expected model-b/prov-b after switch, got:\n%s", out)
	}
	if strings.Contains(out, "model: model-a") {
		t.Errorf("stale model-a still present after switch:\n%s", out)
	}
}

// TestStatusTelemetryResetOnFork verifies that after the telemetry holder is
// reset (as runForkClone/runImport do on /fork, /clone, /import - wired in
// #291), /status shows "no telemetry yet" again, so cumulative stats do not
// bleed across conversations.
func TestStatusTelemetryResetOnFork(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)

	holder := NewTelemetryHolder()
	holder.Fold(agentcore.TelemetryEvent{
		Turns:              3,
		TruncationCount:    1,
		CompactionCount:    0,
		ContextUtilization: 0.42,
		ContextTokens:      53760,
		ContextWindow:      128000,
		ToolDurationsMs:    map[string]agentcore.ToolTiming{"bash": {Count: 2, TotalMs: 150}},
	})
	deps.telemetry = holder

	var buf bytes.Buffer
	runStatus(&buf, &deps)
	if out := buf.String(); !strings.Contains(out, "turns: 3") {
		t.Errorf("expected 'turns: 3' before reset, got:\n%s", out)
	}

	// /fork, /clone, /import all call holder.Reset() (runForkClone/runImport).
	holder.Reset()
	buf.Reset()
	runStatus(&buf, &deps)
	out := buf.String()
	if n := strings.Count(out, "no telemetry yet"); n != 2 {
		t.Errorf("expected 2 'no telemetry yet' after reset (cumulative + last run), got %d:\n%s", n, out)
	}
	if strings.Contains(out, "turns: 3") {
		t.Errorf("stale telemetry after reset:\n%s", out)
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

// TestStatusTiming verifies /status renders fast (target <50ms; assert <100ms
// to absorb CI runner variance). It is pure in-memory rendering - no disk or
// network I/O on the hot path.
func TestStatusTiming(t *testing.T) {
	p := &replProvider{reply: "unused"}
	deps, _ := newTestDeps(t, p)
	deps.cwd = "/tmp/e2e-timing"
	deps.live.contextWindow = 128000
	deps.agentCtx.Messages = append(deps.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}},
	)
	holder := NewTelemetryHolder()
	holder.Fold(agentcore.TelemetryEvent{
		Turns:              5,
		ContextUtilization: 0.5,
		ContextTokens:      64000,
		ContextWindow:      128000,
		ToolDurationsMs:    map[string]agentcore.ToolTiming{"bash": {Count: 3, TotalMs: 210}, "read": {Count: 6, TotalMs: 90}},
	})
	deps.telemetry = holder

	// Warm once (allocs), then measure.
	var buf bytes.Buffer
	runStatus(&buf, &deps)
	buf.Reset()

	start := time.Now()
	runStatus(&buf, &deps)
	elapsed := time.Since(start)
	if elapsed >= 100*time.Millisecond {
		t.Errorf("/status render took %v, want <100ms (target <50ms)", elapsed)
	}
}

// TestStatusE2EViaREPL drives /status through the REPL loop intercept and
// asserts every section is present and the model is never invoked.
func TestStatusE2EViaREPL(t *testing.T) {
	p := &replProvider{reply: "should not be called"}
	deps, _ := newTestDeps(t, p)
	deps.cwd = "/tmp/e2e-repl"
	deps.live.model = "e2e-model"
	deps.live.providerName = "e2e-prov"
	deps.live.contextWindow = 128000

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
