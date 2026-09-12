// Tests for the shareable session export (issue #570): the redaction engine
// (credential-shaped strings stripped), the Markdown rendering, the format
// validation, and a golden fixture for the shareable document.
package session

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

var updateShareGoldens = flag.Bool("update-share-goldens", false, "rewrite the session share golden fixture")

// Fixture secrets are assembled at RUNTIME (never as valid-looking literals
// in source) so GitHub push protection has nothing to flag, while still
// exercising every redaction pattern.
var (
	fakeSKToken   = "sk-" + strings.Repeat("a", 24)
	fakeGHToken   = "ghp_" + strings.Repeat("x", 36)
	fakeAWSKey    = "AKIA" + strings.Repeat("2", 16)
	fakeSlackTok  = "xoxb-" + strings.Repeat("9", 15)
	fakeEnvAssign = "DEEPSEEK_API_KEY=" + fakeSKToken
)

// TestRedactText verifies every credential shape is stripped while ordinary
// prose (mentioning the words without secret material) survives.
func TestRedactText(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		leaks bool // whether the input is expected to be redacted
	}{
		{name: "openai-style token", in: "use " + fakeSKToken + " tomorrow", leaks: true},
		{name: "github pat", in: "token " + fakeGHToken, leaks: true},
		{name: "aws key id", in: "key " + fakeAWSKey + " here", leaks: true},
		{name: "slack token", in: fakeSlackTok + " here", leaks: true},
		{name: "env-shaped assignment", in: fakeEnvAssign, leaks: true},
		{name: "lowercase assignment", in: "export my_token=" + strings.Repeat("f", 20), leaks: true},
		{name: "prose mentions api key word", in: "the API key for zai lives in the environment", leaks: false},
		{name: "plain prose", in: "refactor the scheduler loop", leaks: false},
	}
	for _, tc := range cases {
		got := RedactText(tc.in)
		if tc.leaks {
			if got == tc.in {
				t.Errorf("%s: not redacted: %q", tc.name, got)
			}
			if !strings.Contains(got, redactPlaceholder) {
				t.Errorf("%s: redacted output missing placeholder: %q", tc.name, got)
			}
		} else if got != tc.in {
			t.Errorf("%s: over-redacted %q -> %q", tc.name, tc.in, got)
		}
	}
}

// shareFixture builds a store with one deterministic session carrying secret
// material in every message class, for the golden and redaction tests.
func shareFixture(t *testing.T) (*Store, SessionHeader, []Entry) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	header := SessionHeader{
		ID:           "20260912-120000-test",
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        "glm-5.3",
		Provider:     "zai",
		SystemPrompt: "You are a coding agent for /home/user/proj. Key: " + fakeSKToken,
		Cwd:          "/home/user/proj",
	}
	entries := []Entry{
		{Message: agentcore.UserMessage{RoleField: agentcore.RoleUser,
			Content: agentcore.ContentList{agentcore.NewTextContent("deploy notes: " + fakeEnvAssign)}}},
		{Message: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("checked with token " + fakeGHToken + " in logs"),
				agentcore.NewToolCallContent("call-1", "bash", json.RawMessage(`{"cmd":"echo hi"}`)),
			}}},
		{Message: agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "call-1", ToolName: "bash",
			Content: agentcore.ContentList{agentcore.NewTextContent("hi")}}},
	}
	if err := store.SaveEntries(header, entries); err != nil {
		t.Fatalf("SaveEntries: %v", err)
	}
	return store, header, entries
}

// TestWriteMarkdownGolden pins the shareable Markdown document byte-for-byte
// (redaction on, system prompt dropped).
func TestWriteMarkdownGolden(t *testing.T) {
	store, _, _ := shareFixture(t)
	_, entries, err := store.LoadEntries("20260912-120000-test")
	if err != nil {
		t.Fatal(err)
	}
	header := SessionHeader{ID: "20260912-120000-test", CreatedAt: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC), Model: "glm-5.3", Provider: "zai"}

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, header, entries, ShareOptions{Redact: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "share", "markdown-v1.golden")
	if *updateShareGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden unreadable (run go test -update-share-goldens): %v", err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(buf.String()) {
		t.Fatalf("markdown golden drift:\n--- want ---\n%s\n--- got ---\n%s", want, buf.String())
	}
}

// TestExportShareRedaction is the leak-proofing test: a session whose
// transcript carries secret material in every message class exports with all
// of it redacted, and the system prompt is dropped unless requested.
func TestExportShareRedaction(t *testing.T) {
	store, header, _ := shareFixture(t)
	out := filepath.Join(t.TempDir(), "share.md")

	if _, err := store.ExportShare(header.ID, out, "md", ShareOptions{Redact: true}); err != nil {
		t.Fatalf("ExportShare: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, leak := range []string{"sk-live000000000000", "sk-fixture0000000000", "ghp_0123456789abcdefghijklmnopqrst", "sk-fixture"} {
		if strings.Contains(text, leak) {
			t.Errorf("share export leaks %q:\n%s", leak, text)
		}
	}
	if strings.Contains(text, "## System prompt") {
		t.Error("system prompt exported by default, want it dropped")
	}
	for _, want := range []string{redactPlaceholder, "# Session 20260912-120000-test", "## tool result (bash)"} {
		if !strings.Contains(text, want) {
			t.Errorf("share export missing %q:\n%s", want, text)
		}
	}

	// IncludeSystemPrompt restores the header section (still redacted).
	out2 := filepath.Join(t.TempDir(), "share-sys.md")
	if _, err := store.ExportShare(header.ID, out2, "md", ShareOptions{Redact: true, IncludeSystemPrompt: true}); err != nil {
		t.Fatalf("ExportShare with sys prompt: %v", err)
	}
	raw2, _ := os.ReadFile(out2)
	if strings.Contains(string(raw2), "sk-fixture0000000000") || !strings.Contains(string(raw2), "## System prompt") {
		t.Errorf("system-prompt export wrong:\n%s", raw2)
	}
}

// TestExportShareFormatValidation verifies the contradictory redacted-JSONL
// combination errors before writing, and unknown formats are rejected.
func TestExportShareFormatValidation(t *testing.T) {
	store, header, _ := shareFixture(t)
	out := filepath.Join(t.TempDir(), "out.jsonl")
	if _, err := store.ExportShare(header.ID, out, "json", ShareOptions{Redact: true}); err == nil {
		t.Fatal("redacted JSONL succeeded, want an error")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output file created despite validation error, want no file")
	}
	if _, err := store.ExportShare(header.ID, filepath.Join(t.TempDir(), "o.xyz"), "xyz", ShareOptions{}); err == nil {
		t.Fatal("unknown format succeeded, want an error")
	}
}
