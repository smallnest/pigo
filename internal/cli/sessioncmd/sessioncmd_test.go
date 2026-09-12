// Tests for the `pigo session` subcommand dispatch (issue #570): the export
// path redacts by default against a real store, the file-format inference, and
// the usage/validation error paths.
package sessioncmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

func seedSession(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)
	store, err := session.NewStore(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	header := session.SessionHeader{ID: "20260912-120000-x", CreatedAt: now, UpdatedAt: now, Model: "m", Provider: "p"}
	msgs := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser,
		Content: agentcore.ContentList{agentcore.NewTextContent("leak me: sk-" + strings.Repeat("l", 24))}}}
	if err := store.Save(header, msgs); err != nil {
		t.Fatal(err)
	}
	return home, header.ID
}

// TestRunExportRedactsByDefault drives the CLI path end to end: the exported
// file must not contain the secret material even though the session does.
func TestRunExportRedactsByDefault(t *testing.T) {
	_, id := seedSession(t)
	out := filepath.Join(t.TempDir(), "share.md")

	var stdout, stderr bytes.Buffer
	code := Run("export", []string{id, "--output", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-"+"lllll") {
		t.Fatalf("export leaks the secret:\n%s", raw)
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Errorf("export missing redaction placeholder:\n%s", raw)
	}
}

// TestRunExportStdoutJSONLRequiresNoRedact verifies the pipeline-friendly
// stdout path and the redacted-JSONL guard.
func TestRunExportStdoutJSONLRequiresNoRedact(t *testing.T) {
	_, id := seedSession(t)

	var stdout, stderr bytes.Buffer
	if code := Run("export", []string{id, "--format", "json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("redacted jsonl exit = %d, want 2", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run("export", []string{id, "--format", "json", "--no-redact"}, &stdout, &stderr); code != 0 {
		t.Fatalf("no-redact jsonl exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sk-"+"lllll") {
		t.Error("--no-redact jsonl should keep the raw transcript for archival")
	}
}

// TestRunExportUsageErrors verifies the usage guardrails: unknown subcommand,
// missing id, unknown flag.
func TestRunExportUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cases := [][]string{
		{},
		{"--format", "md"},
		{"some-id", "--bogus"},
	}
	for _, args := range cases {
		if code := Run("export", args, &stdout, &stderr); code != 2 {
			t.Errorf("args %v exit = %d, want 2", args, code)
		}
	}
	if code := Run("frobnicate", nil, &stdout, &stderr); code != 2 {
		t.Errorf("unknown subcommand exit = %d, want 2", code)
	}
}
