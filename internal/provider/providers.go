// This file implements the first concrete Providers (US-013): Bedrock,
// OpenRouter, and Ollama. Each satisfies the Provider interface by building an
// *http.Request and delegating to the shared transport (StreamRequest) with a
// provider-appropriate Decoder — no bespoke HTTP/SSE handling per provider.
//
// Two backing driver shapes cover all three:
//
//   - openAICompatDriver — POSTs to {baseURL}/chat/completions in the OpenAI
//     Chat Completions wire format, decoded by OpenAIDecoder. OpenRouter and
//     Ollama are instances of it; it is the generic OpenAI-compatible layer,
//     reusable for any gateway (Groq, together, local servers, …).
//   - anthropicCompatDriver — POSTs the Anthropic Messages wire format, decoded
//     by AnthropicDecoder. Bedrock rides this: Anthropic-on-Bedrock speaks the
//     Messages API, so the decoder is reused wholesale.
//
// Failures follow the dual failure model (FR-13): only the earliest "cannot
// build the stream" case (missing key, bad request construction) is a returned
// error; every runtime failure rides the stream as a terminal error event,
// which StreamRequest already guarantees.
//
// Security (US-012 / US-026): API keys are referenced by provider name in any
// error; secret values are never logged or embedded in error text.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// providerBaseURLs holds the default endpoint per built-in provider. A base URL
// is a transport concern (the decoders are URL-agnostic), so overriding it —
// e.g. pointing Ollama at a remote host — needs no decoder change.
const (
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	ollamaBaseURL     = "http://localhost:11434/v1"
	// bedrockBaseURL is a placeholder default; real Bedrock endpoints are
	// region-specific (bedrock-runtime.<region>.amazonaws.com) and supplied at
	// construction. It is exported as a field so callers set the resolved URL.
	bedrockBaseURL = "https://bedrock-runtime.us-east-1.amazonaws.com"
)

// ---------------------------------------------------------------------------
// OpenAI-compatible driver (OpenRouter, Ollama, and any OpenAI-compatible API).
// ---------------------------------------------------------------------------

// openAICompatDriver is the shared backing for every OpenAI-compatible
// provider. It holds the provider identity, endpoint, model catalog, and the
// auth scheme; StreamCompletion builds the chat-completions request and hands
// it to the transport with a fresh OpenAIDecoder.
type openAICompatDriver struct {
	name    string
	baseURL string
	models  []Model
	// requiresAuth reports whether an Authorization: Bearer header is sent.
	// Ollama (local) needs none; OpenRouter does.
	requiresAuth bool
	// extraHeaders are attached to every request (e.g. OpenRouter attribution).
	extraHeaders map[string]string
}

func (d *openAICompatDriver) Name() string    { return d.name }
func (d *openAICompatDriver) Models() []Model { return d.models }

// StreamCompletion builds the OpenAI Chat Completions request and streams it.
func (d *openAICompatDriver) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if d.requiresAuth && strings.TrimSpace(req.Config.APIKey) == "" {
		// Early "cannot build the stream": reference the provider, never a value.
		return nil, fmt.Errorf("%s: missing API key", d.name)
	}
	body, err := encodeOpenAIRequest(req)
	if err != nil {
		return nil, fmt.Errorf("%s: build request body: %w", d.name, err)
	}
	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if d.requiresAuth {
			httpReq.Header.Set("Authorization", "Bearer "+req.Config.APIKey)
		}
		for k, v := range d.extraHeaders {
			httpReq.Header.Set(k, v)
		}
		return httpReq, nil
	}
	return StreamRequest(ctx, TransportConfig{NewRequest: newReq, Decoder: NewOpenAIDecoder()})
}

// encodeOpenAIRequest serializes a CompletionRequest into an OpenAI Chat
// Completions JSON body with streaming enabled and usage requested.
func encodeOpenAIRequest(req CompletionRequest) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(req.Context.Messages)+1)
	if sp := req.Context.SystemPrompt; sp != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": sp})
	}
	for _, m := range req.Context.Messages {
		msgs = append(msgs, encodeOpenAIMessage(m)...)
	}
	body := map[string]any{
		"model":          req.Model,
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if tools := encodeOpenAITools(req.Context.Tools); len(tools) > 0 {
		body["tools"] = tools
	}
	return json.Marshal(body)
}

// encodeOpenAIMessage maps one pigo message onto the OpenAI wire shape. An
// assistant message may expand to content + tool_calls in a single entry; a
// tool result becomes a role:"tool" entry keyed by tool_call_id.
func encodeOpenAIMessage(m agentcore.Message) []map[string]any {
	switch msg := m.(type) {
	case agentcore.UserMessage:
		return []map[string]any{{"role": "user", "content": agentcore.ContentToText(msg.Content)}}
	case agentcore.AssistantMessage:
		entry := map[string]any{"role": "assistant"}
		entry["content"] = agentcore.ContentToText(msg.Content)
		var toolCalls []map[string]any
		for _, c := range msg.Content {
			if tc, ok := c.(agentcore.ToolCallContent); ok {
				toolCalls = append(toolCalls, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": string(tc.Arguments),
					},
				})
			}
		}
		if len(toolCalls) > 0 {
			entry["tool_calls"] = toolCalls
		}
		return []map[string]any{entry}
	case agentcore.ToolResultMessage:
		return []map[string]any{{
			"role":         "tool",
			"tool_call_id": msg.ToolCallID,
			"content":      agentcore.ContentToText(msg.Content),
		}}
	default:
		return nil
	}
}

// encodeOpenAITools maps AgentTools onto the OpenAI function-tool schema.
func encodeOpenAITools(tools []agentcore.AgentTool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := json.RawMessage(t.Schema())
		if len(params) == 0 {
			params = json.RawMessage("{}")
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"parameters":  params,
			},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Anthropic-compatible driver (Bedrock).
// ---------------------------------------------------------------------------

// anthropicCompatDriver backs Anthropic-wire providers that are not the direct
// Anthropic API — Bedrock being the case here. It POSTs the Messages wire
// format and decodes with AnthropicDecoder.
type anthropicCompatDriver struct {
	name    string
	baseURL string
	models  []Model
	// path is the endpoint path appended to baseURL (Bedrock's invoke path
	// embeds the model id, so it is derived per request).
	pathFor func(model string) string
	// authHeader sets provider auth on the request (never logs the value).
	authHeader func(req *http.Request, apiKey string)
}

func (d *anthropicCompatDriver) Name() string    { return d.name }
func (d *anthropicCompatDriver) Models() []Model { return d.models }

// StreamCompletion builds the Anthropic Messages request and streams it,
// decoding with AnthropicDecoder.
func (d *anthropicCompatDriver) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if strings.TrimSpace(req.Config.APIKey) == "" {
		return nil, fmt.Errorf("%s: missing API key", d.name)
	}
	body, err := encodeAnthropicRequest(req)
	if err != nil {
		return nil, fmt.Errorf("%s: build request body: %w", d.name, err)
	}
	path := "/messages"
	if d.pathFor != nil {
		path = d.pathFor(req.Model)
	}
	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if d.authHeader != nil {
			d.authHeader(httpReq, req.Config.APIKey)
		}
		return httpReq, nil
	}
	return StreamRequest(ctx, TransportConfig{NewRequest: newReq, Decoder: NewAnthropicDecoder()})
}

// encodeAnthropicRequest serializes a CompletionRequest into an Anthropic
// Messages JSON body with streaming enabled. The system prompt is a top-level
// field; tool results and tool calls follow the Messages content-block shape.
func encodeAnthropicRequest(req CompletionRequest) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(req.Context.Messages))
	for _, m := range req.Context.Messages {
		if enc := encodeAnthropicMessage(m); enc != nil {
			msgs = append(msgs, enc)
		}
	}
	body := map[string]any{
		"messages": msgs,
		"stream":   true,
	}
	if sp := req.Context.SystemPrompt; sp != "" {
		body["system"] = sp
	}
	if maxTok := maxOutputTokensFor(req); maxTok > 0 {
		body["max_tokens"] = maxTok
	} else {
		// Anthropic requires max_tokens; supply a safe default when unknown.
		body["max_tokens"] = 4096
	}
	if tools := encodeAnthropicTools(req.Context.Tools); len(tools) > 0 {
		body["tools"] = tools
	}
	return json.Marshal(body)
}

// encodeAnthropicMessage maps one pigo message onto the Anthropic Messages
// wire shape. Assistant tool calls become tool_use blocks; tool results become
// a user message carrying a tool_result block (Anthropic's convention).
func encodeAnthropicMessage(m agentcore.Message) map[string]any {
	switch msg := m.(type) {
	case agentcore.UserMessage:
		return map[string]any{"role": "user", "content": agentcore.ContentToText(msg.Content)}
	case agentcore.AssistantMessage:
		var blocks []map[string]any
		for _, c := range msg.Content {
			switch b := c.(type) {
			case agentcore.TextContent:
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case agentcore.ToolCallContent:
				var input any
				_ = json.Unmarshal(b.Arguments, &input)
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": b.ID, "name": b.Name, "input": input,
				})
			}
		}
		if len(blocks) == 0 {
			blocks = []map[string]any{{"type": "text", "text": ""}}
		}
		return map[string]any{"role": "assistant", "content": blocks}
	case agentcore.ToolResultMessage:
		return map[string]any{"role": "user", "content": []map[string]any{{
			"type":        "tool_result",
			"tool_use_id": msg.ToolCallID,
			"content":     agentcore.ContentToText(msg.Content),
		}}}
	default:
		return nil
	}
}

// encodeAnthropicTools maps AgentTools onto the Anthropic tool schema.
func encodeAnthropicTools(tools []agentcore.AgentTool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		schema := json.RawMessage(t.Schema())
		if len(schema) == 0 {
			schema = json.RawMessage("{}")
		}
		out = append(out, map[string]any{
			"name":         t.Name(),
			"description":  t.Description(),
			"input_schema": schema,
		})
	}
	return out
}

// maxOutputTokensFor pulls a max-output hint from the request Extra map, if any,
// so callers can bound Anthropic responses without a registry lookup here.
func maxOutputTokensFor(req CompletionRequest) int {
	if req.Config.Extra == nil {
		return 0
	}
	switch v := req.Config.Extra["max_tokens"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Constructors.
// ---------------------------------------------------------------------------

// NewOpenRouterProvider builds the OpenRouter provider — the reference
// OpenAI-compatible gateway. baseURL defaults to the public endpoint when empty.
func NewOpenRouterProvider(baseURL string, models []Model) Provider {
	if baseURL == "" {
		baseURL = openRouterBaseURL
	}
	return &openAICompatDriver{
		name:         "openrouter",
		baseURL:      baseURL,
		models:       models,
		requiresAuth: true,
		extraHeaders: map[string]string{
			// OpenRouter attribution headers (optional but recommended).
			"HTTP-Referer": "https://github.com/smallnest/pigo",
			"X-Title":      "pigo",
		},
	}
}

// NewOllamaProvider builds the Ollama provider (local, OpenAI-compatible, no
// auth). baseURL defaults to the local daemon when empty.
func NewOllamaProvider(baseURL string, models []Model) Provider {
	if baseURL == "" {
		baseURL = ollamaBaseURL
	}
	return &openAICompatDriver{
		name:         "ollama",
		baseURL:      baseURL,
		models:       models,
		requiresAuth: false,
	}
}

// NewBedrockProvider builds the Bedrock provider, reusing the Anthropic Messages
// decoder (Anthropic-on-Bedrock speaks the Messages wire format). baseURL
// defaults to a us-east-1 runtime endpoint when empty; real deployments pass
// the region-specific URL. Auth is a Bearer token (Bedrock API keys); SigV4
// signing, when required, is layered by the caller's HTTP client.
func NewBedrockProvider(baseURL string, models []Model) Provider {
	if baseURL == "" {
		baseURL = bedrockBaseURL
	}
	return &anthropicCompatDriver{
		name:    "bedrock",
		baseURL: baseURL,
		models:  models,
		authHeader: func(req *http.Request, apiKey string) {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		},
	}
}
