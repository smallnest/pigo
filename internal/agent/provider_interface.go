// This file defines the unified Provider interface and its dual failure model
// (US-007). A Provider turns a CompletionRequest into a stream of
// AssistantMessageEvents. Failures follow the same contract as StreamFn (FR-13):
//
//   - "cannot even build the stream" (bad config, missing model) → returned error.
//   - any runtime failure once streaming has begun → a terminal StreamErrorEvent
//     carrying an assistant message with stopReason=error/aborted, after which
//     the stream is closed. It is never a returned error.
//
// Model carries provider-agnostic capability metadata so the loop and UI can
// reason about a model without knowing the concrete provider.
package agent

import "context"

// Model is provider-agnostic metadata describing a single model's identity and
// capabilities. Providers construct these; the loop/UI consume them.
type Model struct {
	// Provider is the provider name (e.g. "anthropic", "openai").
	Provider string `json:"provider"`
	// ID is the provider-specific model id (e.g. "claude-opus-4-8").
	ID string `json:"id"`
	// DisplayName is a human-friendly label; falls back to ID when empty.
	DisplayName string `json:"displayName,omitempty"`
	// ContextWindow is the maximum input+output token window, 0 if unknown.
	ContextWindow int `json:"contextWindow,omitempty"`
	// MaxOutputTokens is the max tokens the model may emit per response, 0 if
	// unknown.
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	// SupportsThinking reports whether the model exposes a reasoning/thinking
	// channel.
	SupportsThinking bool `json:"supportsThinking,omitempty"`
	// SupportsTools reports whether the model can call tools.
	SupportsTools bool `json:"supportsTools,omitempty"`
	// ThinkingLevels maps unified thinking levels to this model's wire values.
	// nil when the model does not support thinking (decision #10).
	ThinkingLevels ThinkingLevelMap `json:"-"`
}

// CompletionRequest is the provider-agnostic input to StreamCompletion: the
// model id, the shaped LLM context, and per-request options.
type CompletionRequest struct {
	// Model is the provider-specific model id to complete against.
	Model string
	// Context is the shaped request (system prompt, LLM-bound messages, tools).
	Context LlmContext
	// Config carries per-request options (API key, thinking level, extras).
	Config StreamConfig
}

// Provider is the unified streaming interface implemented by every backend. It
// hides per-vendor differences behind a single AssistantMessageEvent stream.
type Provider interface {
	// Name returns the provider's identifier (matches Model.Provider).
	Name() string
	// Models lists the models this provider can serve.
	Models() []Model
	// StreamCompletion streams a completion for req. Per the dual failure model
	// it returns an error only for the earliest "cannot build the stream" case;
	// all runtime failures ride the returned stream as a terminal error event.
	StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error)
}

// StreamFnFromProvider adapts a Provider to the loop's StreamFn contract so a
// Provider can drive streamAssistantResponse directly. The two failure models
// are identical, so the adaptation is a straight delegation.
func StreamFnFromProvider(p Provider) StreamFn {
	return func(ctx context.Context, model string, llm LlmContext, cfg StreamConfig) (*AssistantMessageEventStream, error) {
		return p.StreamCompletion(ctx, CompletionRequest{Model: model, Context: llm, Config: cfg})
	}
}
