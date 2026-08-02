package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeUpdateCache seeds the selfupdate cache under a temp PIGO_HOME so the
// banner's cached-latest lookup is deterministic.
func writeUpdateCache(t *testing.T, latest string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PIGO_HOME", dir)
	data, _ := json.Marshal(map[string]any{
		"checked_at": time.Now(),
		"latest":     latest,
	})
	if err := os.WriteFile(filepath.Join(dir, "update-check.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenderBannerShowsVersion(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir()) // empty cache: no upgrade hint
	out := renderBanner(DefaultTheme(), Options{Version: "v0.3.1"}, "/tmp/proj")
	if !strings.Contains(out, "Version") || !strings.Contains(out, "v0.3.1") {
		t.Errorf("banner missing Version row: %q", out)
	}
	if strings.Contains(out, "Run pigo update to upgrade") {
		t.Error("banner should not show upgrade hint with empty cache")
	}
}

func TestRenderBannerDevNoHint(t *testing.T) {
	writeUpdateCache(t, "v9.9.9") // even with a newer tag cached...
	out := renderBanner(DefaultTheme(), Options{Version: "dev"}, "/tmp/proj")
	if !strings.Contains(out, "dev") {
		t.Errorf("banner should show dev version: %q", out)
	}
	if strings.Contains(out, "Run pigo update to upgrade") {
		t.Error("dev build must not show an upgrade hint")
	}
}

func TestRenderBannerUpgradeHint(t *testing.T) {
	writeUpdateCache(t, "v0.4.0")
	out := renderBanner(DefaultTheme(), Options{Version: "v0.3.1"}, "/tmp/proj")
	if !strings.Contains(out, "v0.4.0") {
		t.Errorf("banner should highlight newer version v0.4.0: %q", out)
	}
	if !strings.Contains(out, "Run pigo update to upgrade") {
		t.Errorf("banner should show upgrade hint: %q", out)
	}
}

func TestRenderBannerUpToDate(t *testing.T) {
	writeUpdateCache(t, "v0.3.1")
	out := renderBanner(DefaultTheme(), Options{Version: "v0.3.1"}, "/tmp/proj")
	if strings.Contains(out, "Run pigo update to upgrade") {
		t.Error("up-to-date build must not show an upgrade hint")
	}
}

// TestRenderBannerProtocolLabel verifies the Protocol row shows the concrete
// OpenAI wire variant: a bare "openai" is surfaced as "openai/chat" (explicit
// Chat Completions), "openai/resp_api" passes through, and an unset protocol
// falls back to the em dash rather than showing an empty row.
func TestRenderBannerProtocolLabel(t *testing.T) {
	cases := []struct {
		protocol string
		want     string
	}{
		{"openai", "openai/chat"},
		{"openai/chat", "openai/chat"},
		{"openai/resp_api", "openai/resp_api"},
		{"anthropic", "anthropic"},
		{"", "—"},
	}
	for _, c := range cases {
		t.Setenv("PIGO_HOME", t.TempDir())
		out := renderBanner(DefaultTheme(), Options{Version: "dev", Protocol: c.protocol}, "/tmp/proj")
		if !strings.Contains(out, c.want) {
			t.Errorf("protocol %q: banner should show %q, got: %q", c.protocol, c.want, out)
		}
	}
}
