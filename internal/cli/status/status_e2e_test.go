// This file holds the end-to-end and edge-case tests for /status (US-005, #295):
// fresh-session behavior, model-switch reflection, telemetry reset on session
// switch, and render timing, exercised through direct RunStatus calls with a
// fake host. The REPL-intercept and headless-flag tests live in package main.
package status

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
)

// TestStatusFreshSessionAllSections verifies /status renders every section on a
// brand-new session before any model turn, with "no telemetry yet", and does not
// panic.
func TestStatusFreshSessionAllSections(t *testing.T) {
	host := newFakeHost()
	host.cwd = "/tmp/e2e-fresh"

	var buf bytes.Buffer
	RunStatus(&buf, host)
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
// invocation, so a /model switch (which mutates live.Model/providerName) is
// reflected on the next /status without a restart.
func TestStatusReflectsModelSwitch(t *testing.T) {
	host := newFakeHost()
	host.live.Model = "model-a"
	host.live.ProviderName = "prov-a"

	var buf bytes.Buffer
	RunStatus(&buf, host)
	if out := buf.String(); !strings.Contains(out, "model: model-a") || !strings.Contains(out, "provider: prov-a") {
		t.Errorf("expected model-a/prov-a, got:\n%s", out)
	}

	// Simulate a /model switch mutating the live config.
	host.live.Model = "model-b"
	host.live.ProviderName = "prov-b"
	buf.Reset()
	RunStatus(&buf, host)
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
	host := newFakeHost()

	holder := cli.NewTelemetryHolder()
	holder.Fold(agentcore.TelemetryEvent{
		Turns:              3,
		TruncationCount:    1,
		CompactionCount:    0,
		ContextUtilization: 0.42,
		ContextTokens:      53760,
		ContextWindow:      128000,
		ToolDurationsMs:    map[string]agentcore.ToolTiming{"bash": {Count: 2, TotalMs: 150}},
	})
	host.telemetry = holder

	var buf bytes.Buffer
	RunStatus(&buf, host)
	if out := buf.String(); !strings.Contains(out, "turns: 3") {
		t.Errorf("expected 'turns: 3' before reset, got:\n%s", out)
	}

	// /fork, /clone, /import all call holder.Reset() (runForkClone/runImport).
	holder.Reset()
	buf.Reset()
	RunStatus(&buf, host)
	out := buf.String()
	if n := strings.Count(out, "no telemetry yet"); n != 2 {
		t.Errorf("expected 2 'no telemetry yet' after reset (cumulative + last run), got %d:\n%s", n, out)
	}
	if strings.Contains(out, "turns: 3") {
		t.Errorf("stale telemetry after reset:\n%s", out)
	}
}

// TestStatusTiming verifies /status renders fast (target <50ms; assert <100ms
// to absorb CI runner variance). It is pure in-memory rendering - no disk or
// network I/O on the hot path.
func TestStatusTiming(t *testing.T) {
	host := newFakeHost()
	host.cwd = "/tmp/e2e-timing"
	host.live.ContextWindow = 128000
	host.agentCtx.Messages = append(host.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}},
	)
	holder := cli.NewTelemetryHolder()
	holder.Fold(agentcore.TelemetryEvent{
		Turns:              5,
		ContextUtilization: 0.5,
		ContextTokens:      64000,
		ContextWindow:      128000,
		ToolDurationsMs:    map[string]agentcore.ToolTiming{"bash": {Count: 3, TotalMs: 210}, "read": {Count: 6, TotalMs: 90}},
	})
	host.telemetry = holder

	// Warm once (allocs), then measure.
	var buf bytes.Buffer
	RunStatus(&buf, host)
	buf.Reset()

	start := time.Now()
	RunStatus(&buf, host)
	elapsed := time.Since(start)
	if elapsed >= 100*time.Millisecond {
		t.Errorf("/status render took %v, want <100ms (target <50ms)", elapsed)
	}
}
