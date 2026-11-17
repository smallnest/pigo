package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

func parallelFixture(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("testdata/websearch/parallel-search.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// Exercise the same registry, validation, hooks, and final ToolResultMessage
// used by an ordinary agent turn, without calling an unrelated model service.
func TestWebSearchParallelDispatch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			fixture := parallelFixture(t)
			var methods []string
			tool := &WebSearchTool{
				getenv: func(k string) string {
					return map[string]string{"PIGO_WEBSEARCH_BACKEND": "parallel", "TAVILY_API_KEY": "must-not-send", "BRAVE_API_KEY": "must-not-send"}[k]
				},
				Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != parallelMCPEndpoint || r.Header.Get("User-Agent") != parallelUserAgent {
						t.Fatalf("unexpected endpoint or User-Agent: %s, %s", r.URL, r.Header.Get("User-Agent"))
					}
					if r.Header.Get("Authorization") != "" || r.Header.Get("X-Subscription-Token") != "" {
						t.Fatal("unrelated credentials leaked")
					}
					if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > webSearchTimeout {
						t.Fatal("exchange is not bounded by the search timeout")
					}
					if r.Method == http.MethodDelete {
						methods = append(methods, "DELETE")
						return makeResp(200, "application/json", ""), nil
					}
					if r.Method != http.MethodPost || r.Header.Get("Accept") != "application/json, text/event-stream" {
						t.Fatal("invalid MCP transport headers or method")
					}
					var message struct {
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
						t.Fatal(err)
					}
					methods = append(methods, message.Method)
					if message.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "fixture-session" {
						t.Fatal("missing negotiated session")
					}
					body := fixture
					switch message.Method {
					case "initialize":
						body = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}}`
					case "notifications/initialized":
						return makeResp(202, "", ""), nil
					case "tools/call":
						var params struct {
							Name      string `json:"name"`
							Arguments struct {
								Objective string   `json:"objective"`
								Queries   []string `json:"search_queries"`
							} `json:"arguments"`
						}
						if err := json.Unmarshal(message.Params, &params); err != nil {
							t.Fatal(err)
						}
						if params.Name != "web_search" || params.Arguments.Objective != "Go context documentation" || len(params.Arguments.Queries) != 1 || params.Arguments.Queries[0] != params.Arguments.Objective {
							t.Fatalf("unexpected tool arguments: %s", message.Params)
						}
					default:
						t.Fatalf("unexpected MCP method: %s", message.Method)
					}
					ctype := "application/json"
					if stream {
						ctype = "text/event-stream; charset=utf-8"
						body = "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\r\n\r\nevent: message\r\ndata: " + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\ndata: ") + "\r\n\r\n"
					}
					resp := makeResp(200, ctype, body)
					resp.Header.Set("Mcp-Session-Id", "fixture-session")
					return resp, nil
				})},
			}
			registry := NewToolRegistry()
			if err := registry.Register(tool); err != nil {
				t.Fatal(err)
			}
			res, terminate := executeToolCall(context.Background(), ToolExecutorConfig{Registry: registry}, agentcore.AgentToolCall{
				ID: "search", Name: "websearch", Arguments: json.RawMessage(`{"query":"Go context documentation","count":3,"allowed_domains":["go.dev"],"blocked_domains":["internal.pkg.go.dev"]}`),
			}, nil)
			text := agentcore.ContentToText(res.Content)
			if res.IsError || terminate || res.Role() != agentcore.RoleToolResult || res.ToolCallID != "search" {
				t.Fatalf("unexpected tool result: %+v", res)
			}
			for _, want := range []string{"via parallel", "context package - Go Packages", "https://pkg.go.dev/context", "Context carries deadlines.", "Cancel the derived context when done."} {
				if !strings.Contains(text, want) {
					t.Errorf("normal response missing %q: %s", want, text)
				}
			}
			if strings.Contains(text, "example.org") || strings.Contains(text, "internal.pkg.go.dev") {
				t.Fatalf("domain filtering failed: %s", text)
			}
			if got := res.Details.(map[string]any)["count"]; got != 1 {
				t.Fatalf("filtered result count = %v, want 1", got)
			}
			if got := strings.Join(methods, ","); got != "initialize,notifications/initialized,tools/call,DELETE" {
				t.Fatalf("MCP lifecycle: %s", got)
			}
			if tool.ExecutionMode() != agentcore.ToolExecutionParallel {
				t.Fatal("execution mode changed")
			}
		})
	}
}

func TestWebSearchParallelUnavailable(t *testing.T) {
	cases := []struct {
		name, body, want string
		status           int
		err              error
	}{
		{name: "http", status: 503, want: "HTTP 503"},
		{name: "transport", err: errors.New("offline"), want: "offline"},
		{name: "malformed", status: 200, body: "{", want: "decoding MCP response"},
		{name: "rpc", status: 200, body: `{"id":1,"error":{"code":-32603}}`, want: "MCP error -32603"},
		{name: "version", status: 200, body: `{"id":1,"result":{"protocolVersion":"unsupported"}}`, want: "unsupported MCP protocol"},
		{name: "oversized", status: 200, body: strings.Repeat("x", searchBodyLimit+1), want: "size limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := execWebSearch(t, map[string]string{"PIGO_WEBSEARCH_BACKEND": "parallel"}, func(r *http.Request) (*http.Response, error) {
				return makeResp(c.status, "application/json", c.body), c.err
			}, `{"query":"Go documentation"}`)
			if text := resultText(res); !strings.Contains(text, "parallel backend failed") || !strings.Contains(text, c.want) {
				t.Fatalf("unexpected failure: %s", text)
			}
		})
	}
}

func TestWebSearchParallelToolResponses(t *testing.T) {
	for _, c := range []struct{ name, result, want string }{
		{"empty", `{"content":[{"type":"text","text":"{\"results\":[]}"}]}`, "(no results)"},
		{"error", `{"isError":true,"content":[{"type":"text","text":"unavailable"}]}`, "MCP web_search returned an error"},
		{"missing", `{"content":[]}`, "missing text content"},
		{"invalid", `{"content":[{"type":"text","text":"{}"}]}`, "missing results"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := execWebSearch(t, map[string]string{"PIGO_WEBSEARCH_BACKEND": "parallel"}, func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), `"method":"initialize"`) {
					return makeResp(200, "application/json", `{"id":1,"result":{"protocolVersion":"2025-03-26"}}`), nil
				}
				if strings.Contains(string(body), `"method":"notifications/initialized"`) {
					return makeResp(202, "", ""), nil
				}
				return makeResp(200, "application/json", `{"id":2,"result":`+c.result+`}`), nil
			}, `{"query":"Go documentation"}`)
			if text := resultText(res); !strings.Contains(text, c.want) {
				t.Fatalf("want %q, got %s", c.want, text)
			}
		})
	}
}

func TestWebSearchParallelDisabledOrBlocked(t *testing.T) {
	for _, disabled := range []bool{true, false} {
		registry := NewToolRegistry()
		if !disabled {
			if err := registry.Register(&WebSearchTool{
				getenv: func(string) string { return "parallel" },
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("disabled or blocked search must not make HTTP requests")
					return nil, errors.New("unexpected request")
				})},
			}); err != nil {
				t.Fatal(err)
			}
		}
		cfg := ToolExecutorConfig{Registry: registry, BeforeToolCall: func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
			return &agentcore.BeforeToolCallDecision{Block: true}
		}}
		res, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "blocked", Name: "websearch", Arguments: json.RawMessage(`{"query":"Go documentation"}`)}, nil)
		if !res.IsError {
			t.Fatal("disabled or blocked tool succeeded")
		}
	}
}

func TestWebSearchInvalidBackend(t *testing.T) {
	res := execWebSearch(t, map[string]string{"PIGO_WEBSEARCH_BACKEND": "typo", "TAVILY_API_KEY": "unused"}, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid selection must not fall back to another provider")
		return nil, errors.New("unexpected request")
	}, `{"query":"Go documentation"}`)
	if !strings.Contains(resultText(res), "PIGO_WEBSEARCH_BACKEND must be auto or parallel") {
		t.Fatal(resultText(res))
	}
}

func TestWebSearchParallelCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := &WebSearchTool{
		getenv: func(string) string { return "parallel" },
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			cancel()
			<-r.Context().Done()
			return nil, r.Context().Err()
		})},
	}
	res, err := tool.Execute(ctx, "cancel", json.RawMessage(`{"query":"Go documentation"}`), nil)
	if err != nil || !strings.Contains(resultText(res), "context canceled") {
		t.Fatalf("cancellation result: %s, %v", resultText(res), err)
	}
}

// Opt-in live acceptance: no model API, credentials, or private input is sent.
func TestWebSearchParallelLive(t *testing.T) {
	if os.Getenv("PIGO_TEST_PARALLEL_LIVE") != "1" {
		t.Skip("set PIGO_TEST_PARALLEL_LIVE=1 to search public Go documentation")
	}
	requests := 0
	var sourceURL string
	tool := &WebSearchTool{
		getenv: func(k string) string {
			if k == "PIGO_WEBSEARCH_BACKEND" {
				return "parallel"
			}
			return ""
		},
		Client: &http.Client{Timeout: webSearchTimeout, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != parallelMCPEndpoint || r.Header.Get("User-Agent") != parallelUserAgent || r.Header.Get("Authorization") != "" {
				t.Fatal("unexpected live request")
			}
			requests++
			t.Logf("live request: %s %s User-Agent=%s", r.Method, r.URL, r.Header.Get("User-Agent"))
			resp, err := http.DefaultTransport.RoundTrip(r)
			if err != nil || r.Method != http.MethodPost {
				return resp, err
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, searchBodyLimit+1))
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			// Capture the URL from the actual tools/call response, before the
			// backend maps it into the normal tool message. Do not canonicalize it.
			if raw, err := parallelMCPResult(body, resp.Header.Get("Content-Type"), 2); err == nil {
				var result struct {
					Content []struct{ Text string } `json:"content"`
				}
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				for _, content := range result.Content {
					var payload struct{ Results []struct{ URL string } }
					if err := json.Unmarshal([]byte(content.Text), &payload); err != nil {
						t.Fatal(err)
					}
					for _, hit := range payload.Results {
						if strings.HasPrefix(hit.URL, "https://pkg.go.dev/context") || strings.HasPrefix(hit.URL, "http://pkg.go.dev/context") {
							sourceURL = hit.URL
							break
						}
					}
				}
			}
			return resp, nil
		})},
	}
	registry := NewToolRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	res, _ := executeToolCall(context.Background(), ToolExecutorConfig{Registry: registry}, agentcore.AgentToolCall{
		ID: "live", Name: "websearch", Arguments: json.RawMessage(`{"query":"Go context package documentation site:pkg.go.dev","allowed_domains":["pkg.go.dev"]}`),
	}, nil)
	text := agentcore.ContentToText(res.Content)
	if res.IsError || sourceURL == "" || !strings.Contains(text, "via parallel") || !strings.Contains(text, "\n   "+sourceURL+"\n") {
		t.Fatalf("live source %q missing: %.500s", sourceURL, text)
	}
	if requests < 3 {
		t.Fatal("live MCP lifecycle was not exercised")
	}
	t.Logf("normal ToolResultMessage includes exact source URL: %s", sourceURL)
}
