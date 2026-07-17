// Tests for the webfetch tool (US-012, #128): URL normalization (http→https),
// HTML→Markdown reduction, size/timeout bounds, cross-origin redirect blocking,
// and structured errors on failure. A fake RoundTripper serves canned responses
// so no network is touched.
package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// mustParse parses a URL or fails the test.
func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// errorAsRedirect unwraps err onto *errRedirectBlocked.
func errorAsRedirect(err error, target **errRedirectBlocked) bool {
	return errors.As(err, target)
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// makeResp builds a canned *http.Response with the given status/content-type/body.
func makeResp(status int, ctype, body string) *http.Response {
	h := http.Header{}
	if ctype != "" {
		h.Set("Content-Type", ctype)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// execWebFetch runs the tool with a client whose transport is fn.
func execWebFetch(t *testing.T, fn roundTripFunc, args string) agentcore.AgentToolResult {
	t.Helper()
	tool := &WebFetchTool{Client: &http.Client{Transport: fn}}
	res, err := tool.Execute(context.Background(), "c1", json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return res
}

// TestWebFetchUpgradesHTTP checks an http:// URL is fetched over https://.
func TestWebFetchUpgradesHTTP(t *testing.T) {
	var gotURL string
	res := execWebFetch(t, func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		return makeResp(200, "text/html", "<html><body><p>hi</p></body></html>"), nil
	}, `{"url":"http://example.com/page"}`)
	if !strings.HasPrefix(gotURL, "https://") {
		t.Errorf("request URL = %q, want https upgrade", gotURL)
	}
	if txt := agentcore.ContentToText(res.Content); !strings.Contains(txt, "hi") {
		t.Errorf("result missing body text: %q", txt)
	}
}

// TestWebFetchHTMLToMarkdown checks basic HTML is reduced to Markdown.
func TestWebFetchHTMLToMarkdown(t *testing.T) {
	body := `<html><body><h1>Title</h1><p>A <a href="https://x.io">link</a> here.</p><script>ignore()</script></body></html>`
	res := execWebFetch(t, func(r *http.Request) (*http.Response, error) {
		return makeResp(200, "text/html; charset=utf-8", body), nil
	}, `{"url":"https://example.com"}`)
	txt := agentcore.ContentToText(res.Content)
	if !strings.Contains(txt, "# Title") {
		t.Errorf("missing heading markdown in %q", txt)
	}
	if !strings.Contains(txt, "[link](https://x.io)") {
		t.Errorf("missing link markdown in %q", txt)
	}
	if strings.Contains(txt, "ignore()") {
		t.Errorf("script content leaked into output: %q", txt)
	}
}

// TestWebFetchNon2xxIsError checks a non-2xx status degrades to a structured
// error result (not a panic, not a Go error).
func TestWebFetchNon2xxIsError(t *testing.T) {
	res := execWebFetch(t, func(r *http.Request) (*http.Response, error) {
		return makeResp(404, "text/html", "not found"), nil
	}, `{"url":"https://example.com/missing"}`)
	txt := agentcore.ContentToText(res.Content)
	if !strings.Contains(txt, "HTTP 404") {
		t.Errorf("expected HTTP 404 error, got %q", txt)
	}
}

// TestWebFetchPromptEchoed checks the optional prompt is echoed into the framing.
func TestWebFetchPromptEchoed(t *testing.T) {
	res := execWebFetch(t, func(r *http.Request) (*http.Response, error) {
		return makeResp(200, "text/plain", "plain body"), nil
	}, `{"url":"https://example.com","prompt":"find the price"}`)
	if txt := agentcore.ContentToText(res.Content); !strings.Contains(txt, "Prompt: find the price") {
		t.Errorf("prompt not echoed: %q", txt)
	}
}

// TestWebFetchRejectsBadScheme checks a non-http(s) scheme is rejected up front.
func TestWebFetchRejectsBadScheme(t *testing.T) {
	res := execWebFetch(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport should not be called for a bad scheme")
		return nil, nil
	}, `{"url":"ftp://example.com/file"}`)
	if txt := agentcore.ContentToText(res.Content); !strings.Contains(txt, "unsupported url scheme") {
		t.Errorf("expected scheme rejection, got %q", txt)
	}
}

// TestWebFetchMissingURL checks an empty url is rejected.
func TestWebFetchMissingURL(t *testing.T) {
	tool := &WebFetchTool{}
	res, err := tool.Execute(context.Background(), "c1", json.RawMessage(`{"url":"  "}`), nil)
	if err != nil {
		t.Fatalf("Execute Go error: %v", err)
	}
	if txt := agentcore.ContentToText(res.Content); !strings.Contains(txt, "url is required") {
		t.Errorf("expected url-required error, got %q", txt)
	}
}

// TestWebFetchCrossOriginRedirectBlocked drives the real redirect-blocking
// client (newWebFetchClient) via CheckRedirect: a cross-origin redirect must not
// be followed, and the target is reported back.
func TestWebFetchCrossOriginRedirectBlocked(t *testing.T) {
	client := newWebFetchClient()
	// Two hops: same-host allowed, cross-host blocked.
	same := mustParse(t, "https://a.example.com/1")
	cross := mustParse(t, "https://b.other.com/2")

	// Same-origin redirect: allowed (nil error).
	viaSame := []*http.Request{{URL: mustParse(t, "https://a.example.com/0")}}
	if err := client.CheckRedirect(&http.Request{URL: same}, viaSame); err != nil {
		t.Errorf("same-origin redirect blocked unexpectedly: %v", err)
	}

	// Cross-origin redirect: blocked with target carried.
	viaCross := []*http.Request{{URL: mustParse(t, "https://a.example.com/0")}}
	err := client.CheckRedirect(&http.Request{URL: cross}, viaCross)
	var blocked *errRedirectBlocked
	if err == nil || !errorAsRedirect(err, &blocked) {
		t.Fatalf("cross-origin redirect not blocked: %v", err)
	}
	if blocked.target != "https://b.other.com/2" {
		t.Errorf("blocked target = %q", blocked.target)
	}
}

// TestNormalizeFetchURL covers the scheme/host rules directly.
func TestNormalizeFetchURL(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"http://x.com/a", "https://x.com/a", false},
		{"https://x.com", "https://x.com", false},
		{"x.com/a", "", true},     // no scheme
		{"ftp://x.com", "", true}, // bad scheme
		{"https://", "", true},    // no host
	}
	for _, c := range cases {
		got, err := normalizeFetchURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeFetchURL(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeFetchURL(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeFetchURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
