package provider

// Tests for the preset provider/model catalog (对标 pi agent's preset picker):
// LookupPreset resolves a catalog id to its owning provider, PresetsByProvider
// groups by provider, and every preset must name a provider that has a known
// credential env var (or be the local, keyless Ollama).

import "testing"

// TestLookupPresetResolvesProvider verifies a catalog id resolves to its
// declared provider, and an unknown id does not.
func TestLookupPresetResolvesProvider(t *testing.T) {
	p, ok := LookupPreset("meta/llama-3.3-70b-instruct")
	if !ok {
		t.Fatal("expected NVIDIA llama preset to be in the catalog")
	}
	if p.Provider != "nvidia" {
		t.Errorf("provider = %q, want nvidia", p.Provider)
	}
	if _, ok := LookupPreset("definitely/not-a-preset"); ok {
		t.Error("unknown id must not resolve to a preset")
	}
}

// TestPresetsByProviderGroups verifies each provider surfaces at least one
// preset and that the returned entries all belong to that provider.
func TestPresetsByProviderGroups(t *testing.T) {
	for _, name := range []string{"openrouter", "nvidia", "ollama"} {
		got := PresetsByProvider(name)
		if len(got) == 0 {
			t.Errorf("provider %q has no presets", name)
		}
		for _, m := range got {
			if m.Provider != name {
				t.Errorf("PresetsByProvider(%q) returned entry for %q", name, m.Provider)
			}
		}
	}
}

// TestPresetProvidersHaveCredentialMapping verifies every non-local preset
// provider has an API-key env var mapping in providerEnvVars, so a selected
// preset can actually resolve a credential. Ollama is local and keyless.
func TestPresetProvidersHaveCredentialMapping(t *testing.T) {
	for _, pv := range PresetProviders {
		if pv.Name == "ollama" {
			continue
		}
		if _, ok := providerEnvVars[pv.Name]; !ok {
			t.Errorf("preset provider %q has no credential env var mapping", pv.Name)
		}
	}
}

// TestPresetLabelFallsBackToID verifies Label uses DisplayName when set and
// falls back to the id otherwise.
func TestPresetLabelFallsBackToID(t *testing.T) {
	if got := (PresetModel{ID: "x", DisplayName: "X"}).Label(); got != "X" {
		t.Errorf("Label = %q, want X", got)
	}
	if got := (PresetModel{ID: "x"}).Label(); got != "x" {
		t.Errorf("Label = %q, want x", got)
	}
}
