// This file implements the webfetch tool (US-012, #128): fetch a URL and return
// its main text as simplified Markdown. pi has no such tool; this mirrors Claude
// Code's WebFetch. Safety properties required by the issue:
//
//   - HTTP URLs are upgraded to HTTPS before the request.
//   - Cross-origin redirects are NOT followed automatically; the redirect target
//     is returned to the caller (the model) so it can decide whether to fetch it.
//   - A request timeout and a response-body size cap bound the work.
//   - A failed fetch (timeout, non-2xx, unreachable) degrades to a structured
//     error result, never a panic.
//
// The optional "prompt" argument is accepted and echoed back in the result
// framing so the model keeps its intent alongside the fetched content; the tool
// does not itself call a model to summarize (that is the agent loop's job).
package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// webFetchTimeout bounds a single fetch. webFetchMaxBytes caps how much of the
// response body is read (protects the model's context and memory from huge
// pages). webFetchMaxMarkdown caps the rendered Markdown length.
const (
	webFetchTimeout     = 30 * time.Second
	webFetchMaxBytes    = 5 * 1024 * 1024
	webFetchMaxMarkdown = 100 * 1024
)

// WebFetchTool fetches a URL and returns its text as simplified Markdown. The
// zero value is usable; Client defaults to a redirect-blocking http.Client with
// webFetchTimeout.
type WebFetchTool struct {
	// Client performs the HTTP request. When nil, a default client is built that
	// blocks cross-origin redirects and enforces webFetchTimeout. Injected for
	// tests so a fake transport can serve canned responses.
	Client *http.Client
}

// webFetchArgs is the decoded argument shape for WebFetchTool.
type webFetchArgs struct {
	// URL is the page to fetch. An http:// URL is upgraded to https://.
	URL string `json:"url"`
	// Prompt is an optional instruction describing what the caller wants from the
	// page; it is echoed into the result framing, not acted on by the tool.
	Prompt string `json:"prompt,omitempty"`
}

// Name implements AgentTool.
func (t *WebFetchTool) Name() string { return "webfetch" }

// Description implements AgentTool.
func (t *WebFetchTool) Description() string {
	return "Fetch a URL and return its main text content as simplified Markdown. " +
		"HTTP URLs are upgraded to HTTPS. Cross-origin redirects are not followed; " +
		"the redirect target is returned so you can fetch it explicitly. Use the " +
		"optional prompt to note what you are looking for on the page."
}

// Schema implements AgentTool.
func (t *WebFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "url":    {"type": "string", "description": "The URL to fetch. http:// is upgraded to https://."},
    "prompt": {"type": "string", "description": "Optional: what to extract or look for on the page."}
  },
  "required": ["url"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. A fetch has no local side effects and is
// safe to run alongside other reads → parallel.
func (t *WebFetchTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// errRedirectBlocked is returned by the client's CheckRedirect to stop a
// cross-origin redirect; the target is carried so Execute can report it.
type errRedirectBlocked struct{ target string }

func (e *errRedirectBlocked) Error() string { return "cross-origin redirect blocked to " + e.target }

// newWebFetchClient builds the default redirect-blocking client. A redirect is
// allowed only when it stays on the same host (scheme+host); a cross-origin hop
// stops with errRedirectBlocked carrying the target URL.
func newWebFetchClient() *http.Client {
	return &http.Client{
		Timeout: webFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			orig := via[0].URL
			if req.URL.Host != orig.Host || req.URL.Scheme != orig.Scheme {
				return &errRedirectBlocked{target: req.URL.String()}
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// Execute implements AgentTool. Fetch failures are encoded as error results (the
// returned Go error is always nil), matching the file tools' contract.
func (t *WebFetchTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[webFetchArgs](args, "webfetch")
	if bad != nil {
		return *bad, nil
	}
	raw := strings.TrimSpace(a.URL)
	if raw == "" {
		return errorResult("webfetch: url is required"), nil
	}

	target, err := normalizeFetchURL(raw)
	if err != nil {
		return errorResult("webfetch: " + err.Error()), nil
	}

	client := t.Client
	if client == nil {
		client = newWebFetchClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return errorResult("webfetch: " + err.Error()), nil
	}
	req.Header.Set("User-Agent", "pigo-webfetch/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		// A blocked cross-origin redirect is reported specially so the model can
		// choose to fetch the target explicitly. errors.As unwraps the *url.Error
		// http.Client wraps CheckRedirect failures in.
		var blocked *errRedirectBlocked
		if errors.As(err, &blocked) {
			return agentcore.AgentToolResult{
				Content: agentcore.ContentList{agentcore.NewTextContent(
					fmt.Sprintf("webfetch: cross-origin redirect not followed.\nTarget: %s\nFetch it explicitly if you want its content.", blocked.target))},
				Details: map[string]any{"redirect": blocked.target, "followed": false},
			}, nil
		}
		return errorResult(fmt.Sprintf("webfetch: request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorResult(fmt.Sprintf("webfetch: %s returned HTTP %d %s", target, resp.StatusCode, http.StatusText(resp.StatusCode))), nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes))
	if err != nil {
		return errorResult(fmt.Sprintf("webfetch: reading response body: %v", err)), nil
	}

	ctype := resp.Header.Get("Content-Type")
	var text string
	if strings.Contains(ctype, "html") || looksLikeHTML(body) {
		text = htmlToMarkdown(body)
	} else {
		text = string(body)
	}
	text = strings.TrimSpace(text)
	truncated := false
	if len(text) > webFetchMaxMarkdown {
		text = text[:webFetchMaxMarkdown]
		truncated = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Fetched %s (HTTP %d)\n", target, resp.StatusCode)
	if a.Prompt != "" {
		fmt.Fprintf(&b, "Prompt: %s\n", a.Prompt)
	}
	if truncated {
		b.WriteString("(content truncated)\n")
	}
	b.WriteString("\n")
	b.WriteString(text)

	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(b.String())},
		Details: map[string]any{"url": target, "status": resp.StatusCode, "truncated": truncated},
	}, nil
}

// normalizeFetchURL parses raw, upgrades an http scheme to https, and rejects
// anything that is not an absolute http(s) URL with a host.
func normalizeFetchURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid url: %v", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "https" // upgrade
	case "https":
		// ok
	case "":
		return "", fmt.Errorf("url must be absolute with an http(s) scheme: %q", raw)
	default:
		return "", fmt.Errorf("unsupported url scheme %q (want http or https)", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("url has no host: %q", raw)
	}
	return u.String(), nil
}

// looksLikeHTML sniffs whether body begins with an HTML marker, used when the
// server omits or mislabels Content-Type.
func looksLikeHTML(body []byte) bool {
	head := strings.ToLower(strings.TrimSpace(string(body[:min(512, len(body))])))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") || strings.Contains(head, "<body")
}
