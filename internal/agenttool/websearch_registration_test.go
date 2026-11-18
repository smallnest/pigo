package agenttool_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/cli/run"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestParallelSearchBuiltinRegistration(t *testing.T) {
	t.Setenv("PIGO_WEBSEARCH_BACKEND", "parallel")
	fixture, err := os.ReadFile("testdata/websearch/parallel-search.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		disabled bool
		policy   run.ToolPolicy
		wantRun  bool
	}{
		{name: "allowed", policy: run.ToolPolicy{Allow: []string{"websearch"}}, wantRun: true},
		{name: "no-tools", disabled: true},
		{name: "denied", policy: run.ToolPolicy{Allow: []string{"websearch"}, Deny: []string{"websearch"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			requests := 0
			tools := run.BuiltinTools(t.TempDir(), c.disabled)
			for _, tool := range tools {
				if search, ok := tool.(*agenttool.WebSearchTool); ok {
					search.Client = &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
						requests++
						var request struct{ Method string }
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Fatal(err)
						}
						body := string(fixture)
						status := 200
						switch request.Method {
						case "initialize":
							body = `{"id":1,"result":{"protocolVersion":"2025-03-26"}}`
						case "notifications/initialized":
							status, body = 202, ""
						}
						return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
					})}
				}
			}
			registry := agenttool.NewToolRegistry()
			for _, tool := range run.ApplyToolPolicy(tools, c.policy) {
				if err := registry.Register(tool); err != nil {
					t.Fatal(err)
				}
			}
			results, _ := agenttool.ExecuteToolCalls(context.Background(), agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: registry}}, []agentcore.AgentToolCall{
				{ID: "builtin", Name: "websearch", Arguments: json.RawMessage(`{"query":"Go context documentation","count":1}`)},
			}, nil)
			text := agentcore.ContentToText(results[0].Content)
			if c.wantRun {
				if requests != 3 || results[0].IsError || !strings.Contains(text, "\n   https://pkg.go.dev/context\n") || strings.Contains(text, "example.org") {
					t.Fatalf("builtin dispatch: requests=%d, result=%s", requests, text)
				}
			} else if requests != 0 || !results[0].IsError {
				t.Fatalf("disabled search dispatched: requests=%d, result=%s", requests, text)
			}
		})
	}
}
