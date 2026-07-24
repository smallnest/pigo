package main

// Tests for the /btw model/thinking override config (#282, US-005): btw.json
// overlays the session defaults for the side thread only, is read fresh each
// call, and falls back silently on missing/empty/partial config.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// withBtwConfig points PIGO_HOME at a temp dir and writes btw.json with the
// given contents (or removes it when contents is ""), returning nothing — the
// temp dir is cleaned up by t.TempDir. It restores PIGO_HOME after the test.
func withBtwConfig(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PIGO_HOME", dir)
	if contents != "" {
		if err := os.WriteFile(filepath.Join(dir, "btw.json"), []byte(contents), 0o644); err != nil {
			t.Fatalf("write btw.json: %v", err)
		}
	}
}

// TestBtwConfigAbsentInherits verifies that with no btw.json the side settings
// equal the session defaults.
func TestBtwConfigAbsentInherits(t *testing.T) {
	withBtwConfig(t, "") // no file
	deps, _ := newTestDeps(t, &replProvider{reply: "x"})
	deps.live.thinkingLevel = agentcore.ThinkingMedium

	var warn bytes.Buffer
	s := resolveBtwSettings(&warn, &deps)
	if s.model != deps.live.model || s.providerName != deps.live.providerName {
		t.Errorf("absent config must inherit model/provider, got %q/%q", s.model, s.providerName)
	}
	if s.thinkingLevel != agentcore.ThinkingMedium {
		t.Errorf("absent config must inherit thinkingLevel, got %q", s.thinkingLevel)
	}
	if warn.Len() != 0 {
		t.Errorf("absent config must not warn, got %q", warn.String())
	}
}

// TestBtwConfigEmptyObjectInherits verifies that an empty JSON object inherits
// everything without warning.
func TestBtwConfigEmptyObjectInherits(t *testing.T) {
	withBtwConfig(t, "{}")
	deps, _ := newTestDeps(t, &replProvider{reply: "x"})
	deps.live.thinkingLevel = agentcore.ThinkingLow

	var warn bytes.Buffer
	s := resolveBtwSettings(&warn, &deps)
	if s.model != deps.live.model || s.thinkingLevel != agentcore.ThinkingLow {
		t.Errorf("empty object must inherit, got model=%q thinking=%q", s.model, s.thinkingLevel)
	}
	if warn.Len() != 0 {
		t.Errorf("empty object must not warn, got %q", warn.String())
	}
}

// TestBtwConfigThinkingOverride verifies a valid thinkingLevel is applied while
// the model still inherits (partial config falls back per-field).
func TestBtwConfigThinkingOverride(t *testing.T) {
	withBtwConfig(t, `{"thinkingLevel":"high"}`)
	deps, _ := newTestDeps(t, &replProvider{reply: "x"})
	deps.live.thinkingLevel = agentcore.ThinkingLow

	var warn bytes.Buffer
	s := resolveBtwSettings(&warn, &deps)
	if s.thinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("expected thinkingLevel override 'high', got %q", s.thinkingLevel)
	}
	if s.model != deps.live.model {
		t.Errorf("model must still inherit when only thinkingLevel is set, got %q", s.model)
	}
	if warn.Len() != 0 {
		t.Errorf("valid override must not warn, got %q", warn.String())
	}
}

// TestBtwConfigInvalidThinkingWarnsAndFallsBack verifies an invalid thinkingLevel
// warns on one line and keeps the session value.
func TestBtwConfigInvalidThinkingWarnsAndFallsBack(t *testing.T) {
	withBtwConfig(t, `{"thinkingLevel":"bogus"}`)
	deps, _ := newTestDeps(t, &replProvider{reply: "x"})
	deps.live.thinkingLevel = agentcore.ThinkingMedium

	var warn bytes.Buffer
	s := resolveBtwSettings(&warn, &deps)
	if s.thinkingLevel != agentcore.ThinkingMedium {
		t.Errorf("invalid thinkingLevel must fall back to session value, got %q", s.thinkingLevel)
	}
	if !strings.Contains(warn.String(), "thinkingLevel") {
		t.Errorf("expected a warning about the invalid thinkingLevel, got %q", warn.String())
	}
}

// TestBtwConfigMalformedWarnsAndInherits verifies a malformed JSON file warns
// once and inherits every field (never crashes /btw).
func TestBtwConfigMalformedWarnsAndInherits(t *testing.T) {
	withBtwConfig(t, `{not json`)
	deps, _ := newTestDeps(t, &replProvider{reply: "x"})
	deps.live.thinkingLevel = agentcore.ThinkingLow

	var warn bytes.Buffer
	s := resolveBtwSettings(&warn, &deps)
	if s.model != deps.live.model || s.thinkingLevel != agentcore.ThinkingLow {
		t.Errorf("malformed config must inherit, got model=%q thinking=%q", s.model, s.thinkingLevel)
	}
	if !strings.Contains(warn.String(), "invalid btw.json") {
		t.Errorf("expected a malformed-config warning, got %q", warn.String())
	}
}

// TestBtwConfigDoesNotMutateSession verifies resolveBtwSettings never mutates
// deps.live, so a /btw override cannot leak into the main session (FR-8).
func TestBtwConfigDoesNotMutateSession(t *testing.T) {
	withBtwConfig(t, `{"thinkingLevel":"xhigh"}`)
	deps, _ := newTestDeps(t, &replProvider{reply: "x"})
	deps.live.thinkingLevel = agentcore.ThinkingLow

	_ = resolveBtwSettings(&bytes.Buffer{}, &deps)
	if deps.live.thinkingLevel != agentcore.ThinkingLow {
		t.Errorf("session thinkingLevel must be unchanged by /btw config, got %q", deps.live.thinkingLevel)
	}
}
