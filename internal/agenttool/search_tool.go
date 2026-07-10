// This file implements the search tools (US-019): grep (search file contents by
// regexp with optional glob filtering), find (locate files by name glob), and ls
// (list a directory, distinguishing files from directories). All three resolve
// paths against a Root with the same boundary guard as the other tools and skip
// paths ignored by the workspace .gitignore. They are read-only → parallel.
package agenttool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// searchMaxResults caps the number of matches/entries any single search returns
// so a broad query cannot flood the model's context.
const searchMaxResults = 1000

// resolveWithin resolves p against root and verifies it stays within it. It is
// the shared boundary policy used by all search tools (mirrors ReadTool).
func resolveWithin(root, p string) (string, error) {
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		root = wd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("invalid root: %w", err)
	}
	var full string
	if filepath.IsAbs(p) {
		full = filepath.Clean(p)
	} else {
		full = filepath.Join(absRoot, p)
	}
	rel, err := filepath.Rel(absRoot, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace root", p)
	}
	return full, nil
}

// gitignore is a minimal .gitignore matcher. It supports the common subset:
// blank lines and #-comments are skipped; a leading "/" anchors to the root;
// a trailing "/" matches directories only; "!" negation re-includes; and plain
// patterns match by base name or path via filepath.Match. It is intentionally
// not a full gitignore implementation (no "**" spanning, no nested .gitignore).
type gitignore struct {
	rules []ignoreRule
}

type ignoreRule struct {
	pattern  string
	negate   bool
	dirOnly  bool
	anchored bool
}

// loadGitignore reads root/.gitignore. A missing file yields an empty matcher
// (matches nothing), never an error.
func loadGitignore(root string) *gitignore {
	gi := &gitignore{}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return gi
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if strings.HasPrefix(line, "/") {
			r.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		if line == "" {
			continue
		}
		r.pattern = line
		gi.rules = append(gi.rules, r)
	}
	return gi
}

// ignored reports whether relPath (slash-separated, relative to root) is ignored.
// isDir refines dir-only rules. Later rules win, so a negation can re-include.
func (g *gitignore) ignored(relPath string, isDir bool) bool {
	relPath = filepath.ToSlash(relPath)
	base := relPath
	if i := strings.LastIndex(relPath, "/"); i >= 0 {
		base = relPath[i+1:]
	}
	result := false
	for _, r := range g.rules {
		if r.dirOnly && !isDir {
			continue
		}
		var match bool
		if r.anchored || strings.Contains(r.pattern, "/") {
			match, _ = filepath.Match(r.pattern, relPath)
		} else {
			match, _ = filepath.Match(r.pattern, base)
			if !match {
				// A non-anchored pattern also matches any path component,
				// so an ignored directory hides everything beneath it.
				for _, seg := range strings.Split(relPath, "/") {
					if ok, _ := filepath.Match(r.pattern, seg); ok {
						match = true
						break
					}
				}
			}
		}
		if match {
			result = !r.negate
		}
	}
	return result
}

// GrepTool searches file contents by regexp under Root, honoring .gitignore.
type GrepTool struct {
	// Root bounds the search; empty defaults to the current working directory.
	Root string
}

type grepToolArgs struct {
	// Pattern is the regexp to search for (Go regexp syntax).
	Pattern string `json:"pattern"`
	// Path optionally scopes the search to a subdirectory (relative to Root).
	Path string `json:"path,omitempty"`
	// Glob optionally filters files by base-name glob (e.g. "*.go").
	Glob string `json:"glob,omitempty"`
}

func (t *GrepTool) Name() string { return "grep" }
func (t *GrepTool) Description() string {
	return "Search file contents by regular expression under the workspace, " +
		"optionally filtering files by glob. Skips .gitignore'd paths."
}
func (t *GrepTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}
func (t *GrepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Regular expression to search for."},
    "path":    {"type": "string", "description": "Subdirectory to scope the search to (relative to the workspace root)."},
    "glob":    {"type": "string", "description": "Filter files by base-name glob, e.g. *.go."}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`)
}

func (t *GrepTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a grepToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return errorResult(fmt.Sprintf("grep: invalid arguments: %v", err)), nil
	}
	if a.Pattern == "" {
		return errorResult("grep: pattern is required"), nil
	}
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return errorResult(fmt.Sprintf("grep: invalid pattern: %v", err)), nil
	}
	root, err := resolveWithin(t.Root, "")
	if err != nil {
		return errorResult("grep: " + err.Error()), nil
	}
	start := root
	if a.Path != "" {
		if start, err = resolveWithin(t.Root, a.Path); err != nil {
			return errorResult("grep: " + err.Error()), nil
		}
	}
	gi := loadGitignore(root)

	var matches []string
	count := 0
	walkErr := filepath.WalkDir(start, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if gi.ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if a.Glob != "" {
			if ok, _ := filepath.Match(a.Glob, d.Name()); !ok {
				return nil
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
				count++
				if count >= searchMaxResults {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return errorResult(fmt.Sprintf("grep: %v", walkErr)), nil
	}

	msg := fmt.Sprintf("%d match(es) for %q", len(matches), a.Pattern)
	if len(matches) > 0 {
		msg += "\n" + strings.Join(matches, "\n")
	}
	if count >= searchMaxResults {
		msg += fmt.Sprintf("\n[truncated at %d matches]", searchMaxResults)
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"matches": len(matches)},
	}, nil
}

// FindTool locates files by base-name glob under Root, honoring .gitignore.
type FindTool struct {
	// Root bounds the search; empty defaults to the current working directory.
	Root string
}

type findToolArgs struct {
	// Glob is the base-name glob to match (e.g. "*.go").
	Glob string `json:"glob"`
	// Path optionally scopes the search to a subdirectory (relative to Root).
	Path string `json:"path,omitempty"`
}

func (t *FindTool) Name() string { return "find" }
func (t *FindTool) Description() string {
	return "Find files by base-name glob under the workspace. Skips .gitignore'd paths."
}
func (t *FindTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}
func (t *FindTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "glob": {"type": "string", "description": "Base-name glob to match, e.g. *.go."},
    "path": {"type": "string", "description": "Subdirectory to scope the search to (relative to the workspace root)."}
  },
  "required": ["glob"],
  "additionalProperties": false
}`)
}

func (t *FindTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a findToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return errorResult(fmt.Sprintf("find: invalid arguments: %v", err)), nil
	}
	if a.Glob == "" {
		return errorResult("find: glob is required"), nil
	}
	root, err := resolveWithin(t.Root, "")
	if err != nil {
		return errorResult("find: " + err.Error()), nil
	}
	start := root
	if a.Path != "" {
		if start, err = resolveWithin(t.Root, a.Path); err != nil {
			return errorResult("find: " + err.Error()), nil
		}
	}
	gi := loadGitignore(root)

	var found []string
	walkErr := filepath.WalkDir(start, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if gi.ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if ok, _ := filepath.Match(a.Glob, d.Name()); ok {
			found = append(found, rel)
			if len(found) >= searchMaxResults {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil {
		return errorResult(fmt.Sprintf("find: %v", walkErr)), nil
	}
	sort.Strings(found)

	msg := fmt.Sprintf("%d file(s) matching %q", len(found), a.Glob)
	if len(found) > 0 {
		msg += "\n" + strings.Join(found, "\n")
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"count": len(found)},
	}, nil
}

// LsTool lists the entries of a directory, distinguishing files from directories.
type LsTool struct {
	// Root bounds the listing; empty defaults to the current working directory.
	Root string
}

type lsToolArgs struct {
	// Path is the directory to list, relative to Root (empty = Root itself).
	Path string `json:"path,omitempty"`
}

func (t *LsTool) Name() string { return "ls" }
func (t *LsTool) Description() string {
	return "List a directory's entries, marking directories with a trailing slash."
}
func (t *LsTool) ExecutionMode() agentcore.ToolExecutionMode { return agentcore.ToolExecutionParallel }
func (t *LsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Directory to list, relative to the workspace root."}
  },
  "additionalProperties": false
}`)
}

func (t *LsTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a lsToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return errorResult(fmt.Sprintf("ls: invalid arguments: %v", err)), nil
	}
	full, err := resolveWithin(t.Root, a.Path)
	if err != nil {
		return errorResult("ls: " + err.Error()), nil
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return errorResult(fmt.Sprintf("ls: %q does not exist", a.Path)), nil
		}
		return errorResult(fmt.Sprintf("ls: cannot stat %q: %v", a.Path, err)), nil
	}
	if !info.IsDir() {
		return errorResult(fmt.Sprintf("ls: %q is not a directory", a.Path)), nil
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return errorResult(fmt.Sprintf("ls: cannot read %q: %v", a.Path, err)), nil
	}

	var dirs, files []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name()+"/")
		} else {
			files = append(files, e.Name())
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)
	lines := append(dirs, files...)

	label := a.Path
	if label == "" {
		label = "."
	}
	msg := fmt.Sprintf("%s (%d dir(s), %d file(s))", label, len(dirs), len(files))
	if len(lines) > 0 {
		msg += "\n" + strings.Join(lines, "\n")
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"dirs": len(dirs), "files": len(files)},
	}, nil
}
