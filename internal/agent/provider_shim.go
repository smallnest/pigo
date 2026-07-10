// This file re-exports the symbols moved to internal/provider (US-002 of the
// agent package split) via type aliases, var bindings, and const
// re-declarations, so the remaining files in package agent (loop.go,
// stream_response.go, config.go, headless.go, etc.) compile unchanged during
// the transition. It will be removed as later steps migrate call sites to
// reference internal/provider directly.
package agent

import "github.com/smallnest/pigo/internal/provider"

// --- provider.go ---

type (
	Model             = provider.Model
	CompletionRequest = provider.CompletionRequest
	Provider          = provider.Provider
)

const (
	StreamEventStart    = provider.StreamEventStart
	StreamEventText     = provider.StreamEventText
	StreamEventThinking = provider.StreamEventThinking
	StreamEventToolCall = provider.StreamEventToolCall
	StreamEventDone     = provider.StreamEventDone
	StreamEventError    = provider.StreamEventError
)

var DefaultCatalog = provider.DefaultCatalog

// --- provider_interface.go ---

type (
	AssistantMessageEvent       = provider.AssistantMessageEvent
	StreamStartEvent            = provider.StreamStartEvent
	StreamTextEvent             = provider.StreamTextEvent
	StreamThinkingEvent         = provider.StreamThinkingEvent
	StreamToolCallEvent         = provider.StreamToolCallEvent
	StreamDoneEvent             = provider.StreamDoneEvent
	StreamErrorEvent            = provider.StreamErrorEvent
	AssistantMessageEventStream = provider.AssistantMessageEventStream
	LlmContext                  = provider.LlmContext
	StreamConfig                = provider.StreamConfig
	StreamFn                    = provider.StreamFn
)

var (
	NewAssistantMessageEventStream = provider.NewAssistantMessageEventStream
	StreamFnFromProvider           = provider.StreamFnFromProvider
)

// --- transport.go ---

type (
	StreamEvent     = provider.StreamEvent
	Decoder         = provider.Decoder
	TransportConfig = provider.TransportConfig
)

var StreamRequest = provider.StreamRequest

// --- anthropic.go ---

type AnthropicDecoder = provider.AnthropicDecoder

var NewAnthropicDecoder = provider.NewAnthropicDecoder

// --- gemini.go ---

type GeminiDecoder = provider.GeminiDecoder

var NewGeminiDecoder = provider.NewGeminiDecoder

// --- openai.go ---

type OpenAIDecoder = provider.OpenAIDecoder

var NewOpenAIDecoder = provider.NewOpenAIDecoder

// --- providers.go ---

var (
	NewBedrockProvider    = provider.NewBedrockProvider
	NewOpenRouterProvider = provider.NewOpenRouterProvider
	NewOllamaProvider     = provider.NewOllamaProvider
)

// --- auth.go ---

type (
	APIKeyConfig    = provider.APIKeyConfig
	TokenSource     = provider.TokenSource
	OAuthToken      = provider.OAuthToken
	CredentialStore = provider.CredentialStore
)

var (
	LoadAPIKeyConfig     = provider.LoadAPIKeyConfig
	LoadAPIKeyConfigFile = provider.LoadAPIKeyConfigFile
	NewTokenSource       = provider.NewTokenSource
	NewCredentialStore   = provider.NewCredentialStore
)

// --- model_registry.go ---

type ModelRegistry = provider.ModelRegistry

var (
	NewModelRegistry             = provider.NewModelRegistry
	NewModelRegistryWithDefaults = provider.NewModelRegistryWithDefaults
)

// --- thinking.go ---

type (
	AnthropicThinking = provider.AnthropicThinking
	GoogleThinking    = provider.GoogleThinking
)

var (
	ResolveThinking            = provider.ResolveThinking
	TranslateAnthropicThinking = provider.TranslateAnthropicThinking
	TranslateGoogleThinking    = provider.TranslateGoogleThinking
)
