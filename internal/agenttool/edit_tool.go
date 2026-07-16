// This file implements the edit tool (US-017): exact string replacement within
// a file. old_string must match exactly; if it is not unique (and replace_all
// is false) the edit is rejected. A unified-style diff of the change is returned
// for the UI to render. Paths resolve against a Root with the same traversal
// guard as the read/write tools.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// EditTool performs exact string replacements in files under Root.
type EditTool struct {
	// Root bounds all edits; a path resolving outside Root is rejected. Empty
	// Root defaults to the current working directory.
	Root string
}

// editToolArgs is the decoded argument shape for EditTool.
type editToolArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// Name implements AgentTool.
func (t *EditTool) Name() string { return "edit" }

// Description implements AgentTool.
func (t *EditTool) Description() string {
	return "Replace an exact string in a file. old_string must be unique unless " +
		"replace_all is set. Returns a diff of the change."
}

// Schema implements AgentTool.
func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":        {"type": "string", "description": "File path to edit, relative to the workspace root."},
    "old_string":  {"type": "string", "description": "Exact text to replace."},
    "new_string":  {"type": "string", "description": "Replacement text."},
    "replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match."}
  },
  "required": ["path", "old_string", "new_string"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Edits mutate the filesystem → sequential.
func (t *EditTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// resolvePath resolves p against Root via the shared resolveWithin boundary
// policy, so every file tool enforces the same workspace-escape guard.
func (t *EditTool) resolvePath(p string) (string, error) {
	return resolveWithin(t.Root, p)
}

// Execute implements AgentTool. Edit failures (no match, non-unique match,
// missing file, out-of-root) are encoded as error results.
func (t *EditTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[editToolArgs](args, "edit")
	if bad != nil {
		return *bad, nil
	}
	if a.Path == "" {
		return errorResult("edit: path is required"), nil
	}
	if a.OldString == a.NewString {
		return errorResult("edit: old_string and new_string are identical; nothing to change"), nil
	}
	full, err := t.resolvePath(a.Path)
	if err != nil {
		return errorResult("edit: " + err.Error()), nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return errorResult(fmt.Sprintf("edit: file %q does not exist", a.Path)), nil
		}
		return errorResult(fmt.Sprintf("edit: cannot read %q: %v", a.Path, err)), nil
	}
	original := string(data)

	count := strings.Count(original, a.OldString)
	if count == 0 {
		return errorResult(fmt.Sprintf("edit: old_string not found in %q", a.Path)), nil
	}
	if count > 1 && !a.ReplaceAll {
		return errorResult(fmt.Sprintf("edit: old_string is not unique in %q (%d matches); provide more context or set replace_all", a.Path, count)), nil
	}

	var updated string
	if a.ReplaceAll {
		updated = strings.ReplaceAll(original, a.OldString, a.NewString)
	} else {
		updated = strings.Replace(original, a.OldString, a.NewString, 1)
	}

	if err := os.WriteFile(full, []byte(updated), filePerm); err != nil {
		return errorResult(fmt.Sprintf("edit: cannot write %q: %v", a.Path, err)), nil
	}

	diff := unifiedDiff(a.Path, original, updated)
	replaced := 1
	if a.ReplaceAll {
		replaced = count
	}
	msg := fmt.Sprintf("Edited %s (%d replacement(s))\n%s", a.Path, replaced, diff)
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"path": a.Path, "replacements": replaced, "diff": diff},
	}, nil
}

// unifiedDiff produces a minimal line-based diff between old and new content.
// It is not a full unified-diff implementation (no hunk coalescing); it emits a
// header plus per-line -/+ markers, which is enough for a UI to render the
// change. Unchanged lines are shown with a leading space for context.
func unifiedDiff(path, oldContent, newContent string) string {
	oldLines := splitLinesKeep(oldContent)
	newLines := splitLinesKeep(newContent)

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)

	// Longest common subsequence over lines drives the -/+ markers.
	ops := diffLines(oldLines, newLines)
	for _, op := range ops {
		switch op.kind {
		case diffEqual:
			fmt.Fprintf(&b, " %s\n", op.text)
		case diffDelete:
			fmt.Fprintf(&b, "-%s\n", op.text)
		case diffInsert:
			fmt.Fprintf(&b, "+%s\n", op.text)
		}
	}
	return b.String()
}

// splitLinesKeep splits s into lines, dropping a single trailing newline so an
// empty final element is not produced for the common "ends with \n" case.
func splitLinesKeep(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffKind int

const (
	diffEqual diffKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffKind
	text string
}

// diffLines computes a line diff via a standard LCS dynamic-programming table,
// then backtracks to emit equal/delete/insert ops in order.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// lcs[i][j] = length of LCS of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			ops = append(ops, diffOp{diffEqual, a[i]})
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			ops = append(ops, diffOp{diffDelete, a[i]})
			i++
		} else {
			ops = append(ops, diffOp{diffInsert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{diffDelete, a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{diffInsert, b[j]})
	}
	return ops
}
