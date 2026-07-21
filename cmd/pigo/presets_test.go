package main

// Tests for provider resolution and the /models preset listing. resolveProvider
// maps a model id to the right gateway (preset catalog first, then prefix rules,
// then OpenRouter default); presetListing renders the curated catalog for the
// REPL /models command.

import (
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
		_, name, err := resolveProvider(c.model, "", "", "")
		if err != nil {
			t.Errorf("resolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("resolveProvider(%q) = %q, want %q", c.model, name, c.wantName)
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
		_, name, err := resolveProvider(c.model, c.baseURL, "", "")
		if err != nil {
			t.Errorf("resolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("resolveProvider(%q, %q) = %q, want %q", c.model, c.baseURL, name, c.wantName)
		}
	}
}

// TestResolveProviderExplicitProtocol verifies an explicit --protocol wins over
// model-id heuristics: openai (with base-url) and anthropic select the matching
// wire driver, an empty base-url for openai errors, and an unknown protocol
// errors instead of silently falling back.
func TestResolveProviderExplicitProtocol(t *testing.T) {
	// openai protocol → "openai" provider name, requires base-url.
	if _, name, err := resolveProvider("any-model", "https://example.com/v1", "openai", ""); err != nil || name != "openai" {
		t.Errorf("protocol=openai = (%q, %v), want (openai, nil)", name, err)
	}
	if _, _, err := resolveProvider("any-model", "", "openai", ""); err == nil {
		t.Error("protocol=openai with no base-url should error")
	}
	// anthropic protocol → "anthropic" provider name, base-url optional (defaults).
	if _, name, err := resolveProvider("claude-x", "", "anthropic", ""); err != nil || name != "anthropic" {
		t.Errorf("protocol=anthropic = (%q, %v), want (anthropic, nil)", name, err)
	}
	// Unknown protocol errors rather than falling back to a heuristic.
	if _, _, err := resolveProvider("any-model", "", "grpc", ""); err == nil {
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
	if _, name, err := resolveProvider("deepseek-chat", "", "", "deepseek"); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek = (%q, %v), want (deepseek, nil)", name, err)
	}
	// Anthropic-protocol provider.
	if _, name, err := resolveProvider("MiniMax-M2", "", "", "minimax"); err != nil || name != "minimax" {
		t.Errorf("provider=minimax = (%q, %v), want (minimax, nil)", name, err)
	}
	// A matching --protocol is not a conflict (deepseek speaks openai).
	if _, name, err := resolveProvider("deepseek-chat", "", "openai", "deepseek"); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek + protocol=openai = (%q, %v), want (deepseek, nil)", name, err)
	}
	// --provider wins over model-id heuristics: an ollama/-prefixed id still
	// resolves to the named provider, not local Ollama.
	if _, name, err := resolveProvider("ollama/x", "", "", "deepseek"); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek with ollama/ model = (%q, %v), want (deepseek, nil)", name, err)
	}
	// --base-url overrides the spec default without changing the provider name.
	if _, name, err := resolveProvider("deepseek-chat", "https://proxy.local/v1", "", "deepseek"); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek + base-url = (%q, %v), want (deepseek, nil)", name, err)
	}
	// Conflict: minimax speaks anthropic; forcing --protocol openai errors and
	// names both flags.
	_, _, err := resolveProvider("MiniMax-M2", "", "openai", "minimax")
	if err == nil {
		t.Fatal("provider=minimax + protocol=openai should conflict")
	}
	if !strings.Contains(err.Error(), "--provider") || !strings.Contains(err.Error(), "--protocol") {
		t.Errorf("conflict error should name both flags, got: %v", err)
	}
	// Unknown provider errors and lists available names.
	_, _, err = resolveProvider("m", "", "", "no-such-provider")
	if err == nil {
		t.Fatal("unknown provider should error")
	}
	if !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("unknown-provider error should list available names, got: %v", err)
	}
}

// TestPresetListingGroupsAndFilters verifies /models lists all providers by
// default and filters to one provider when given an argument.
func TestPresetListingGroupsAndFilters(t *testing.T) {
	all := presetListing("")
	for _, want := range []string{"openrouter", "nvidia", "ollama"} {
		if !strings.Contains(all, want) {
			t.Errorf("full listing missing provider %q:\n%s", want, all)
		}
	}
	// Filter to nvidia only: openrouter must not appear.
	nv := presetListing("nvidia")
	if !strings.Contains(nv, "nvidia") {
		t.Errorf("filtered listing missing nvidia:\n%s", nv)
	}
	if strings.Contains(nv, "openrouter") {
		t.Errorf("nvidia filter must not include openrouter:\n%s", nv)
	}
	// Unknown filter yields a helpful message, not a crash.
	if got := presetListing("bogus"); !strings.Contains(got, "no preset provider") {
		t.Errorf("unknown filter = %q, want a not-found message", got)
	}
}
