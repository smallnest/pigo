// This file implements the websearch backends: Tavily, Brave, and SpecStream
// (credentialed JSON APIs) plus a keyless DuckDuckGo HTML fallback.
// selectSearchBackend picks the first backend whose credential is present,
// defaulting to DuckDuckGo.
package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// searchBackend is one pluggable search provider. name is used in result framing
// and error messages; search runs the query and returns up to count normalized
// hits.
type searchBackend interface {
	name() string
	search(ctx context.Context, client *http.Client, query string, count int) ([]searchResult, error)
}

// selectSearchBackend returns the first backend whose credential env var is set,
// falling back to the keyless DuckDuckGo backend. The order encodes preference:
// LLM-optimized Tavily first, then Brave, then the keyless fallback.
func selectSearchBackend(getenv func(string) string) searchBackend {
	if k := strings.TrimSpace(getenv("TAVILY_API_KEY")); k != "" {
		return tavilyBackend{apiKey: k}
	}
	if k := strings.TrimSpace(getenv("BRAVE_API_KEY")); k != "" {
		return braveBackend{apiKey: k}
	}
	if k := strings.TrimSpace(getenv("SPECSTREAM_API_KEY")); k != "" {
		return specstreamBackend{apiKey: k}
	}
	return duckDuckGoBackend{}
}

// searchBodyLimit caps how much of a backend response body is read.
const searchBodyLimit = 4 * 1024 * 1024

// --- Tavily ---------------------------------------------------------------

type tavilyBackend struct{ apiKey string }

func (b tavilyBackend) name() string { return "tavily" }

func (b tavilyBackend) search(ctx context.Context, client *http.Client, query string, count int) ([]searchResult, error) {
	reqBody, _ := json.Marshal(map[string]any{"query": query, "max_results": count})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, searchBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	out := make([]searchResult, 0, len(decoded.Results))
	for _, r := range decoded.Results {
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

// --- Brave ----------------------------------------------------------------

type braveBackend struct{ apiKey string }

func (b braveBackend) name() string { return "brave" }

func (b braveBackend) search(ctx context.Context, client *http.Client, query string, count int) ([]searchResult, error) {
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), count)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, searchBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	out := make([]searchResult, 0, len(decoded.Web.Results))
	for _, r := range decoded.Web.Results {
		out = append(out, searchResult{Title: stripHTMLTags(r.Title), URL: r.URL, Snippet: stripHTMLTags(r.Description)})
	}
	return out, nil
}

// --- SpecStream -----------------------------------------------------------

type specstreamBackend struct{ apiKey string }

func (b specstreamBackend) name() string { return "specstream" }

func (b specstreamBackend) search(ctx context.Context, client *http.Client, query string, count int) ([]searchResult, error) {
	reqBody, _ := json.Marshal(map[string]any{"q": query, "num": count})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.specstream.ai/v1/web-search", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, searchBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		Sources []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Excerpt string `json:"excerpt"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	out := make([]searchResult, 0, len(decoded.Sources))
	for _, r := range decoded.Sources {
		snippet := strings.TrimSpace(r.Excerpt)
		if snippet == "" {
			snippet = strings.TrimSpace(r.Snippet)
		}
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: snippet})
	}
	return out, nil
}

// --- DuckDuckGo (keyless fallback) ----------------------------------------

type duckDuckGoBackend struct{}

func (duckDuckGoBackend) name() string { return "duckduckgo" }

func (duckDuckGoBackend) search(ctx context.Context, client *http.Client, query string, count int) ([]searchResult, error) {
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// A browser-like User-Agent avoids the endpoint serving an empty/blocked page.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; pigo-websearch/1.0)")
	req.Header.Set("Accept", "text/html")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, searchBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	results, err := parseDuckDuckGoHTML(body)
	if err != nil {
		return nil, err
	}
	if len(results) > count {
		results = results[:count]
	}
	return results, nil
}

// parseDuckDuckGoHTML extracts result links and snippets from the DuckDuckGo
// HTML endpoint. Title/URL come from <a class="result__a">; the URL is wrapped in
// a redirect carrying the real target in the uddg query param, which is decoded.
// Snippets come from elements with class "result__snippet", matched to results by
// position. A result with no snippet is still returned (snippet empty).
func parseDuckDuckGoHTML(body []byte) ([]searchResult, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parsing html: %w", err)
	}
	var results []searchResult
	var snippets []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "result__a") {
			href := attr(n, "href")
			results = append(results, searchResult{Title: nodeText(n), URL: unwrapDDGURL(href)})
		}
		if n.Type == html.ElementNode && hasClass(n, "result__snippet") {
			snippets = append(snippets, nodeText(n))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	for i := range results {
		if i < len(snippets) {
			results[i].Snippet = snippets[i]
		}
	}
	return results, nil
}

// unwrapDDGURL turns a DuckDuckGo redirect href ("//duckduckgo.com/l/?uddg=...")
// into the real target by decoding the uddg param. A non-redirect href is
// returned as-is (with a scheme added when protocol-relative).
func unwrapDDGURL(href string) string {
	raw := href
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return href
	}
	if target := u.Query().Get("uddg"); target != "" {
		return target
	}
	return raw
}

// --- HTML helpers ---------------------------------------------------------

// attr returns the value of the named attribute on n, or "".
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// hasClass reports whether n's class attribute contains the given class token.
func hasClass(n *html.Node, class string) bool {
	for _, f := range strings.Fields(attr(n, "class")) {
		if f == class {
			return true
		}
	}
	return false
}

// nodeText returns the concatenated, space-collapsed text content of n.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// stripHTMLTags removes inline markup (e.g. Brave's <strong> highlights) from a
// snippet, leaving space-collapsed text. Malformed fragments are returned as-is.
func stripHTMLTags(s string) string {
	if !strings.ContainsRune(s, '<') {
		return s
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	return nodeText(doc)
}
