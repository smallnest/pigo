// This file implements the read tool (US-015): read a file's contents by path,
// with optional line offset/limit, numbered output, and large-file truncation.
// Paths are resolved against a Root and rejected if they escape it (path
// traversal guard) or do not exist.
package agenttool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// readToolMaxLines caps how many lines a single read returns before truncating
// (protects the model's context from huge files). Callers page with offset.
const readToolMaxLines = 2000

// readToolMaxLineLen caps how many bytes of a single line are returned; longer
// lines are truncated with a marker.
const readToolMaxLineLen = 2000

// scanBufInit is the initial per-line scanner buffer (it grows on demand up to
// the max). readScanBufMax is generous — a read may page through a file with
// very long lines (minified JS, JSON) that must not error out mid-read.
const (
	scanBufInit    = 64 * 1024
	readScanBufMax = 16 * 1024 * 1024
	grepScanBufMax = 1 * 1024 * 1024
)

// filePerm / dirPerm are the modes new files and parent directories are created
// with by the write/edit tools (standard non-executable file, traversable dir).
const (
	filePerm os.FileMode = 0o644
	dirPerm  os.FileMode = 0o755
)

// ReadTool reads text files under Root. It is the first concrete AgentTool.
type ReadTool struct {
	// Root is the directory that bounds all reads. A path resolving outside Root
	// is rejected. Empty Root defaults to the current working directory.
	Root string
}

// readToolArgs is the decoded argument shape for ReadTool.
type readToolArgs struct {
	// Path is the file to read, relative to Root (or absolute within Root).
	Path string `json:"path"`
	// Offset is the 1-based line to start reading from. 0/1 both mean line 1.
	Offset int `json:"offset,omitempty"`
	// Limit is the maximum number of lines to return. 0 means the default cap.
	Limit int `json:"limit,omitempty"`
}

// Name implements AgentTool.
func (t *ReadTool) Name() string { return "read" }

// Description implements AgentTool.
func (t *ReadTool) Description() string {
	return "Read a text file's contents by path, with optional line offset/limit. " +
		"Output is line-numbered; very large files are truncated."
}

// Schema implements AgentTool.
func (t *ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "File path to read, relative to the workspace root."},
    "offset": {"type": "integer", "description": "1-based line number to start from.", "minimum": 0},
    "limit":  {"type": "integer", "description": "Maximum number of lines to return.", "minimum": 0}
  },
  "required": ["path"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Reads are side-effect free → parallel.
func (t *ReadTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// resolvePath resolves p against Root via the shared resolveWithin boundary
// policy, so every file tool enforces the same workspace-escape guard.
func (t *ReadTool) resolvePath(p string) (string, error) {
	return resolveWithin(t.Root, p)
}

// Execute implements AgentTool. It never returns a Go error for a read failure
// (bad path, missing file); those are encoded as error results so the model can
// react. The returned error is reserved for argument decode failures.
func (t *ReadTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[readToolArgs](args, "read")
	if bad != nil {
		return *bad, nil
	}
	if a.Path == "" {
		return errorResult("read: path is required"), nil
	}
	full, err := t.resolvePath(a.Path)
	if err != nil {
		return errorResult("read: " + err.Error()), nil
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return errorResult(fmt.Sprintf("read: file %q does not exist", a.Path)), nil
		}
		return errorResult(fmt.Sprintf("read: cannot stat %q: %v", a.Path, err)), nil
	}
	if info.IsDir() {
		return errorResult(fmt.Sprintf("read: %q is a directory, not a file", a.Path)), nil
	}

	f, err := os.Open(full)
	if err != nil {
		return errorResult(fmt.Sprintf("read: cannot open %q: %v", a.Path, err)), nil
	}
	defer f.Close()

	text, truncated := readNumbered(f, a.Offset, a.Limit)
	if truncated {
		text += fmt.Sprintf("\n... (output truncated at %d lines; use offset to read more)", readToolMaxLines)
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(text)}}, nil
}

// readNumbered reads lines from r starting at 1-based offset, returning at most
// limit lines (capped at readToolMaxLines), each prefixed with its line number.
// The bool reports whether the output was truncated by the cap.
func readNumbered(r io.Reader, offset, limit int) (string, bool) {
	if offset < 1 {
		offset = 1
	}
	max := limit
	if max <= 0 || max > readToolMaxLines {
		max = readToolMaxLines
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, scanBufInit), readScanBufMax)
	var b strings.Builder
	lineNo := 0
	emitted := 0
	truncated := false
	for sc.Scan() {
		lineNo++
		if lineNo < offset {
			continue
		}
		if emitted >= max {
			truncated = true
			break
		}
		line := sc.Text()
		if len(line) > readToolMaxLineLen {
			line = line[:readToolMaxLineLen] + "… (line truncated)"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", lineNo, line)
		emitted++
	}
	return b.String(), truncated
}
