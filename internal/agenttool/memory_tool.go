// This file implements the memory_search AgentTool (issue #477): a read-only
// tool that queries the persistent memory library (internal/memory) by relevance
// using its BM25 full-text index and returns ranked snippets to the model.
//
// Writes are intentionally NOT a separate tool here. Per the SPEC (§4.1/§4.2),
// memory writes reuse the existing Write/Edit file tools, constrained to the
// memory root and carrying the canonical frontmatter (name/description/
// metadata.type). Traversal protection for those writes lives in the memory
// package (memory.assertSafeComponent) and the write-path plumbing; a dedicated
// memory_write tool would duplicate that. After an off-tool write, the next
// memory_search picks it up automatically because Execute searches with
// ReconcileFirst=true (lazy reconcile).
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/memory"
)

// memorySearchDefaultLimit is the result cap used when the caller omits limit or
// passes a non-positive value; memorySearchMaxLimit is the hard upper bound.
const (
	memorySearchDefaultLimit = 10
	memorySearchMaxLimit     = 50
)

// MemorySearchTool searches the persistent memory library by relevance. Store is
// exported so the loop-integration node (#481) can construct the tool with a
// live *memory.Store, mirroring how TodoTool exposes its Store.
type MemorySearchTool struct {
	// Store is the persistent memory store. When nil, Execute degrades to a
	// friendly no-op result rather than erroring, so a session without memory
	// configured still runs.
	Store *memory.Store
}

// memorySearchArgs is the decoded argument shape for memory_search.
type memorySearchArgs struct {
	Query string `json:"query"`
	Scope string `json:"scope"`
	Type  string `json:"type"`
	Limit int    `json:"limit"`
}

// Name implements AgentTool.
func (t *MemorySearchTool) Name() string { return "memory_search" }

// Description implements AgentTool.
func (t *MemorySearchTool) Description() string {
	return "Search the persistent memory library by relevance (BM25 full-text) " +
		"and return ranked snippets from previously saved notes, checkpoints, and " +
		"references. Use it to recall context from earlier sessions before " +
		"answering or acting. Optional filters: scope (global|projects|sessions|cc), " +
		"type (user|feedback|project|reference|checkpoint|progress|notes|free), and " +
		"limit (default 10, max 50). Results are ordered most-relevant first."
}

// Schema implements AgentTool.
func (t *MemorySearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Free-text query; tokenized and matched against memory bodies (BM25)."},
    "scope": {"type": "string", "description": "Optional scope filter: global, projects, sessions, or cc."},
    "type":  {"type": "string", "description": "Optional type filter: user, feedback, project, reference, checkpoint, progress, notes, or free."},
    "limit": {"type": "integer", "description": "Max results to return (default 10, capped at 50)."}
  },
  "required": ["query"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. memory_search is read-only, so it runs in
// the default parallel mode.
func (t *MemorySearchTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// Execute implements AgentTool. It decodes the args, runs a lazily-reconciled
// BM25 search, and formats the ranked hits as text (one line each:
// "[type/scope] path (score) — snippet") with the structured []SearchResult in
// Details. A nil Store or empty query degrades to a friendly no-op result rather
// than a Go error so the loop keeps running.
func (t *MemorySearchTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[memorySearchArgs](args, "memory_search")
	if bad != nil {
		return *bad, nil
	}

	query := strings.TrimSpace(a.Query)
	if t.Store == nil {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(
				"memory_search: no memory store configured; nothing to search.")},
		}, nil
	}
	if query == "" {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(
				"memory_search: empty query; provide a search string.")},
		}, nil
	}

	limit := a.Limit
	if limit <= 0 {
		limit = memorySearchDefaultLimit
	}
	if limit > memorySearchMaxLimit {
		limit = memorySearchMaxLimit
	}

	results, err := t.Store.Search(query, memory.SearchOptions{
		Scope:          strings.TrimSpace(a.Scope),
		Type:           strings.TrimSpace(a.Type),
		Limit:          limit,
		ReconcileFirst: true, // lazy reconcile so off-tool writes are indexed
		// ScoreFloor left at its zero value → package default (0.15).
	})
	if err != nil {
		return errorResult(fmt.Sprintf("memory_search: %v", err)), nil
	}

	if len(results) == 0 {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(
				fmt.Sprintf("memory_search: no results for %q.", query))},
			Details: results,
		}, nil
	}

	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(formatMemoryResults(query, results))},
		Details: results,
	}, nil
}

// formatMemoryResults renders ranked hits as a header line plus one line per
// result: "[type/scope] path (score) — snippet". Snippets are whitespace-
// collapsed so a multi-line body stays on a single row.
func formatMemoryResults(query string, results []memory.SearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "memory_search: %d result(s) for %q, most relevant first:", len(results), query)
	for _, r := range results {
		typ := string(r.Type)
		if typ == "" {
			typ = "free"
		}
		scope := string(r.Scope)
		if r.ScopeID != "" {
			scope = scope + "/" + r.ScopeID
		}
		line := fmt.Sprintf("\n[%s/%s] %s (%.3f)", typ, scope, r.Path, r.Score)
		if snip := collapseWhitespace(r.Snippet); snip != "" {
			line += " — " + snip
		}
		b.WriteString(line)
	}
	return b.String()
}

// collapseWhitespace folds any run of whitespace (including newlines) into a
// single space and trims the ends, keeping a snippet to one line.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
