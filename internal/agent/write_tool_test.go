package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runWrite(t *testing.T, tool *WriteTool, args map[string]any) AgentToolResult {
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

func TestWriteToolCreate(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Root: dir}
	res := runWrite(t, tool, map[string]any{"path": "out.txt", "content": "hello"})
	if !strings.Contains(resultText(res), "Created") {
		t.Errorf("expected Created, got %q", resultText(res))
	}
	got, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("file content = %q, err = %v", got, err)
	}
}

func TestWriteToolCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Root: dir}
	res := runWrite(t, tool, map[string]any{"path": "a/b/c/deep.txt", "content": "x"})
	if strings.Contains(resultText(res), "error") {
		t.Errorf("unexpected error: %q", resultText(res))
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b", "c", "deep.txt")); err != nil {
		t.Errorf("nested file not created: %v", err)
	}
}

func TestWriteToolOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := &WriteTool{Root: dir}
	res := runWrite(t, tool, map[string]any{"path": "exists.txt", "content": "new"})
	if !strings.Contains(resultText(res), "Overwrote") {
		t.Errorf("expected Overwrote, got %q", resultText(res))
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}
	// Details should flag the overwrite.
	details, ok := res.Details.(map[string]any)
	if !ok || details["overwrote"] != true {
		t.Errorf("details missing overwrote flag: %+v", res.Details)
	}
}

func TestWriteToolPathTraversal(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Root: dir}
	res := runWrite(t, tool, map[string]any{"path": "../escape.txt", "content": "x"})
	if !strings.Contains(resultText(res), "outside the workspace root") {
		t.Errorf("expected boundary error, got %q", resultText(res))
	}
	// The escape file must not exist.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); err == nil {
		t.Fatal("path traversal wrote outside the root!")
	}
}

func TestWriteToolDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "adir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tool := &WriteTool{Root: dir}
	res := runWrite(t, tool, map[string]any{"path": "adir", "content": "x"})
	if !strings.Contains(resultText(res), "is a directory") {
		t.Errorf("expected directory error, got %q", resultText(res))
	}
}

func TestWriteToolMissingArgs(t *testing.T) {
	tool := &WriteTool{Root: t.TempDir()}
	res := runWrite(t, tool, map[string]any{"content": "x"})
	if !strings.Contains(resultText(res), "path is required") {
		t.Errorf("expected path-required error, got %q", resultText(res))
	}
}

func TestWriteToolMode(t *testing.T) {
	tool := &WriteTool{}
	if tool.Name() != "write" {
		t.Errorf("name = %q", tool.Name())
	}
	if tool.ExecutionMode() != ToolExecutionSequential {
		t.Error("write should be sequential")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Errorf("schema not valid JSON: %v", err)
	}
}
