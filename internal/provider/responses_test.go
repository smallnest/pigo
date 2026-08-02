package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/option"

	"github.com/smallnest/pigo/internal/agentcore"
)

// roundTripFunc adapts a function to http.RoundTripper so a test can stub the
// SDK transport without a live endpoint.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newResponsesTestDriver builds a resp_api driver whose SDK client is pointed at
// the given stub round-tripper, capturing the request path the SDK targets.
func newResponsesTestDriver(baseURL string, rt roundTripFunc) *responsesDriver {
	d := NewOpenAIResponsesProvider("openai", baseURL, nil)
	d.clientOpts = []option.RequestOption{
		option.WithHTTPClient(&http.Client{Transport: rt}),
	}
	return d
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// drain collects the terminal message from a stream, mirroring how the loop
// consumes a provider stream.
func drain(t *testing.T, stream *AssistantMessageEventStream) agentcore.AssistantMessage {
	t.Helper()
	for range stream.Events() {
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result error: %v", err)
	}
	return msg
}

func userMsg(text string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	}
}

func TestResponsesDriverPostsToResponsesEndpoint(t *testing.T) {
	var gotPath, gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		const body = `{"id":"resp_123","model":"gpt-4o","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi there"}]}],"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}`
		return jsonResponse(http.StatusOK, body), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	req := CompletionRequest{
		Model: "gpt-4o",
		Context: LlmContext{
			SystemPrompt: "be terse",
			Messages:     agentcore.MessageList{userMsg("hello")},
		},
		Config: StreamConfig{APIKey: "sk-test"},
	}
	stream, err := d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	if !strings.HasSuffix(gotPath, "/responses") {
		t.Errorf("request path = %q, want to end with /responses", gotPath)
	}
	// The prompt and system instruction must reach the wire body.
	if !strings.Contains(gotBody, "hello") {
		t.Errorf("request body missing prompt: %q", gotBody)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if payload["instructions"] != "be terse" {
		t.Errorf("instructions = %v, want %q", payload["instructions"], "be terse")
	}
	if payload["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o", payload["model"])
	}

	if got := textOf(msg); got != "hi there" {
		t.Errorf("assistant text = %q, want %q", got, "hi there")
	}
	if msg.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn", msg.StopReason)
	}
	if msg.ResponseID != "resp_123" {
		t.Errorf("response id = %q, want resp_123", msg.ResponseID)
	}
	if msg.Usage == nil || msg.Usage.InputTokens != 11 || msg.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want {11 7}", msg.Usage)
	}
	if msg.API != "openai" || msg.Provider != "openai" {
		t.Errorf("tags = api:%q provider:%q, want openai/openai", msg.API, msg.Provider)
	}
}

// A non-2xx from the endpoint must ride the stream as a terminal error event,
// not be returned from StreamCompletion (dual failure model, FR-13).
func TestResponsesDriverUpstreamErrorRidesStream(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	req := CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hello")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	}
	stream, err := d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error on upstream failure: %v", err)
	}

	var sawError bool
	for ev := range stream.Events() {
		if _, ok := ev.(StreamErrorEvent); ok {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected a terminal StreamErrorEvent for a 401 response")
	}
	msg, _ := stream.Result(context.Background())
	if msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}

// A missing API key is the one early "cannot build the stream" error.
func TestResponsesDriverMissingKeyIsEarlyError(t *testing.T) {
	d := NewOpenAIResponsesProvider("openai", "https://api.openai.test/v1", nil)
	_, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:  "gpt-4o",
		Config: StreamConfig{APIKey: "  "},
	})
	if err == nil {
		t.Fatal("expected early error for missing API key")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error = %q, want to mention missing API key", err.Error())
	}
}

// textOf returns the concatenated text content of an assistant message.
func textOf(m agentcore.AssistantMessage) string {
	var b bytes.Buffer
	for _, c := range m.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
