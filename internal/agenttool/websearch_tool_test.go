// Tests for the websearch tool: backend auto-selection by credential, per-backend
// response parsing (Tavily JSON, Brave JSON with HTML highlights, DuckDuckGo HTML
// with redirect-wrapped URLs), domain filtering, count clamping, and structured
// errors. A fake RoundTripper serves canned responses so no network is touched.
package agenttool

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// execWebSearch runs the tool with a fake transport and a fixed environment.
func execWebSearch(t *testing.T, env map[string]string, fn roundTripFunc, args string) agentcore.AgentToolResult {
	t.Helper()
	tool := &WebSearchTool{
		Client: &http.Client{Transport: fn},
		getenv: func(k string) string { return env[k] },
	}
	res, err := tool.Execute(context.Background(), "c1", json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return res
}

// resultText is defined in read_tool_test.go (same package).

func TestSelectSearchBackend(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TAVILY_API_KEY": "t"}, "tavily"},
		{map[string]string{"BRAVE_API_KEY": "b"}, "brave"},
		{map[string]string{"TAVILY_API_KEY": "t", "BRAVE_API_KEY": "b"}, "tavily"},
		{map[string]string{}, "duckduckgo"},
		{map[string]string{"PIGO_WEBSEARCH_BACKEND": "parallel", "TAVILY_API_KEY": "t", "BRAVE_API_KEY": "b"}, "parallel"},
		{map[string]string{"PIGO_WEBSEARCH_BACKEND": " Parallel "}, "parallel"},
		{map[string]string{"PIGO_WEBSEARCH_BACKEND": "auto", "BRAVE_API_KEY": "b"}, "brave"},
		{map[string]string{"PARALLEL_API_KEY": "unused"}, "duckduckgo"},
	}
	for _, c := range cases {
		backend, err := selectSearchBackend(func(k string) string { return c.env[k] })
		if err != nil {
			t.Fatal(err)
		}
		got := backend.name()
		if got != c.want {
			t.Errorf("env %v: backend = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestWebSearchTavily(t *testing.T) {
	body := `{"results":[{"title":"Go","url":"https://go.dev","content":"The Go language"},{"title":"Docs","url":"https://pkg.go.dev","content":"packages"}]}`
	var gotAuth string
	res := execWebSearch(t, map[string]string{"TAVILY_API_KEY": "secret"},
		func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.tavily.com" {
				t.Errorf("unexpected host %q", r.URL.Host)
			}
			gotAuth = r.Header.Get("Authorization")
			return makeResp(200, "application/json", body), nil
		}, `{"query":"go language"}`)

	txt := resultText(res)
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", gotAuth)
	}
	if !strings.Contains(txt, "via tavily") || !strings.Contains(txt, "https://go.dev") || !strings.Contains(txt, "The Go language") {
		t.Errorf("unexpected result:\n%s", txt)
	}
	if bk, _ := res.Details.(map[string]any)["backend"].(string); bk != "tavily" {
		t.Errorf("Details.backend = %q, want tavily", bk)
	}
}

func TestWebSearchBraveStripsHTML(t *testing.T) {
	body := `{"web":{"results":[{"title":"Rust <strong>lang</strong>","url":"https://rust-lang.org","description":"A <strong>systems</strong> language"}]}}`
	var gotToken string
	res := execWebSearch(t, map[string]string{"BRAVE_API_KEY": "tok"},
		func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.search.brave.com" {
				t.Errorf("unexpected host %q", r.URL.Host)
			}
			gotToken = r.Header.Get("X-Subscription-Token")
			return makeResp(200, "application/json", body), nil
		}, `{"query":"rust"}`)

	txt := resultText(res)
	if gotToken != "tok" {
		t.Errorf("X-Subscription-Token = %q, want tok", gotToken)
	}
	if strings.Contains(txt, "<strong>") {
		t.Errorf("HTML tags not stripped:\n%s", txt)
	}
	if !strings.Contains(txt, "Rust lang") || !strings.Contains(txt, "A systems language") {
		t.Errorf("unexpected result:\n%s", txt)
	}
}

func TestWebSearchDuckDuckGo(t *testing.T) {
	html := `<div class="result">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&rut=x">First Title</a>
      <a class="result__snippet">First snippet</a>
    </div>
    <div class="result">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fb">Second Title</a>
      <a class="result__snippet">Second snippet</a>
    </div>`
	res := execWebSearch(t, map[string]string{},
		func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "html.duckduckgo.com" {
				t.Errorf("unexpected host %q", r.URL.Host)
			}
			return makeResp(200, "text/html", html), nil
		}, `{"query":"anything"}`)

	txt := resultText(res)
	if !strings.Contains(txt, "https://example.com/a") || !strings.Contains(txt, "https://example.org/b") {
		t.Errorf("redirect URLs not decoded:\n%s", txt)
	}
	if !strings.Contains(txt, "First Title") || !strings.Contains(txt, "Second snippet") {
		t.Errorf("titles/snippets missing:\n%s", txt)
	}
}

func TestWebSearchDomainFilter(t *testing.T) {
	body := `{"results":[{"title":"A","url":"https://keep.com/x","content":"a"},{"title":"B","url":"https://drop.com/y","content":"b"}]}`
	res := execWebSearch(t, map[string]string{"TAVILY_API_KEY": "k"},
		func(r *http.Request) (*http.Response, error) {
			return makeResp(200, "application/json", body), nil
		}, `{"query":"q","allowed_domains":["keep.com"]}`)

	txt := resultText(res)
	if strings.Contains(txt, "drop.com") {
		t.Errorf("blocked domain leaked:\n%s", txt)
	}
	if !strings.Contains(txt, "keep.com") {
		t.Errorf("allowed domain dropped:\n%s", txt)
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	res := execWebSearch(t, map[string]string{},
		func(r *http.Request) (*http.Response, error) {
			t.Error("transport should not be called for empty query")
			return makeResp(200, "text/html", ""), nil
		}, `{"query":"   "}`)
	if !strings.Contains(resultText(res), "query is required") {
		t.Errorf("want query-required error, got:\n%s", resultText(res))
	}
}

func TestWebSearchBackendError(t *testing.T) {
	res := execWebSearch(t, map[string]string{"TAVILY_API_KEY": "k"},
		func(r *http.Request) (*http.Response, error) {
			return makeResp(500, "application/json", "boom"), nil
		}, `{"query":"q"}`)
	if !strings.Contains(resultText(res), "tavily backend failed") {
		t.Errorf("want backend-failed error, got:\n%s", resultText(res))
	}
}
