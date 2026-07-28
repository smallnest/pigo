package provider

// Tests for provider resolution moved from cmd/pigo (US-004, #361): ResolveProvider
// maps a model id to the right gateway (preset catalog first, then prefix rules,
// then OpenRouter default), and ResolveBaseURL applies the base-url override
// precedence. Environment lookups are injected via os.Getenv here.

import (
	"os"
	"strings"
	"testing"
)

// TestResolveProviderPresetCatalog verifies a preset id resolves to its declared
// provider (NVIDIA and Ollama presets do not fall through to OpenRouter).
func TestResolveProviderPresetCatalog(t *testing.T) {
	cases := []struct {
		model    string
		wantName string
	}{
		{"meta/llama-3.3-70b-instruct", "nvidia"},     // NVIDIA preset
		{"ollama/llama3.3", "ollama"},                 // Ollama preset
		{"openai/gpt-4o", "openrouter"},               // OpenRouter preset
		{"anthropic/claude-3.5-sonnet", "openrouter"}, // OpenRouter preset
	}
	for _, c := range cases {
		_, name, err := ResolveProvider(c.model, "", "", "", os.Getenv)
		if err != nil {
			t.Errorf("ResolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("ResolveProvider(%q) = %q, want %q", c.model, name, c.wantName)
		}
	}
}

// TestResolveProviderPrefixAndDefault verifies the prefix rules and the
// OpenRouter default for ids not in the catalog.
func TestResolveProviderPrefixAndDefault(t *testing.T) {
	cases := []struct {
		model    string
		baseURL  string
		wantName string
	}{
		{"ollama/some-local-model", "", "ollama"}, // ollama/ prefix
		{"nvidia/some-nim-model", "", "nvidia"},   // nvidia/ prefix
		{"some-unknown-model", "", "openrouter"},  // default
		{"m", "http://host:11434/v1", "ollama"},   // ollama port
	}
	for _, c := range cases {
		_, name, err := ResolveProvider(c.model, c.baseURL, "", "", os.Getenv)
		if err != nil {
			t.Errorf("ResolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("ResolveProvider(%q, %q) = %q, want %q", c.model, c.baseURL, name, c.wantName)
		}
	}
}

// TestResolveProviderExplicitProtocol verifies an explicit --protocol wins over
// model-id heuristics: openai (with base-url) and anthropic select the matching
// wire driver, an empty base-url for openai errors, and an unknown protocol
// errors instead of silently falling back.
func TestResolveProviderExplicitProtocol(t *testing.T) {
	// openai protocol → "openai" provider name, requires base-url.
	if _, name, err := ResolveProvider("any-model", "https://example.com/v1", "openai", "", os.Getenv); err != nil || name != "openai" {
		t.Errorf("protocol=openai = (%q, %v), want (openai, nil)", name, err)
	}
	if _, _, err := ResolveProvider("any-model", "", "openai", "", os.Getenv); err == nil {
		t.Error("protocol=openai with no base-url should error")
	}
	// anthropic protocol → "anthropic" provider name, base-url optional (defaults).
	if _, name, err := ResolveProvider("claude-x", "", "anthropic", "", os.Getenv); err != nil || name != "anthropic" {
		t.Errorf("protocol=anthropic = (%q, %v), want (anthropic, nil)", name, err)
	}
	// Unknown protocol errors rather than falling back to a heuristic.
	if _, _, err := ResolveProvider("any-model", "", "grpc", "", os.Getenv); err == nil {
		t.Error("unknown protocol should error")
	}
}

// TestResolveProviderExplicitProvider verifies that --provider selects a
// built-in provider from the registry: the returned provider-name is the spec
// name (so key resolution reads the right env var), an OpenAI-protocol provider
// (deepseek) and an Anthropic-protocol provider (minimax) both resolve, an
// incompatible --protocol is a conflict error naming both flags, and an unknown
// provider name errors while listing the available names.
func TestResolveProviderExplicitProvider(t *testing.T) {
	// OpenAI-protocol provider: returns its own name for key lookup.
	if _, name, err := ResolveProvider("deepseek-chat", "", "", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek = (%q, %v), want (deepseek, nil)", name, err)
	}
	// Anthropic-protocol provider.
	if _, name, err := ResolveProvider("MiniMax-M2", "", "", "minimax", os.Getenv); err != nil || name != "minimax" {
		t.Errorf("provider=minimax = (%q, %v), want (minimax, nil)", name, err)
	}
	// A matching --protocol is not a conflict (deepseek speaks openai).
	if _, name, err := ResolveProvider("deepseek-chat", "", "openai", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek + protocol=openai = (%q, %v), want (deepseek, nil)", name, err)
	}
	// --provider wins over model-id heuristics: an ollama/-prefixed id still
	// resolves to the named provider, not local Ollama.
	if _, name, err := ResolveProvider("ollama/x", "", "", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek with ollama/ model = (%q, %v), want (deepseek, nil)", name, err)
	}
	// --base-url overrides the spec default without changing the provider name.
	if _, name, err := ResolveProvider("deepseek-chat", "https://proxy.local/v1", "", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek + base-url = (%q, %v), want (deepseek, nil)", name, err)
	}
	// Conflict: minimax speaks anthropic; forcing --protocol openai errors and
	// names both flags.
	_, _, err := ResolveProvider("MiniMax-M2", "", "openai", "minimax", os.Getenv)
	if err == nil {
		t.Fatal("provider=minimax + protocol=openai should conflict")
	}
	if !strings.Contains(err.Error(), "--provider") || !strings.Contains(err.Error(), "--protocol") {
		t.Errorf("conflict error should name both flags, got: %v", err)
	}
	// Unknown provider errors and lists available names.
	_, _, err = ResolveProvider("m", "", "", "no-such-provider", os.Getenv)
	if err == nil {
		t.Fatal("unknown provider should error")
	}
	if !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("unknown-provider error should list available names, got: %v", err)
	}
}

// TestResolveProviderCNPresets verifies the Chinese-cloud preset ids route to
// their own provider (not the OpenRouter default) via the LookupPreset branch.
func TestResolveProviderCNPresets(t *testing.T) {
	cases := []struct {
		model    string
		wantName string
	}{
		{"ernie-4.5-turbo-32k", "qianfan"},
		{"doubao-seed-1-6", "volcengine"},
		{"qwen-max", "dashscope"},
		{"hunyuan-turbos-latest", "hunyuan"},
	}
	for _, c := range cases {
		_, name, err := ResolveProvider(c.model, "", "", "", os.Getenv)
		if err != nil {
			t.Errorf("ResolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("ResolveProvider(%q) = %q, want %q", c.model, name, c.wantName)
		}
	}
}

// TestResolveProviderCNExplicit verifies --provider selects the CN providers
// directly and that --base-url overrides without changing the provider name.
func TestResolveProviderCNExplicit(t *testing.T) {
	for _, name := range []string{"qianfan", "volcengine", "dashscope", "hunyuan"} {
		if _, got, err := ResolveProvider("some-model", "", "", name, os.Getenv); err != nil || got != name {
			t.Errorf("provider=%s = (%q, %v), want (%s, nil)", name, got, err, name)
		}
		if _, got, err := ResolveProvider("some-model", "https://proxy.local/v1", "", name, os.Getenv); err != nil || got != name {
			t.Errorf("provider=%s + base-url = (%q, %v), want (%s, nil)", name, got, err, name)
		}
	}
}

// TestResolveProviderModelNameInference verifies model-name inference (Issue
// #235): with only --model given, a bare model name whose prefix identifies a
// single provider resolves to that provider — NOT the OpenRouter default.
func TestResolveProviderModelNameInference(t *testing.T) {
	cases := []struct {
		model    string
		wantName string
	}{
		{"claude-opus-4-8", "anthropic"},
		{"deepseek-chat", "deepseek"},
		{"gpt-4.1", "openai"},
		{"gemini-3-pro", "google"},
		{"grok-5", "xai"},
	}
	for _, c := range cases {
		if _, name, err := ResolveProvider(c.model, "", "", "", os.Getenv); err != nil || name != c.wantName {
			t.Errorf("ResolveProvider(%q) = (%q, %v), want (%q, nil)", c.model, name, err, c.wantName)
		}
	}
}

// TestResolveProviderInferencePrecedence verifies that model-name inference does
// not override explicit flags and does not fire when a --base-url is given, and
// that unknown/ambiguous names still fall back to OpenRouter.
func TestResolveProviderInferencePrecedence(t *testing.T) {
	// Explicit --provider wins over an inferable model name.
	if _, name, err := ResolveProvider("claude-opus-4-8", "", "", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek overrides inference = (%q, %v), want (deepseek, nil)", name, err)
	}
	// Explicit --protocol wins over an inferable model name.
	if _, name, err := ResolveProvider("claude-opus-4-8", "https://example.com/v1", "openai", "", os.Getenv); err != nil || name != "openai" {
		t.Errorf("protocol=openai overrides inference = (%q, %v), want (openai, nil)", name, err)
	}
	// A --base-url signals a custom endpoint: inference is skipped, default applies.
	if _, name, err := ResolveProvider("claude-opus-4-8", "https://gw.local/v1", "", "", os.Getenv); err != nil || name != "openrouter" {
		t.Errorf("inference skipped with base-url = (%q, %v), want (openrouter, nil)", name, err)
	}
	// Ambiguous/unknown names still default to OpenRouter.
	for _, m := range []string{"llama-3.3-70b", "totally-unknown-model"} {
		if _, name, err := ResolveProvider(m, "", "", "", os.Getenv); err != nil || name != "openrouter" {
			t.Errorf("ResolveProvider(%q) = (%q, %v), want (openrouter, nil)", m, name, err)
		}
	}
}

// TestResolveBaseURLPrecedence exercises all four precedence levels for a
// hyphenated provider (zai-coding-cn → ZAI_CODING_CN_BASE_URL).
func TestResolveBaseURLPrecedence(t *testing.T) {
	spec, ok := LookupProviderSpec("zai-coding-cn")
	if !ok {
		t.Fatal("expected zai-coding-cn in registry")
	}
	if got := ResolveBaseURL(spec, "", os.Getenv); got != spec.DefaultBaseURL {
		t.Errorf("default: got %q, want %q", got, spec.DefaultBaseURL)
	}
	t.Setenv("ZAI_CODING_CN_BASE_URL", "https://generic.example/v4")
	if got := ResolveBaseURL(spec, "", os.Getenv); got != "https://generic.example/v4" {
		t.Errorf("generic env: got %q, want %q", got, "https://generic.example/v4")
	}
	if got := ResolveBaseURL(spec, "https://flag.example/v4", os.Getenv); got != "https://flag.example/v4" {
		t.Errorf("flag over generic: got %q, want %q", got, "https://flag.example/v4")
	}
}

// TestResolveBaseURLProviderSpecificEnv covers a provider that declares a
// provider-specific base-url env var (azure), asserting it sits between the flag
// and the generic convention in precedence.
func TestResolveBaseURLProviderSpecificEnv(t *testing.T) {
	spec, ok := LookupProviderSpec("azure-openai-responses")
	if !ok {
		t.Fatal("expected azure-openai-responses in registry")
	}
	if len(spec.BaseURLEnvVars) == 0 {
		t.Fatal("expected azure-openai-responses to declare BaseURLEnvVars")
	}
	t.Setenv("AZURE_OPENAI_BASE_URL", "https://specific.example")
	if got := ResolveBaseURL(spec, "", os.Getenv); got != "https://specific.example" {
		t.Errorf("provider-specific env: got %q, want %q", got, "https://specific.example")
	}
	t.Setenv("AZURE_OPENAI_RESPONSES_BASE_URL", "https://generic.example")
	if got := ResolveBaseURL(spec, "", os.Getenv); got != "https://specific.example" {
		t.Errorf("provider-specific beats generic: got %q, want %q", got, "https://specific.example")
	}
	if got := ResolveBaseURL(spec, "https://flag.example", os.Getenv); got != "https://flag.example" {
		t.Errorf("flag beats provider-specific: got %q, want %q", got, "https://flag.example")
	}
}
