// This file implements model-name → provider inference (US: auto-infer provider
// from --model alone). When a user supplies only --model, with no --provider,
// --protocol, or --base-url, pigo tries to guess the owning provider from the
// model id's well-known name prefix (e.g. "claude-*" → anthropic, "deepseek-*"
// → deepseek). This lets `pigo -m claude-opus-4-8` reach the Anthropic API
// without the user spelling out the provider or its wire protocol.
//
// The mapping is deliberately conservative: only names that unambiguously
// identify a single built-in provider are inferred. Ambiguous families served
// by many gateways (notably "llama-*", "qwq-*", "gemma-*", "mixtral-*") are NOT
// inferred — they return ok=false so the caller falls through to its existing
// default (OpenRouter), which is the safe, unchanged behavior.
//
// Every provider name returned here is guaranteed to exist in providerRegistry
// (enforced by a test), so callers can hand the result straight to the
// registry-driven resolution path.
package provider

import "strings"

// modelPrefixProvider maps a lowercase model-name prefix to the built-in
// provider name that serves that family. Order matters: the table is scanned
// top-to-bottom and the FIRST matching prefix wins, so list more specific
// prefixes before shorter ones that would also match.
//
// Each provider name here must be present in providerRegistry (registry.go).
var modelPrefixProvider = []struct {
	prefix   string
	provider string
}{
	{"claude-", "anthropic"},
	{"gpt-", "openai"},
	{"o1-", "openai"},
	{"o3-", "openai"},
	{"o4-", "openai"},
	{"gemini-", "google"},
	{"deepseek-", "deepseek"},
	{"glm-", "zai"},
	{"kimi-", "moonshotai"},
	{"moonshot-", "moonshotai"},
	{"qwen-", "dashscope"},
	{"ernie-", "qianfan"},
	{"doubao-", "volcengine"},
	{"grok-", "xai"},
	{"mistral-", "mistral"},
	{"codestral-", "mistral"},
	{"devstral-", "mistral"},
	{"hunyuan-", "hunyuan"},
	{"minimax-", "minimax"},
	{"mimo-", "xiaomi"},
}

// InferProviderFromModel guesses the built-in provider name that serves a given
// model id, based on the id's well-known name prefix. It returns the provider
// name and ok=true on a confident match, or ("", false) when the id is unknown
// or ambiguous (served by multiple gateways). Matching is case-insensitive.
//
// The returned name is always a valid entry in providerRegistry. Callers should
// use it only when no explicit --provider/--protocol/--base-url was given; those
// flags take precedence over any inference.
func InferProviderFromModel(model string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(model))
	if id == "" {
		return "", false
	}
	// A "provider/model" style id (e.g. "openai/gpt-4o") is an OpenRouter-style
	// routed id, not a bare model name — leave those to the caller's preset/
	// prefix handling rather than inferring from the leading segment.
	if strings.Contains(id, "/") {
		return "", false
	}
	for _, m := range modelPrefixProvider {
		if strings.HasPrefix(id, m.prefix) {
			return m.provider, true
		}
	}
	return "", false
}
