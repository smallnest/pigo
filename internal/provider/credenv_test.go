package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScrubsCredentialVar exercises the name-shape rules: suffix matching,
// substring matching, the PIGO_ prefix, and ordinary names that must survive.
func TestScrubsCredentialVar(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"DEEPSEEK_API_KEY", true},
		{"anthropic_api_key", true}, // case-insensitive
		{"OPENAI_APIKEY", true},
		{"ANTHROPIC_AUTH_TOKEN", true},
		{"AWS_SECRET_ACCESS_KEY", true},
		{"GH_TOKEN", true},
		{"DB_PASSWORD", true},
		{"MY_CREDENTIALS_FILE", true},
		{"PIGO_HOME", true},
		{"pigo_session", true},
		{"SERVER_PRIVATE_KEY", true},
		{"PATH", false},
		{"HOME", false},
		{"LANG", false},
		{"EDITOR", false},
		{"GATEWAY_TOKEN_FILE", true}, // token substring, conservative
	}
	for _, tc := range cases {
		if got := ScrubsCredentialVar(tc.name); got != tc.want {
			t.Errorf("ScrubsCredentialVar(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestScrubEnv verifies the derived child environment: credential-shaped and
// PIGO_* entries dropped, ordinary entries kept, malformed entries dropped,
// and allow entries re-injected last in deterministic order.
func TestScrubEnv(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/user",
		"DEEPSEEK_API_KEY=sk-secret",
		"pigo_session=abc",
		"PIGO_THINKING_LEVEL=high",
		"MY_DB_PASSWORD=hunter2",
		"LANG=en_US.UTF-8",
		"malformed-entry",
	}
	got := ScrubEnv(parent, map[string]string{"DEEPSEEK_API_KEY": "sk-child"})

	joined := strings.Join(got, "\n")
	for _, banned := range []string{"sk-secret", "hunter2", "PIGO_", "pigo_session", "malformed"} {
		if strings.Contains(joined, banned) {
			t.Errorf("scrubbed env leaks %q:\n%s", banned, joined)
		}
	}
	for _, want := range []string{"PATH=/usr/bin:/bin", "HOME=/home/user", "LANG=en_US.UTF-8", "DEEPSEEK_API_KEY=sk-child"} {
		if !strings.Contains(joined, want) {
			t.Errorf("scrubbed env missing %q:\n%s", want, joined)
		}
	}
	// The allow entry must come last and in deterministic (sorted) order.
	if got[len(got)-1] != "DEEPSEEK_API_KEY=sk-child" {
		t.Errorf("last entry = %q, want the allowed override", got[len(got)-1])
	}
}

// TestCredentialFileRoundTrip covers LoadCredentialFile and
// ResolveCredentialReference: found names, missing names, malformed YAML, and
// the permission warning heuristic.
func TestCredentialFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.yaml")
	body := "deepseek-main: sk-aaa\nzai-coding: sk-bbb\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := ResolveCredentialReference(path, "deepseek-main")
	if err != nil || key != "sk-aaa" {
		t.Fatalf("ResolveCredentialReference = (%q, %v), want sk-aaa", key, err)
	}
	if _, err := ResolveCredentialReference(path, "missing-name"); err == nil {
		t.Error("missing credential name succeeded, want error")
	}
	if CredentialFilePermissionsWarn(path) {
		t.Error("0600 file flagged as permissive, want no warning")
	}

	// A group/other-readable file must raise the warning.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if !CredentialFilePermissionsWarn(path) {
		t.Error("0644 file not flagged, want warning")
	}

	// Malformed YAML surfaces as an error naming the file.
	if err := os.WriteFile(path, []byte(":: not a mapping\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredentialReference(path, "deepseek-main"); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("malformed file error = %v, want it to name the file", err)
	}

	// An empty reference name is rejected before touching the file.
	if _, err := ResolveCredentialReference(path, "  "); err == nil {
		t.Error("empty reference name succeeded, want error")
	}
}
