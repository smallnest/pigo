package provider

import "testing"

// TestPresetProvidersIncludeNewProviders verifies the curated preset provider
// list gained the expanded set of gateways, each paired with the correct API-key
// environment variable (referenced by name only).
func TestPresetProvidersIncludeNewProviders(t *testing.T) {
	byName := make(map[string]string, len(PresetProviders))
	for _, p := range PresetProviders {
		byName[p.Name] = p.EnvVar
	}

	want := map[string]string{
		"deepseek":   "DEEPSEEK_API_KEY",
		"groq":       "GROQ_API_KEY",
		"xai":        "XAI_API_KEY",
		"cerebras":   "CEREBRAS_API_KEY",
		"mistral":    "MISTRAL_API_KEY",
		"moonshotai": "MOONSHOT_API_KEY",
		"zai":        "ZAI_API_KEY",
		"fireworks":  "FIREWORKS_API_KEY",
		"together":   "TOGETHER_API_KEY",
		"minimax":    "MINIMAX_API_KEY",
		"xiaomi":     "XIAOMI_API_KEY",
	}
	for name, env := range want {
		got, ok := byName[name]
		if !ok {
			t.Errorf("PresetProviders missing provider %q", name)
			continue
		}
		if got != env {
			t.Errorf("provider %q env var = %q, want %q", name, got, env)
		}
	}

	// Every preset provider must be a known provider in the central registry, so
	// selecting a preset can always be resolved to a working Provider.
	for _, p := range PresetProviders {
		if p.Name == "ollama" {
			continue // local pseudo-provider, not in the registry
		}
		if _, ok := LookupProviderSpec(p.Name); !ok {
			t.Errorf("preset provider %q has no ProviderSpec in the registry", p.Name)
		}
	}
}

// TestPresetCatalogCountsPerProvider asserts each expanded provider contributes
// the expected number of curated entries.
func TestPresetCatalogCountsPerProvider(t *testing.T) {
	wantAtLeast := map[string]int{
		"deepseek":   2,
		"groq":       3,
		"xai":        2,
		"cerebras":   3,
		"mistral":    4,
		"moonshotai": 3,
		"zai":        3,
		"fireworks":  3,
		"together":   3,
		"minimax":    2,
		"xiaomi":     3,
	}
	for provider, min := range wantAtLeast {
		got := PresetsByProvider(provider)
		if len(got) < min {
			t.Errorf("PresetsByProvider(%q) returned %d entries, want >= %d", provider, len(got), min)
		}
		for _, p := range got {
			if p.Provider != provider {
				t.Errorf("PresetsByProvider(%q) returned entry for %q", provider, p.Provider)
			}
			if p.ID == "" {
				t.Errorf("PresetsByProvider(%q) returned entry with empty ID", provider)
			}
		}
	}
}

// TestLookupPresetNewEntries verifies representative new model ids resolve to the
// correct owning provider and carry a non-empty label.
func TestLookupPresetNewEntries(t *testing.T) {
	cases := []struct {
		id       string
		provider string
	}{
		{"deepseek-v4-pro", "deepseek"},
		{"llama-3.3-70b-versatile", "groq"},
		{"grok-4.5", "xai"},
		{"zai-glm-4.7", "cerebras"},
		{"mistral-large-latest", "mistral"},
		{"kimi-k2-thinking", "moonshotai"},
		{"glm-5.1", "zai"},
		{"accounts/fireworks/models/gpt-oss-120b", "fireworks"},
		{"deepseek-ai/DeepSeek-V4-Pro", "together"},
		{"MiniMax-M3", "minimax"},
		{"mimo-v2.5-pro", "xiaomi"},
	}
	for _, tc := range cases {
		p, ok := LookupPreset(tc.id)
		if !ok {
			t.Errorf("LookupPreset(%q) not found", tc.id)
			continue
		}
		if p.Provider != tc.provider {
			t.Errorf("LookupPreset(%q).Provider = %q, want %q", tc.id, p.Provider, tc.provider)
		}
		if p.Label() == "" {
			t.Errorf("LookupPreset(%q).Label() is empty", tc.id)
		}
	}

	if _, ok := LookupPreset("this-model-does-not-exist"); ok {
		t.Error("LookupPreset returned ok for an unknown id")
	}
}
