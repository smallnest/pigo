package main

// Tests for provider resolution and the /models preset listing. resolveProvider
// maps a model id to the right gateway (preset catalog first, then prefix rules,
// then OpenRouter default); presetListing renders the curated catalog for the
// TUI /models command.

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
		_, name, err := resolveProvider(c.model, "")
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
		_, name, err := resolveProvider(c.model, c.baseURL)
		if err != nil {
			t.Errorf("resolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("resolveProvider(%q, %q) = %q, want %q", c.model, c.baseURL, name, c.wantName)
		}
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
