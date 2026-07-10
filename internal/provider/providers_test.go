package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// captureServer records the last request path, headers, and decoded JSON body,
// then replays a canned SSE stream. It lets the provider tests assert the wire
// shape a driver produced without a live upstream.
type captureServer struct {
	srv     *httptest.Server
	path    string
	headers http.Header
	body    map[string]any
}

func newCaptureServer(t *testing.T, sseBody string) *captureServer {
	t.Helper()
	cs := &captureServer{}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.path = r.URL.Path
		cs.headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &cs.body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseBody))
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

// drainStream collects event kinds and the final message from a provider stream.
func drainStream(t *testing.T, stream *AssistantMessageEventStream) ([]string, agentcore.AssistantMessage) {
	t.Helper()
	var kinds []string
	for ev := range stream.Events() {
		kinds = append(kinds, ev.EventKind())
	}
	final, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}
	return kinds, final
}

// TestOpenRouterProviderStreamsChatCompletions drives OpenRouter (the reference
// OpenAI-compatible provider) end to end: the driver must POST to
// /chat/completions with a Bearer token and stream through OpenAIDecoder.
func TestOpenRouterProviderStreamsChatCompletions(t *testing.T) {
	cs := newCaptureServer(t, openaiToolCallSSE)
	p := NewOpenRouterProvider(cs.srv.URL, []Model{{Provider: "openrouter", ID: "openai/gpt-4o"}})

	if p.Name() != "openrouter" {
		t.Errorf("name = %q, want openrouter", p.Name())
	}
	stream, err := p.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "openai/gpt-4o",
		Context: LlmContext{SystemPrompt: "be brief", Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}}}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	kinds, final := drainStream(t, stream)
	if kinds[len(kinds)-1] != StreamEventDone {
		t.Errorf("last event = %q, want done", kinds[len(kinds)-1])
	}
	if final.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", final.StopReason)
	}

	// Wire assertions: path, auth header, and request body shape.
	if cs.path != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", cs.path)
	}
	if got := cs.headers.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("auth header = %q, want Bearer sk-test", got)
	}
	if cs.body["stream"] != true {
		t.Errorf("stream flag = %v, want true", cs.body["stream"])
	}
	if cs.body["model"] != "openai/gpt-4o" {
		t.Errorf("model = %v, want openai/gpt-4o", cs.body["model"])
	}
	msgs, _ := cs.body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2 (system+user)", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Errorf("system message = %v", first)
	}
}

// TestOpenRouterMissingKeyIsEarlyError verifies a missing API key is the early
// "cannot build the stream" returned error, and the provider name is named
// without leaking any value.
func TestOpenRouterMissingKeyIsEarlyError(t *testing.T) {
	p := NewOpenRouterProvider("", nil)
	_, err := p.StreamCompletion(context.Background(), CompletionRequest{Model: "m"})
	if err == nil {
		t.Fatal("missing API key must return an early error")
	}
	if !strings.Contains(err.Error(), "openrouter") {
		t.Errorf("error should name the provider, got %v", err)
	}
}

// TestOllamaProviderNoAuth verifies the local Ollama provider sends no
// Authorization header and still streams through OpenAIDecoder.
func TestOllamaProviderNoAuth(t *testing.T) {
	cs := newCaptureServer(t, openaiToolCallSSE)
	p := NewOllamaProvider(cs.srv.URL, []Model{{Provider: "ollama", ID: "llama3"}})
	if p.Name() != "ollama" {
		t.Errorf("name = %q, want ollama", p.Name())
	}
	// No API key configured — Ollama must not require one.
	stream, err := p.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "llama3",
		Context: LlmContext{Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}}}},
	})
	if err != nil {
		t.Fatalf("StreamCompletion (no auth): %v", err)
	}
	_, final := drainStream(t, stream)
	if final.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", final.StopReason)
	}
	if got := cs.headers.Get("Authorization"); got != "" {
		t.Errorf("Ollama must send no auth header, got %q", got)
	}
}

// TestBedrockProviderStreamsAnthropicWire verifies the Bedrock provider POSTs
// the Anthropic Messages wire format (system top-level, max_tokens present) and
// streams through AnthropicDecoder.
func TestBedrockProviderStreamsAnthropicWire(t *testing.T) {
	cs := newCaptureServer(t, anthropicToolUseSSE)
	p := NewBedrockProvider(cs.srv.URL, []Model{{Provider: "bedrock", ID: "anthropic.claude-3"}})
	if p.Name() != "bedrock" {
		t.Errorf("name = %q, want bedrock", p.Name())
	}
	stream, err := p.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "anthropic.claude-3",
		Context: LlmContext{SystemPrompt: "sys", Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}}}},
		Config:  StreamConfig{APIKey: "bedrock-key"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	kinds, _ := drainStream(t, stream)
	if kinds[len(kinds)-1] != StreamEventDone {
		t.Errorf("last event = %q, want done", kinds[len(kinds)-1])
	}
	if cs.body["system"] != "sys" {
		t.Errorf("system = %v, want top-level 'sys'", cs.body["system"])
	}
	if cs.body["max_tokens"] == nil {
		t.Error("Anthropic wire requires max_tokens")
	}
	if cs.body["stream"] != true {
		t.Errorf("stream flag = %v, want true", cs.body["stream"])
	}
}

// TestBedrockMissingKeyIsEarlyError mirrors the OpenRouter early-error check.
func TestBedrockMissingKeyIsEarlyError(t *testing.T) {
	p := NewBedrockProvider("", nil)
	_, err := p.StreamCompletion(context.Background(), CompletionRequest{Model: "m"})
	if err == nil {
		t.Fatal("missing API key must return an early error")
	}
	if !strings.Contains(err.Error(), "bedrock") {
		t.Errorf("error should name the provider, got %v", err)
	}
}

// TestProvidersRegisterInModelRegistry verifies the shared driver providers plug
// into the model registry (US-011) and resolve their models.
func TestProvidersRegisterInModelRegistry(t *testing.T) {
	reg := NewModelRegistry()
	if err := reg.RegisterProvider(NewOpenRouterProvider("", []Model{{Provider: "openrouter", ID: "or-model"}})); err != nil {
		t.Fatalf("register openrouter: %v", err)
	}
	if err := reg.RegisterProvider(NewOllamaProvider("", []Model{{Provider: "ollama", ID: "ol-model"}})); err != nil {
		t.Fatalf("register ollama: %v", err)
	}
	m, prov, err := reg.Resolve("or-model")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if m.ID != "or-model" || prov.Name() != "openrouter" {
		t.Errorf("resolved %+v via %q", m, prov.Name())
	}
}
