package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runSearch(t *testing.T, tool AgentTool, args map[string]any) AgentToolResult {
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

// seedTree writes a small directory tree with a .gitignore for the search tests.
func seedTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mustWrite("main.go", "package main\nfunc main() { hello() }\n")
	mustWrite("util.go", "package main\nfunc hello() {}\n")
	mustWrite("README.md", "# project\nhello world\n")
	mustWrite("sub/deep.go", "package sub\n// hello from sub\n")
	mustWrite("build/generated.go", "package build\nfunc hello() {}\n")
	mustWrite(".gitignore", "build/\n*.log\n")
	mustWrite("debug.log", "hello log line\n")
	return dir
}

func TestGrepBasic(t *testing.T) {
	dir := seedTree(t)
	tool := &GrepTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"pattern": "hello"})
	txt := resultText(res)
	// Matches in tracked files.
	if !strings.Contains(txt, "main.go") || !strings.Contains(txt, "util.go") {
		t.Errorf("expected go file matches, got %q", txt)
	}
	// .gitignore'd paths must be skipped.
	if strings.Contains(txt, "build/generated.go") {
		t.Errorf("ignored dir should be skipped: %q", txt)
	}
	if strings.Contains(txt, "debug.log") {
		t.Errorf("ignored *.log should be skipped: %q", txt)
	}
}

func TestGrepGlobFilter(t *testing.T) {
	dir := seedTree(t)
	tool := &GrepTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"pattern": "hello", "glob": "*.md"})
	txt := resultText(res)
	if !strings.Contains(txt, "README.md") {
		t.Errorf("expected README match, got %q", txt)
	}
	if strings.Contains(txt, ".go") {
		t.Errorf("glob *.md should exclude .go files: %q", txt)
	}
}

func TestGrepInvalidPattern(t *testing.T) {
	dir := seedTree(t)
	tool := &GrepTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"pattern": "["})
	if !strings.Contains(resultText(res), "invalid pattern") {
		t.Errorf("expected invalid-pattern error, got %q", resultText(res))
	}
}

func TestFindGlob(t *testing.T) {
	dir := seedTree(t)
	tool := &FindTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"glob": "*.go"})
	txt := resultText(res)
	if !strings.Contains(txt, "main.go") || !strings.Contains(txt, "sub/deep.go") {
		t.Errorf("expected go files, got %q", txt)
	}
	if strings.Contains(txt, "build/generated.go") {
		t.Errorf("ignored dir should be skipped: %q", txt)
	}
	if strings.Contains(txt, "README.md") {
		t.Errorf("*.go should not match README.md: %q", txt)
	}
}

func TestLsDistinguishesFilesAndDirs(t *testing.T) {
	dir := seedTree(t)
	tool := &LsTool{Root: dir}
	res := runSearch(t, tool, map[string]any{})
	txt := resultText(res)
	// Directories carry a trailing slash.
	if !strings.Contains(txt, "sub/") {
		t.Errorf("expected sub/ dir marker, got %q", txt)
	}
	if !strings.Contains(txt, "main.go") {
		t.Errorf("expected main.go file, got %q", txt)
	}
	details, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("details missing: %+v", res.Details)
	}
	if details["files"] == nil || details["dirs"] == nil {
		t.Errorf("expected file/dir counts, got %+v", details)
	}
}

func TestLsNotADirectory(t *testing.T) {
	dir := seedTree(t)
	tool := &LsTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"path": "main.go"})
	if !strings.Contains(resultText(res), "not a directory") {
		t.Errorf("expected not-a-directory error, got %q", resultText(res))
	}
}

func TestLsMissing(t *testing.T) {
	dir := seedTree(t)
	tool := &LsTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"path": "nope"})
	if !strings.Contains(resultText(res), "does not exist") {
		t.Errorf("expected does-not-exist error, got %q", resultText(res))
	}
}

func TestSearchPathTraversal(t *testing.T) {
	dir := seedTree(t)
	for _, tc := range []struct {
		name string
		tool AgentTool
		args map[string]any
	}{
		{"grep", &GrepTool{Root: dir}, map[string]any{"pattern": "x", "path": "../"}},
		{"find", &FindTool{Root: dir}, map[string]any{"glob": "*", "path": "../"}},
		{"ls", &LsTool{Root: dir}, map[string]any{"path": "../"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runSearch(t, tc.tool, tc.args)
			if !strings.Contains(resultText(res), "outside the workspace root") {
				t.Errorf("expected boundary error, got %q", resultText(res))
			}
		})
	}
}

func TestSearchToolModes(t *testing.T) {
	for _, tool := range []AgentTool{&GrepTool{}, &FindTool{}, &LsTool{}} {
		if tool.ExecutionMode() != ToolExecutionParallel {
			t.Errorf("%s should be parallel", tool.Name())
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
			t.Errorf("%s schema not valid JSON: %v", tool.Name(), err)
		}
	}
}

func TestGitignoreNegation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.txt\n!keep.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gi := loadGitignore(dir)
	if !gi.ignored("drop.txt", false) {
		t.Error("*.txt should be ignored")
	}
	if gi.ignored("keep.txt", false) {
		t.Error("!keep.txt should be re-included")
	}
}
