package session

// Tests for session export/import (US-008, #124). They cover the lossless
// JSONL round-trip (export → import preserves the header fields, message
// sequence, and entry tree), the self-contained HTML export (inline styles, no
// external network resources, HTML-escaped content), and format selection by
// file extension.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// seedSession writes a small multi-turn session and returns its header.
func seedSession(t *testing.T, s *Store) SessionHeader {
	t.Helper()
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	header := SessionHeader{
		ID:           NewID(now),
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        "anthropic/claude-opus-4",
		Provider:     "anthropic",
		SystemPrompt: "You are pigo.",
	}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return header
}

// TestExportImportJSONLRoundTrip is the core acceptance check: exporting a
// session to JSONL and importing it back yields the same message sequence and
// header carry-over, with the source id recorded as the import's parent.
func TestExportImportJSONLRoundTrip(t *testing.T) {
	s := newStore(t)
	header := seedSession(t, s)

	out := filepath.Join(t.TempDir(), "export.jsonl")
	n, err := s.Export(header.ID, out)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if n != len(sampleMessages()) {
		t.Errorf("exported %d entries, want %d", n, len(sampleMessages()))
	}

	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	newHeader, entries, err := s.Import(out, now)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if newHeader.ID == header.ID {
		t.Errorf("import must assign a fresh id, got the source id %q", newHeader.ID)
	}
	if newHeader.ParentSession != header.ID {
		t.Errorf("ParentSession = %q, want source id %q", newHeader.ParentSession, header.ID)
	}
	if newHeader.Model != header.Model || newHeader.Provider != header.Provider || newHeader.SystemPrompt != header.SystemPrompt {
		t.Errorf("header fields not carried over: %+v", newHeader)
	}
	if len(entries) != len(sampleMessages()) {
		t.Fatalf("imported %d entries, want %d", len(entries), len(sampleMessages()))
	}

	// The imported session must be independently loadable with the same roles.
	_, gotMsgs, err := s.Load(newHeader.ID)
	if err != nil {
		t.Fatalf("Load imported: %v", err)
	}
	wantRoles := []string{agentcore.RoleUser, agentcore.RoleAssistant, agentcore.RoleToolResult, agentcore.RoleAssistant}
	if len(gotMsgs) != len(wantRoles) {
		t.Fatalf("imported message count = %d, want %d", len(gotMsgs), len(wantRoles))
	}
	for i, m := range gotMsgs {
		if m.Role() != wantRoles[i] {
			t.Errorf("message[%d] role = %q, want %q", i, m.Role(), wantRoles[i])
		}
	}
	// The tool call must survive the round-trip.
	a, ok := gotMsgs[1].(agentcore.AssistantMessage)
	if !ok {
		t.Fatalf("message[1] is not AssistantMessage: %T", gotMsgs[1])
	}
	if calls := a.ToolCalls(); len(calls) != 1 || calls[0].Name != "read" {
		t.Errorf("tool calls = %+v, want one 'read'", calls)
	}
}

// TestWriteReadJSONLPreservesTree checks the lower-level primitives: WriteJSONL
// then ReadJSONL preserves entry ids and parentIds verbatim (so the tree — and
// thus resume/PathToLeaf — behaves identically).
func TestWriteReadJSONLPreservesTree(t *testing.T) {
	s := newStore(t)
	header := seedSession(t, s)
	_, entries, err := s.LoadEntries(header.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}

	var buf bytes.Buffer
	if err := WriteJSONL(&buf, header, entries); err != nil {
		t.Fatalf("WriteJSONL: %v", err)
	}
	gotHeader, gotEntries, err := ReadJSONL(&buf)
	if err != nil {
		t.Fatalf("ReadJSONL: %v", err)
	}
	if gotHeader.Version != SchemaVersion {
		t.Errorf("version = %d, want %d", gotHeader.Version, SchemaVersion)
	}
	if len(gotEntries) != len(entries) {
		t.Fatalf("entry count = %d, want %d", len(gotEntries), len(entries))
	}
	for i := range entries {
		if gotEntries[i].ID != entries[i].ID || gotEntries[i].ParentID != entries[i].ParentID {
			t.Errorf("entry[%d] tree ids changed: got {%q,%q} want {%q,%q}",
				i, gotEntries[i].ID, gotEntries[i].ParentID, entries[i].ID, entries[i].ParentID)
		}
	}
}

// TestExportHTMLSelfContained verifies the HTML export is a self-contained
// document: it carries inline styles, has no external network resources (no
// http(s):// URLs, no <script>, no <link>), and HTML-escapes session content.
func TestExportHTMLSelfContained(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	header := SessionHeader{
		ID:        NewID(now),
		CreatedAt: now,
		UpdatedAt: now,
		Model:     "anthropic/claude-opus-4",
		Provider:  "anthropic",
	}
	// Include a message with HTML-significant characters to exercise escaping.
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("<script>alert('xss')</script>")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("safe & sound")}, StopReason: agentcore.StopReasonEndTurn},
	}
	if err := s.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out := filepath.Join(t.TempDir(), "export.html")
	if _, err := s.Export(header.ID, out); err != nil {
		t.Fatalf("Export html: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	doc := string(data)

	if !strings.Contains(doc, "<style>") {
		t.Error("HTML export must inline a <style> block")
	}
	if strings.Contains(doc, "<script") {
		t.Error("HTML export must not contain <script> tags")
	}
	if strings.Contains(doc, "<link") {
		t.Error("HTML export must not reference external stylesheets via <link>")
	}
	if strings.Contains(doc, "http://") || strings.Contains(doc, "https://") {
		t.Error("HTML export must not reference external network resources (http/https)")
	}
	// The raw script tag from the user message must be escaped, not live.
	if strings.Contains(doc, "<script>alert") {
		t.Error("session content must be HTML-escaped (found live <script>alert)")
	}
	if !strings.Contains(doc, "&lt;script&gt;alert") {
		t.Error("expected escaped user content &lt;script&gt;alert in HTML export")
	}
	if !strings.Contains(doc, "safe &amp; sound") {
		t.Error("expected escaped ampersand in assistant content")
	}
}

// TestImportRejectsHTML verifies importing a non-JSONL file (e.g. an HTML
// export) fails rather than materializing garbage.
func TestImportRejectsHTML(t *testing.T) {
	s := newStore(t)
	header := seedSession(t, s)
	out := filepath.Join(t.TempDir(), "export.html")
	if _, err := s.Export(header.ID, out); err != nil {
		t.Fatalf("Export html: %v", err)
	}
	if _, _, err := s.Import(out, time.Now()); err == nil {
		t.Error("Import of an HTML file should fail, got nil error")
	}
}
