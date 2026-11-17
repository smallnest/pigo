package agenttool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	parallelMCPEndpoint = "https://mcp.parallel.ai/mcp"
	parallelUserAgent   = "pigo-websearch/1.0 (+https://github.com/smallnest/pigo)"
	parallelMCPVersion  = "2025-03-26"
)

// parallelBackend adapts only web_search from the Unified MCP server. It sends
// the requested public query without credentials, conversation, or local files.
type parallelBackend struct{}

func (parallelBackend) name() string { return "parallel" }

func (parallelBackend) search(ctx context.Context, client *http.Client, query string, count int) ([]searchResult, error) {
	// The existing timeout bounds the whole exchange, not each handshake step.
	ctx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()
	init, session, err := parallelMCPPost(ctx, client, "", 1, "initialize", map[string]any{
		"protocolVersion": parallelMCPVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "pigo-websearch", "version": "1.0"},
	})
	if err != nil {
		return nil, err
	}
	if session != "" {
		defer func() {
			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, parallelMCPEndpoint, nil)
			if err != nil {
				return
			}
			parallelMCPHeaders(req, session)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
	}
	var negotiated struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(init, &negotiated); err != nil {
		return nil, fmt.Errorf("decoding initialization: %w", err)
	}
	if negotiated.ProtocolVersion != parallelMCPVersion {
		return nil, fmt.Errorf("unsupported MCP protocol version %q", negotiated.ProtocolVersion)
	}
	if _, _, err := parallelMCPPost(ctx, client, session, 0, "notifications/initialized", nil); err != nil {
		return nil, err
	}
	raw, _, err := parallelMCPPost(ctx, client, session, 2, "tools/call", map[string]any{
		"name": "web_search",
		"arguments": map[string]any{
			"objective":      query,
			"search_queries": []string{query},
		},
	})
	if err != nil {
		return nil, err
	}
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decoding tool result: %w", err)
	}
	if result.IsError {
		return nil, fmt.Errorf("MCP web_search returned an error")
	}
	for _, content := range result.Content {
		if content.Type != "text" {
			continue
		}
		var payload struct {
			Results *[]struct {
				Title    string   `json:"title"`
				URL      string   `json:"url"`
				Excerpts []string `json:"excerpts"`
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(content.Text), &payload); err != nil {
			return nil, fmt.Errorf("decoding search results: %w", err)
		}
		if payload.Results == nil {
			return nil, fmt.Errorf("MCP web_search response missing results")
		}
		out := make([]searchResult, 0, min(count, len(*payload.Results)))
		for _, hit := range *payload.Results {
			out = append(out, searchResult{Title: hit.Title, URL: hit.URL, Snippet: strings.Join(hit.Excerpts, "\n")})
			if len(out) >= count {
				break
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("MCP web_search response missing text content")
}

func parallelMCPHeaders(req *http.Request, session string) {
	req.Header.Set("User-Agent", parallelUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", parallelMCPVersion)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
}

// parallelMCPPost is a bounded, read-only MCP exchange. It accepts JSON or SSE
// responses and ignores notifications; it never executes server requests.
func parallelMCPPost(ctx context.Context, client *http.Client, session string, id int, method string, params any) (json.RawMessage, string, error) {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != 0 {
		message["id"] = id
	}
	if params != nil {
		message["params"] = params
	}
	body, err := json.Marshal(message)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parallelMCPEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	parallelMCPHeaders(req, session)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("MCP %s: HTTP %d", method, resp.StatusCode)
	}
	if id == 0 {
		return nil, "", nil
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, searchBodyLimit+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > searchBodyLimit {
		return nil, "", fmt.Errorf("MCP response exceeds size limit")
	}
	result, err := parallelMCPResult(body, resp.Header.Get("Content-Type"), id)
	return result, resp.Header.Get("Mcp-Session-Id"), err
}

func parallelMCPResult(body []byte, contentType string, id int) (json.RawMessage, error) {
	decode := func(data []byte) (json.RawMessage, bool, error) {
		var response struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, false, fmt.Errorf("decoding MCP response: %w", err)
		}
		if response.ID == nil || *response.ID != id {
			return nil, false, nil
		}
		if response.Error != nil {
			return nil, true, fmt.Errorf("MCP error %d", response.Error.Code)
		}
		if len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) {
			return nil, true, fmt.Errorf("MCP response missing result")
		}
		return response.Result, true, nil
	}
	if !strings.HasPrefix(contentType, "text/event-stream") {
		if result, matched, err := decode(body); matched || err != nil {
			return result, err
		}
	} else {
		scanner := bufio.NewScanner(bytes.NewReader(body))
		scanner.Buffer(make([]byte, 4096), searchBodyLimit+1)
		var data []string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			if line == "" && len(data) > 0 {
				if result, matched, err := decode([]byte(strings.Join(data, "\n"))); matched || err != nil {
					return result, err
				}
				data = nil
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		if len(data) > 0 {
			if result, matched, err := decode([]byte(strings.Join(data, "\n"))); matched || err != nil {
				return result, err
			}
		}
	}
	return nil, fmt.Errorf("MCP response missing matching request ID")
}
