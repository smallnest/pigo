package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runRead(t *testing.T, tool *ReadTool, args map[string]any) (AgentToolResult, bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, gerr := tool.Execute(context.Background(), "call-1", raw, nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	return res, false
}

func resultText(res AgentToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestReadToolBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("line one\nline two\nline three\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &ReadTool{Root: dir}
	res, _ := runRead(t, tool, map[string]any{"path": "hello.txt"})
	text := resultText(res)
	if !strings.Contains(text, "line one") || !strings.Contains(text, "line three") {
		t.Errorf("missing content: %q", text)
	}
	// Line numbers present.
	if !strings.Contains(text, "1\tline one") || !strings.Contains(text, "3\tline three") {
		t.Errorf("missing line numbers: %q", text)
	}
}

func TestReadToolOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		sb.WriteString("row\n")
	}
	path := filepath.Join(dir, "rows.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &ReadTool{Root: dir}
	res, _ := runRead(t, tool, map[string]any{"path": "rows.txt", "offset": 3, "limit": 2})
	text := resultText(res)
	// Should include line numbers 3 and 4, not 1,2,5.
	if !strings.Contains(text, "3\trow") || !strings.Contains(text, "4\trow") {
		t.Errorf("offset/limit window wrong: %q", text)
	}
	if strings.Contains(text, "2\trow") || strings.Contains(text, "5\trow") {
		t.Errorf("offset/limit leaked outside window: %q", text)
	}
}

func TestReadToolMissingFile(t *testing.T) {
	tool := &ReadTool{Root: t.TempDir()}
	res, _ := runRead(t, tool, map[string]any{"path": "nope.txt"})
	if !strings.Contains(resultText(res), "does not exist") {
		t.Errorf("expected does-not-exist error, got %q", resultText(res))
	}
}

func TestReadToolPathTraversal(t *testing.T) {
	dir := t.TempDir()
	// A secret sits outside the root.
	parent := filepath.Dir(dir)
	secret := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	defer os.Remove(secret)

	tool := &ReadTool{Root: dir}
	res, _ := runRead(t, tool, map[string]any{"path": "../secret.txt"})
	text := resultText(res)
	if strings.Contains(text, "top secret") {
		t.Fatal("path traversal escaped the root!")
	}
	if !strings.Contains(text, "outside the workspace root") {
		t.Errorf("expected boundary error, got %q", text)
	}
}

func TestReadToolDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tool := &ReadTool{Root: dir}
	res, _ := runRead(t, tool, map[string]any{"path": "subdir"})
	if !strings.Contains(resultText(res), "is a directory") {
		t.Errorf("expected directory error, got %q", resultText(res))
	}
}

func TestReadToolTruncation(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 0; i < readToolMaxLines+50; i++ {
		sb.WriteString("x\n")
	}
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &ReadTool{Root: dir}
	res, _ := runRead(t, tool, map[string]any{"path": "big.txt"})
	if !strings.Contains(resultText(res), "output truncated") {
		t.Error("expected truncation notice for oversized file")
	}
}

func TestReadToolMissingPathArg(t *testing.T) {
	tool := &ReadTool{Root: t.TempDir()}
	res, _ := runRead(t, tool, map[string]any{})
	if !strings.Contains(resultText(res), "path is required") {
		t.Errorf("expected path-required error, got %q", resultText(res))
	}
}

func TestReadToolSchemaAndMode(t *testing.T) {
	tool := &ReadTool{}
	if tool.Name() != "read" {
		t.Errorf("name = %q", tool.Name())
	}
	if tool.ExecutionMode() != ToolExecutionParallel {
		t.Errorf("read should be parallel")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Errorf("schema not valid JSON: %v", err)
	}
}
