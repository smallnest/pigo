package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runEdit(t *testing.T, tool *EditTool, args map[string]any) AgentToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, gerr := tool.Execute(context.Background(), "call-1", raw, nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	return res
}

func seedFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return p
}

func TestEditToolUniqueMatch(t *testing.T) {
	dir := t.TempDir()
	p := seedFile(t, dir, "f.txt", "alpha\nbeta\ngamma\n")
	tool := &EditTool{Root: dir}
	res := runEdit(t, tool, map[string]any{"path": "f.txt", "old_string": "beta", "new_string": "BETA"})
	if strings.Contains(resultText(res), "not found") || strings.Contains(resultText(res), "not unique") {
		t.Fatalf("unexpected error: %q", resultText(res))
	}
	got, _ := os.ReadFile(p)
	if string(got) != "alpha\nBETA\ngamma\n" {
		t.Errorf("content = %q", got)
	}
	// Diff present.
	if !strings.Contains(resultText(res), "-beta") || !strings.Contains(resultText(res), "+BETA") {
		t.Errorf("diff missing markers: %q", resultText(res))
	}
}

func TestEditToolNonUniqueErrors(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "x\nx\nx\n")
	tool := &EditTool{Root: dir}
	res := runEdit(t, tool, map[string]any{"path": "f.txt", "old_string": "x", "new_string": "y"})
	if !strings.Contains(resultText(res), "not unique") {
		t.Errorf("expected non-unique error, got %q", resultText(res))
	}
	// File unchanged.
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(got) != "x\nx\nx\n" {
		t.Errorf("file should be unchanged, got %q", got)
	}
}

func TestEditToolReplaceAll(t *testing.T) {
	dir := t.TempDir()
	p := seedFile(t, dir, "f.txt", "x\nx\nx\n")
	tool := &EditTool{Root: dir}
	res := runEdit(t, tool, map[string]any{"path": "f.txt", "old_string": "x", "new_string": "y", "replace_all": true})
	got, _ := os.ReadFile(p)
	if string(got) != "y\ny\ny\n" {
		t.Errorf("content = %q, want all replaced", got)
	}
	details, ok := res.Details.(map[string]any)
	if !ok || details["replacements"] != 3 {
		t.Errorf("expected 3 replacements, details = %+v", res.Details)
	}
}

func TestEditToolNotFound(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "hello\n")
	tool := &EditTool{Root: dir}
	res := runEdit(t, tool, map[string]any{"path": "f.txt", "old_string": "missing", "new_string": "x"})
	if !strings.Contains(resultText(res), "not found") {
		t.Errorf("expected not-found error, got %q", resultText(res))
	}
}

func TestEditToolMissingFile(t *testing.T) {
	tool := &EditTool{Root: t.TempDir()}
	res := runEdit(t, tool, map[string]any{"path": "nope.txt", "old_string": "a", "new_string": "b"})
	if !strings.Contains(resultText(res), "does not exist") {
		t.Errorf("expected does-not-exist, got %q", resultText(res))
	}
}

func TestEditToolIdenticalStrings(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "a\n")
	tool := &EditTool{Root: dir}
	res := runEdit(t, tool, map[string]any{"path": "f.txt", "old_string": "a", "new_string": "a"})
	if !strings.Contains(resultText(res), "identical") {
		t.Errorf("expected identical error, got %q", resultText(res))
	}
}

func TestEditToolPathTraversal(t *testing.T) {
	dir := t.TempDir()
	tool := &EditTool{Root: dir}
	res := runEdit(t, tool, map[string]any{"path": "../x.txt", "old_string": "a", "new_string": "b"})
	if !strings.Contains(resultText(res), "outside the workspace root") {
		t.Errorf("expected boundary error, got %q", resultText(res))
	}
}

func TestEditToolMode(t *testing.T) {
	tool := &EditTool{}
	if tool.Name() != "edit" {
		t.Errorf("name = %q", tool.Name())
	}
	if tool.ExecutionMode() != ToolExecutionSequential {
		t.Error("edit should be sequential")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Errorf("schema not valid JSON: %v", err)
	}
}

func TestUnifiedDiff(t *testing.T) {
	diff := unifiedDiff("f.txt", "a\nb\nc\n", "a\nB\nc\n")
	if !strings.Contains(diff, "--- a/f.txt") || !strings.Contains(diff, "+++ b/f.txt") {
		t.Errorf("missing header: %q", diff)
	}
	if !strings.Contains(diff, "-b") || !strings.Contains(diff, "+B") {
		t.Errorf("missing change lines: %q", diff)
	}
	// Unchanged context lines carry a leading space.
	if !strings.Contains(diff, " a") || !strings.Contains(diff, " c") {
		t.Errorf("missing context lines: %q", diff)
	}
}
