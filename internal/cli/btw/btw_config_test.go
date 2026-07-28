package btw

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
	"github.com/smallnest/pigo/internal/cli"
)

// fakeHost satisfies cli.Host by embedding the interface (so every method is
// present) while overriding only Live(), the sole accessor ResolveBtwSettings
// reads. The embedded nil interface would panic if any other method were
// called, which these tests never do.
type fakeHost struct {
	cli.Host
	live *cli.LiveConfig
}

func (f fakeHost) Live() *cli.LiveConfig { return f.live }

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
	live := &cli.LiveConfig{Model: "sess-model", ProviderName: "sess-prov", ThinkingLevel: agentcore.ThinkingMedium}
	host := fakeHost{live: live}

	var warn bytes.Buffer
	s := ResolveBtwSettings(&warn, host)
	if s.Model != live.Model || s.ProviderName != live.ProviderName {
		t.Errorf("absent config must inherit model/provider, got %q/%q", s.Model, s.ProviderName)
	}
	if s.ThinkingLevel != agentcore.ThinkingMedium {
		t.Errorf("absent config must inherit thinkingLevel, got %q", s.ThinkingLevel)
	}
	if warn.Len() != 0 {
		t.Errorf("absent config must not warn, got %q", warn.String())
	}
}

// TestBtwConfigEmptyObjectInherits verifies that an empty JSON object inherits
// everything without warning.
func TestBtwConfigEmptyObjectInherits(t *testing.T) {
	withBtwConfig(t, "{}")
	live := &cli.LiveConfig{Model: "sess-model", ThinkingLevel: agentcore.ThinkingLow}
	host := fakeHost{live: live}

	var warn bytes.Buffer
	s := ResolveBtwSettings(&warn, host)
	if s.Model != live.Model || s.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("empty object must inherit, got model=%q thinking=%q", s.Model, s.ThinkingLevel)
	}
	if warn.Len() != 0 {
		t.Errorf("empty object must not warn, got %q", warn.String())
	}
}

// TestBtwConfigThinkingOverride verifies a valid thinkingLevel is applied while
// the model still inherits (partial config falls back per-field).
func TestBtwConfigThinkingOverride(t *testing.T) {
	withBtwConfig(t, `{"thinkingLevel":"high"}`)
	live := &cli.LiveConfig{Model: "sess-model", ThinkingLevel: agentcore.ThinkingLow}
	host := fakeHost{live: live}

	var warn bytes.Buffer
	s := ResolveBtwSettings(&warn, host)
	if s.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("expected thinkingLevel override 'high', got %q", s.ThinkingLevel)
	}
	if s.Model != live.Model {
		t.Errorf("model must still inherit when only thinkingLevel is set, got %q", s.Model)
	}
	if warn.Len() != 0 {
		t.Errorf("valid override must not warn, got %q", warn.String())
	}
}

// TestBtwConfigInvalidThinkingWarnsAndFallsBack verifies an invalid thinkingLevel
// warns on one line and keeps the session value.
func TestBtwConfigInvalidThinkingWarnsAndFallsBack(t *testing.T) {
	withBtwConfig(t, `{"thinkingLevel":"bogus"}`)
	live := &cli.LiveConfig{Model: "sess-model", ThinkingLevel: agentcore.ThinkingMedium}
	host := fakeHost{live: live}

	var warn bytes.Buffer
	s := ResolveBtwSettings(&warn, host)
	if s.ThinkingLevel != agentcore.ThinkingMedium {
		t.Errorf("invalid thinkingLevel must fall back to session value, got %q", s.ThinkingLevel)
	}
	if !strings.Contains(warn.String(), "thinkingLevel") {
		t.Errorf("expected a warning about the invalid thinkingLevel, got %q", warn.String())
	}
}

// TestBtwConfigMalformedWarnsAndInherits verifies a malformed JSON file warns
// once and inherits every field (never crashes /btw).
func TestBtwConfigMalformedWarnsAndInherits(t *testing.T) {
	withBtwConfig(t, `{not json`)
	live := &cli.LiveConfig{Model: "sess-model", ThinkingLevel: agentcore.ThinkingLow}
	host := fakeHost{live: live}

	var warn bytes.Buffer
	s := ResolveBtwSettings(&warn, host)
	if s.Model != live.Model || s.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("malformed config must inherit, got model=%q thinking=%q", s.Model, s.ThinkingLevel)
	}
	if !strings.Contains(warn.String(), "invalid btw.json") {
		t.Errorf("expected a malformed-config warning, got %q", warn.String())
	}
}

// TestBtwConfigDoesNotMutateSession verifies ResolveBtwSettings never mutates
// the session live config, so a /btw override cannot leak into the main session
// (FR-8).
func TestBtwConfigDoesNotMutateSession(t *testing.T) {
	withBtwConfig(t, `{"thinkingLevel":"xhigh"}`)
	live := &cli.LiveConfig{Model: "sess-model", ThinkingLevel: agentcore.ThinkingLow}
	host := fakeHost{live: live}

	_ = ResolveBtwSettings(&bytes.Buffer{}, host)
	if live.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("session thinkingLevel must be unchanged by /btw config, got %q", live.ThinkingLevel)
	}
}
