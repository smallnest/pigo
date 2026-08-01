package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/memory"
)

// newMemoryStoreWithCorpus opens a *memory.Store over a temp DB + temp mimo root
// and writes a couple of .md files under the layout. It does NOT reconcile — the
// tool's ReconcileFirst=true is expected to index them lazily on first search.
func newMemoryStoreWithCorpus(t *testing.T) *memory.Store {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "mimo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	writeMemFile(t, root, "permission deadlock encountered during checkpoint save then retry succeeded",
		"projects", "proj1", "notes", "rare.md")
	writeMemFile(t, root, "unrelated grocery shopping list",
		"global", "user", "u1.md")

	dbPath := filepath.Join(base, "sub", "memory.db")
	st, err := memory.Open(dbPath, root, "")
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func writeMemFile(t *testing.T, root, body string, segs ...string) string {
	t.Helper()
	full := filepath.Join(append([]string{root}, segs...)...)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %q: %v", full, err)
	}
	return filepath.Clean(full)
}

func runMemorySearch(t *testing.T, tool *MemorySearchTool, args map[string]any) (string, any) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := tool.Execute(context.Background(), "call-1", raw, nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return contentText(res.Content), res.Details
}

// contentText concatenates the text of every TextContent block in a result.
func contentText(content agentcore.ContentList) string {
	var b strings.Builder
	for _, c := range content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMemorySearchToolInterface(t *testing.T) {
	tool := &MemorySearchTool{}
	if tool.Name() != "memory_search" {
		t.Fatalf("Name = %q, want memory_search", tool.Name())
	}
	if tool.Description() == "" {
		t.Fatal("Description must not be empty")
	}
	// Schema must be valid JSON declaring query as required.
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("Schema is not valid JSON: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "query" {
		t.Fatalf("Schema required = %v, want [query]", schema.Required)
	}
}

func TestMemorySearchFindsSnippet(t *testing.T) {
	tool := &MemorySearchTool{Store: newMemoryStoreWithCorpus(t)}

	text, details := runMemorySearch(t, tool, map[string]any{"query": "permission deadlock"})

	if !strings.Contains(text, "rare.md") {
		t.Fatalf("expected result text to reference rare.md, got:\n%s", text)
	}
	if !strings.Contains(strings.ToLower(text), "permission") {
		t.Fatalf("expected snippet to mention 'permission', got:\n%s", text)
	}

	// Details must carry the structured results (lazy reconcile indexed the file).
	results, ok := details.([]memory.SearchResult)
	if !ok {
		t.Fatalf("Details type = %T, want []memory.SearchResult", details)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one structured result")
	}
	found := false
	for _, r := range results {
		if strings.HasSuffix(r.Path, filepath.Join("notes", "rare.md")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected rare.md among structured results, got %+v", results)
	}
}

func TestMemorySearchScopeAndTypeFilter(t *testing.T) {
	tool := &MemorySearchTool{Store: newMemoryStoreWithCorpus(t)}

	// Filter to the global/user doc; the projects/notes doc must be excluded even
	// though it also matches the shared word.
	text, _ := runMemorySearch(t, tool, map[string]any{
		"query": "grocery permission",
		"scope": "global",
		"type":  "user",
	})
	if strings.Contains(text, "rare.md") {
		t.Fatalf("scope/type filter should exclude rare.md, got:\n%s", text)
	}
	if !strings.Contains(text, "u1.md") {
		t.Fatalf("expected u1.md to match global/user filter, got:\n%s", text)
	}
}

func TestMemorySearchNoResults(t *testing.T) {
	tool := &MemorySearchTool{Store: newMemoryStoreWithCorpus(t)}
	text, _ := runMemorySearch(t, tool, map[string]any{"query": "zzzznonexistenttoken"})
	if !strings.Contains(text, "no results") {
		t.Fatalf("expected a clear empty message, got:\n%s", text)
	}
}

func TestMemorySearchEmptyQueryNoOp(t *testing.T) {
	tool := &MemorySearchTool{Store: newMemoryStoreWithCorpus(t)}
	text, _ := runMemorySearch(t, tool, map[string]any{"query": "   "})
	if !strings.Contains(text, "empty query") {
		t.Fatalf("expected empty-query no-op message, got:\n%s", text)
	}
}

func TestMemorySearchNilStoreNoOp(t *testing.T) {
	tool := &MemorySearchTool{} // Store nil
	text, _ := runMemorySearch(t, tool, map[string]any{"query": "anything"})
	if !strings.Contains(text, "no memory store") {
		t.Fatalf("expected nil-store no-op message, got:\n%s", text)
	}
}

func TestMemorySearchInvalidArgs(t *testing.T) {
	tool := &MemorySearchTool{Store: newMemoryStoreWithCorpus(t)}
	res, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"query": 123}`), nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	var text strings.Builder
	text.WriteString(contentText(res.Content))
	if !strings.Contains(text.String(), "invalid arguments") {
		t.Fatalf("expected invalid-arguments error result, got:\n%s", text.String())
	}
}
