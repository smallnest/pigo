// This file implements the websearch tool: run a web search and return the top
// results (title, URL, snippet) as Markdown. pi has no such tool; this mirrors
// Claude Code's WebSearch. It is provider-agnostic and auto-detects a backend by
// available credentials unless PIGO_WEBSEARCH_BACKEND=parallel explicitly selects
// the keyless Parallel MCP backend:
//
//   - Tavily      when TAVILY_API_KEY is set (LLM-optimized results).
//   - Brave       when BRAVE_API_KEY is set (independent index).
//   - DuckDuckGo   as a keyless fallback (HTML endpoint, no API key needed).
//
// The first backend whose credential is present wins; DuckDuckGo is always the
// last-resort fallback. Optional allowed/blocked domain filters are applied
// uniformly to every backend by post-filtering the result hosts, so behavior is
// consistent regardless of which backend served the query.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// webSearchTimeout bounds a single search request. webSearchDefaultCount is used
// when the caller omits count; webSearchMaxCount caps it so a run cannot pull an
// unbounded result set into the model's context.
const (
	webSearchTimeout      = 15 * time.Second
	webSearchDefaultCount = 5
	webSearchMaxCount     = 10
)

// WebSearchTool runs a web search via the first available backend. The zero
// value is usable: Client defaults to an http.Client with webSearchTimeout and
// getenv defaults to os.Getenv (both injected in tests).
type WebSearchTool struct {
	// Client performs backend HTTP requests. When nil, a default client bounded by
	// webSearchTimeout is built. Injected for tests to serve canned responses.
	Client *http.Client
	// getenv reads the backend selection and credentials. When nil, os.Getenv is used.
	// Injected for tests so backend selection is deterministic without touching the
	// process environment.
	getenv func(string) string
}

// webSearchArgs is the decoded argument shape for WebSearchTool.
type webSearchArgs struct {
	// Query is the search query (required).
	Query string `json:"query"`
	// Count is the desired number of results (optional; clamped to webSearchMaxCount).
	Count int `json:"count,omitempty"`
	// AllowedDomains, when non-empty, keeps only results whose host matches one of
	// these domains (suffix match). BlockedDomains drops results whose host matches.
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

// searchResult is one normalized hit shared across backends.
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Name implements AgentTool.
func (t *WebSearchTool) Name() string { return "websearch" }

// Description implements AgentTool.
func (t *WebSearchTool) Description() string {
	return "Search the web and return the top results (title, URL, snippet). " +
		"Uses the configured backend or auto-selects by credentials (Tavily, Brave, or a " +
		"keyless DuckDuckGo fallback). Use allowed_domains/blocked_domains to " +
		"restrict results by host. Follow up with the webfetch tool to read a result."
}

// Schema implements AgentTool.
func (t *WebSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "query":           {"type": "string", "description": "The search query."},
    "count":           {"type": "integer", "description": "Desired number of results (default 5, max 10).", "minimum": 1, "maximum": 10},
    "allowed_domains": {"type": "array", "items": {"type": "string"}, "description": "Only include results from these domains (suffix match)."},
    "blocked_domains": {"type": "array", "items": {"type": "string"}, "description": "Exclude results from these domains (suffix match)."}
  },
  "required": ["query"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. A search has no local side effects and is
// safe to run alongside other reads → parallel.
func (t *WebSearchTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// Execute implements AgentTool. Backend failures are encoded as error results
// (the returned Go error is always nil), matching the file tools' contract.
func (t *WebSearchTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[webSearchArgs](args, "websearch")
	if bad != nil {
		return *bad, nil
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return errorResult("websearch: query is required"), nil
	}

	count := a.Count
	if count <= 0 {
		count = webSearchDefaultCount
	}
	if count > webSearchMaxCount {
		count = webSearchMaxCount
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: webSearchTimeout}
	}
	getenv := t.getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	backend, err := selectSearchBackend(getenv)
	if err != nil {
		return errorResult("websearch: " + err.Error()), nil
	}
	// A domain-filtered query can discard most raw hits, so over-fetch before
	// filtering to still land near the requested count.
	fetchCount := count
	if len(a.AllowedDomains) > 0 || len(a.BlockedDomains) > 0 {
		fetchCount = min(webSearchMaxCount, count*3)
	}

	results, err := backend.search(ctx, client, query, fetchCount)
	if err != nil {
		return errorResult(fmt.Sprintf("websearch: %s backend failed: %v", backend.name(), err)), nil
	}
	results = filterByDomain(results, a.AllowedDomains, a.BlockedDomains)
	if len(results) > count {
		results = results[:count]
	}

	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(renderSearchResults(query, backend.name(), results))},
		Details: map[string]any{"backend": backend.name(), "query": query, "count": len(results)},
	}, nil
}

// renderSearchResults formats the hits as a numbered Markdown list, noting which
// backend served the query so the model knows the source.
func renderSearchResults(query, backend string, results []searchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q (via %s):\n", query, backend)
	if len(results) == 0 {
		b.WriteString("\n(no results)")
		return b.String()
	}
	for i, r := range results {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, strings.TrimSpace(r.Title), strings.TrimSpace(r.URL))
		if s := strings.TrimSpace(r.Snippet); s != "" {
			fmt.Fprintf(&b, "   %s\n", s)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// filterByDomain keeps only results whose host suffix-matches an allowed domain
// (when allowed is non-empty) and drops any whose host suffix-matches a blocked
// domain. An unparseable URL is dropped only under an allow-list.
func filterByDomain(results []searchResult, allowed, blocked []string) []searchResult {
	if len(allowed) == 0 && len(blocked) == 0 {
		return results
	}
	out := results[:0:0]
	for _, r := range results {
		host := hostOf(r.URL)
		if len(allowed) > 0 && !matchesAnyDomain(host, allowed) {
			continue
		}
		if matchesAnyDomain(host, blocked) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// hostOf extracts the lowercased host from a result URL, or "" if unparseable.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// matchesAnyDomain reports whether host equals or is a subdomain of any domain.
func matchesAnyDomain(host string, domains []string) bool {
	if host == "" {
		return false
	}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
