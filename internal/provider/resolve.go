package provider

// Provider resolution moved here from cmd/pigo (US-004, #361): mapping a model
// id / --provider / --protocol selection to a concrete wire driver, plus the
// base-url override precedence. Environment lookups are injected as an
// env func(string) string so callers (and tests) control the environment
// instead of reaching into the process env directly.

import (
	"fmt"
	"strings"

	"github.com/smallnest/pigo/internal/cli/config"
)

// ResolveProvider maps a model id to a built-in provider. An explicit
// --provider name wins over every other rule: it selects a built-in provider
// from the registry and constructs the matching wire driver (see
// ResolveNamedProvider). When provider is empty, protocol and model-id
// heuristics apply as before.
//
// When protocol is a non-empty explicit selection ("openai" or "anthropic") it
// wins over the model-id heuristics: the provider is built directly for that
// wire format against baseURL, which is how a user points pigo at a self-hosted
// or third-party endpoint and says which protocol it speaks. An "anthropic"
// selection with no baseURL targets the public Anthropic API.
//
// When protocol is empty, resolution falls back to model-id heuristics:
//
//  1. If the id is in the preset catalog, use its declared provider (this is how
//     OpenRouter/NVIDIA/Ollama presets pick the right gateway).
//  2. An "ollama/" prefix (or a base URL on the Ollama port) → local Ollama.
//  3. An "nvidia/" prefix → NVIDIA NIM (strips the prefix for the wire id).
//  4. Model-name inference: with no --base-url, a well-known model-name prefix
//     (e.g. "claude-*", "deepseek-*") selects its first-party built-in provider
//     via ResolveNamedProvider (see InferProviderFromModel).
//  5. Everything else → OpenRouter, the reference OpenAI-compatible gateway.
//
// An unknown protocol value is an error, surfaced to the caller for exit-code
// mapping rather than silently falling back.
func ResolveProvider(model, baseURL, protocol, providerName string, env func(string) string) (Provider, string, error) {
	// Explicit --provider selects a built-in provider from the registry and
	// wins over both --protocol inference and model-id heuristics.
	if strings.TrimSpace(providerName) != "" {
		return ResolveNamedProvider(providerName, model, baseURL, protocol, env)
	}

	// 0. Explicit protocol selection wins over every heuristic. Normalize the
	// surface value first so "openai" and "openai/chat" collapse to the same
	// Chat Completions selector and "openai/resp_api" routes to the Responses
	// driver; an unknown value surfaces as an error for exit-code mapping.
	canonical, err := NormalizeProtocol(protocol)
	if err != nil {
		return nil, "", err
	}
	switch canonical {
	case ProtocolOpenAI:
		if strings.TrimSpace(baseURL) == "" {
			return nil, "", fmt.Errorf("--protocol openai requires --base-url")
		}
		return NewOpenAICompatibleProvider(baseURL, []Model{{Provider: "openai", ID: model, SupportsImages: true}}), "openai", nil
	case ProtocolOpenAIResponses:
		// The Responses driver has no public default endpoint here: unlike the
		// anthropic path (which targets the public API), resp_api mirrors the
		// Chat Completions requirement and demands an explicit --base-url.
		if strings.TrimSpace(baseURL) == "" {
			return nil, "", fmt.Errorf("--protocol openai/resp_api requires --base-url")
		}
		return NewOpenAIResponsesProvider("openai", baseURL, []Model{{Provider: "openai", ID: model, SupportsImages: true}}), "openai", nil
	case ProtocolAnthropic:
		return NewAnthropicProvider(baseURL, []Model{{Provider: "anthropic", ID: model, SupportsImages: true}}), "anthropic", nil
	case "":
		// fall through to heuristic resolution
	}

	// 1. Preset catalog wins: a curated id knows its own provider.
	if p, ok := LookupPreset(model); ok {
		switch p.Provider {
		case "nvidia":
			return NewNvidiaProvider(baseURL, []Model{{Provider: "nvidia", ID: model, SupportsImages: true}}), "nvidia", nil
		case "ollama":
			id := strings.TrimPrefix(model, "ollama/")
			return NewOllamaProvider(baseURL, []Model{{Provider: "ollama", ID: id, SupportsImages: true}}), "ollama", nil
		case "", "openrouter":
			return NewOpenRouterProvider(baseURL, []Model{{Provider: "openrouter", ID: model, SupportsImages: true}}), "openrouter", nil
		default:
			// Any other preset provider is a named built-in (e.g. deepseek,
			// qianfan, dashscope): build it from the registry so the correct
			// base URL, protocol, and API-key env var are used — not OpenRouter's.
			return ResolveNamedProvider(p.Provider, model, baseURL, protocol, env)
		}
	}

	// 2. Local Ollama by prefix or port.
	if strings.HasPrefix(model, "ollama/") || strings.Contains(baseURL, "11434") {
		id := strings.TrimPrefix(model, "ollama/")
		return NewOllamaProvider(baseURL, []Model{{Provider: "ollama", ID: id, SupportsImages: true}}), "ollama", nil
	}
	// 3. NVIDIA NIM by prefix.
	if strings.HasPrefix(model, "nvidia/") {
		id := strings.TrimPrefix(model, "nvidia/")
		return NewNvidiaProvider(baseURL, []Model{{Provider: "nvidia", ID: id, SupportsImages: true}}), "nvidia", nil
	}
	// 4. Model-name inference: with no --provider/--protocol (both empty here) and
	//    no --base-url, guess the provider from the model name's well-known prefix
	//    (e.g. "claude-*" → anthropic, "deepseek-*" → deepseek). A confident hit is
	//    routed through ResolveNamedProvider so the provider's registry protocol,
	//    default base URL, and API-key env var are used. A --base-url is treated as
	//    a custom-endpoint signal that should not be second-guessed, so inference is
	//    skipped when one is given. Ambiguous/unknown names fall through to (5).
	if strings.TrimSpace(baseURL) == "" {
		if name, ok := InferProviderFromModel(model); ok {
			return ResolveNamedProvider(name, model, baseURL, protocol, env)
		}
	}
	// 5. Default: OpenRouter.
	return NewOpenRouterProvider(baseURL, []Model{{Provider: "openrouter", ID: model, SupportsImages: true}}), "openrouter", nil
}

// ResolveNamedProvider builds the driver for an explicit --provider selection.
// It looks the name up in the built-in registry and constructs the wire driver
// matching the spec's Protocol: "openai" → an OpenAI-compatible (Bearer) driver,
// "anthropic" → an Anthropic-Messages driver. The base URL follows the override
// precedence in ResolveBaseURL (--base-url > provider-specific env > generic
// <PROVIDER>_BASE_URL > spec default). The returned provider-name string is the
// spec name, so downstream API-key resolution reads the provider's own env var
// (spec.EnvVars).
//
// Special providers with bespoke auth (azure/bedrock/vertex/cloudflare —
// AuthScheme aws/azure/special, or the cloudflare-* names) are routed to
// ResolveSpecialProvider, which validates their required env vars and composes
// the concrete endpoint (node #188).
func ResolveNamedProvider(name, model, baseURL, protocol string, env func(string) string) (Provider, string, error) {
	spec, ok := LookupProviderSpec(name)
	if !ok {
		return nil, "", fmt.Errorf("unknown --provider %q (available: %s)", name, strings.Join(ProviderNames(), ", "))
	}
	// A concurrently-set --protocol must agree with the provider's own protocol;
	// an incompatible pair is a user error naming both flags.
	if p := strings.TrimSpace(protocol); p != "" && p != spec.Protocol {
		return nil, "", fmt.Errorf("--provider %q speaks the %q protocol, which conflicts with --protocol %q; drop --protocol or set it to %q", name, spec.Protocol, p, spec.Protocol)
	}
	// Special-auth providers (Azure / Bedrock / Vertex / Cloudflare) compose
	// their endpoint from several env vars and/or need non-standard credential
	// validation, so route them to the dedicated resolver (US-007 / node #188).
	// It performs its own base-URL composition (honoring the --base-url override)
	// and returns a clear error naming any absent required env var.
	if IsSpecialAuthProvider(spec) {
		p, err := ResolveSpecialProvider(spec, model, baseURL, env)
		if err != nil {
			return nil, "", err
		}
		return p, spec.Name, nil
	}
	// Base-URL precedence (US-004 / FR-8, FR-9): --base-url flag > provider-
	// specific base-url env var(s) > generic <PROVIDER>_BASE_URL > spec default.
	url := ResolveBaseURL(spec, baseURL, env)
	models := []Model{{Provider: spec.Name, ID: model, SupportsImages: true}}
	// Note: spec.ExtraHeaders would be attached here, but the exported generic
	// constructors do not yet accept custom headers; all built-in specs currently
	// carry no ExtraHeaders, so this is a no-op today (refined alongside #188).
	switch spec.Protocol {
	case ProtocolAnthropic:
		// Auth header follows the spec's AuthScheme (x-api-key + anthropic-version
		// for anthropic/minimax/minimax-cn; Bearer for any anthropic-protocol
		// gateway that authenticates with a plain bearer token). The driver name is
		// the spec name so errors reference the selected provider.
		return NewAnthropicProtocolProvider(spec.Name, url, spec.AuthScheme, models), spec.Name, nil
	case ProtocolOpenAI:
		return NewOpenAICompatibleProvider(url, models), spec.Name, nil
	default:
		// The registry only ever stores openai/anthropic; guard anyway so an
		// unexpected value is a clear error rather than a nil provider.
		return nil, "", fmt.Errorf("--provider %q has unsupported protocol %q", name, spec.Protocol)
	}
}

// ResolveBaseURL determines the effective base URL for a selected provider,
// applying the base_url override precedence (US-004 / FR-8, FR-9). The first
// non-empty source wins, in this order:
//
//  1. flagBaseURL — the explicit --base-url/-u flag (highest).
//  2. provider-specific base-url env var(s) from spec.BaseURLEnvVars, in the
//     order the registry declares them (e.g. AZURE_OPENAI_BASE_URL).
//  3. the generic <PROVIDER>_BASE_URL env var, where <PROVIDER> is the provider
//     name uppercased with '-' rewritten to '_' (e.g. zai-coding-cn →
//     ZAI_CODING_CN_BASE_URL).
//  4. spec.DefaultBaseURL — the registry default (lowest).
//
// Values are trimmed of surrounding whitespace before the non-empty check, so a
// whitespace-only env var does not shadow a lower-precedence source. Environment
// lookups go through the injected env func so callers control the environment.
func ResolveBaseURL(spec ProviderSpec, flagBaseURL string, env func(string) string) string {
	// 1. Explicit flag wins over every env-var convention.
	if v := strings.TrimSpace(flagBaseURL); v != "" {
		return v
	}
	// 2. Provider-specific override env vars, in registry precedence order.
	for _, name := range spec.BaseURLEnvVars {
		if v := strings.TrimSpace(env(name)); v != "" {
			return v
		}
	}
	// 3. Generic <PROVIDER>_BASE_URL convention.
	if envName := config.GenericBaseURLEnvVar(spec.Name); envName != "" {
		if v := strings.TrimSpace(env(envName)); v != "" {
			return v
		}
	}
	// 4. Registry default.
	return spec.DefaultBaseURL
}
