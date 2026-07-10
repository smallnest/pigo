// This file implements the write tool (US-016): create or overwrite a file at a
// given path, creating parent directories as needed. Overwrites are reported so
// the caller/model knows an existing file was replaced (parity with pi's write
// behavior). Paths resolve against a Root and are rejected if they escape it.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteTool writes text files under Root, creating parent directories as needed.
type WriteTool struct {
	// Root bounds all writes; a path resolving outside Root is rejected. Empty
	// Root defaults to the current working directory.
	Root string
}

// writeToolArgs is the decoded argument shape for WriteTool.
type writeToolArgs struct {
	// Path is the file to write, relative to Root (or absolute within Root).
	Path string `json:"path"`
	// Content is the full file contents to write (overwrites any existing file).
	Content string `json:"content"`
}

// Name implements AgentTool.
func (t *WriteTool) Name() string { return "write" }

// Description implements AgentTool.
func (t *WriteTool) Description() string {
	return "Create or overwrite a file at the given path, creating parent " +
		"directories as needed. Overwriting an existing file is reported."
}

// Schema implements AgentTool.
func (t *WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":    {"type": "string", "description": "File path to write, relative to the workspace root."},
    "content": {"type": "string", "description": "Full file contents to write."}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Writes mutate the filesystem → sequential
// so a batch does not race concurrent writes to the same tree.
func (t *WriteTool) ExecutionMode() ToolExecutionMode { return ToolExecutionSequential }

// resolvePath resolves p against Root and verifies it stays within Root. It
// mirrors ReadTool.resolvePath so the two tools share one boundary policy.
func (t *WriteTool) resolvePath(p string) (string, error) {
	root := t.Root
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

// Execute implements AgentTool. Write failures are encoded as error results;
// the returned Go error is reserved for nothing here (argument decode also
// degrades to a result), matching the read tool's contract.
func (t *WriteTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate ToolUpdateFunc) (AgentToolResult, error) {
	var a writeToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return errorResult(fmt.Sprintf("write: invalid arguments: %v", err)), nil
	}
	if a.Path == "" {
		return errorResult("write: path is required"), nil
	}
	full, err := t.resolvePath(a.Path)
	if err != nil {
		return errorResult("write: " + err.Error()), nil
	}

	// Detect overwrite before writing so the result can report it. A path that
	// points at a directory is an error, not an overwrite.
	overwrote := false
	if info, statErr := os.Stat(full); statErr == nil {
		if info.IsDir() {
			return errorResult(fmt.Sprintf("write: %q is a directory, not a file", a.Path)), nil
		}
		overwrote = true
	}

	// Create parent directories as needed.
	if dir := filepath.Dir(full); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return errorResult(fmt.Sprintf("write: cannot create parent directories for %q: %v", a.Path, err)), nil
		}
	}

	if err := os.WriteFile(full, []byte(a.Content), 0o644); err != nil {
		return errorResult(fmt.Sprintf("write: cannot write %q: %v", a.Path, err)), nil
	}

	verb := "Created"
	if overwrote {
		verb = "Overwrote"
	}
	msg := fmt.Sprintf("%s %s (%d bytes)", verb, a.Path, len(a.Content))
	return AgentToolResult{
		Content: ContentList{NewTextContent(msg)},
		Details: map[string]any{"path": a.Path, "bytes": len(a.Content), "overwrote": overwrote},
	}, nil
}
