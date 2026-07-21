package main

import (
	"testing"

	"github.com/smallnest/pigo/internal/provider"
)

// TestGenericBaseURLEnvVar verifies the <PROVIDER>_BASE_URL name derivation,
// especially the hyphen→underscore conversion and uppercasing.
func TestGenericBaseURLEnvVar(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"deepseek", "DEEPSEEK_BASE_URL"},
		{"zai-coding-cn", "ZAI_CODING_CN_BASE_URL"},
		{"vercel-ai-gateway", "VERCEL_AI_GATEWAY_BASE_URL"},
		{"", ""},
	}
	for _, c := range cases {
		if got := genericBaseURLEnvVar(c.name); got != c.want {
			t.Errorf("genericBaseURLEnvVar(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestResolveBaseURLPrecedence exercises all four precedence levels for a
// hyphenated provider (zai-coding-cn → ZAI_CODING_CN_BASE_URL), asserting that
// each higher-precedence source shadows every lower one.
func TestResolveBaseURLPrecedence(t *testing.T) {
	spec, ok := provider.LookupProviderSpec("zai-coding-cn")
	if !ok {
		t.Fatal("expected zai-coding-cn in registry")
	}
	// The default (lowest) applies when nothing else is set.
	if got := resolveBaseURL(spec, ""); got != spec.DefaultBaseURL {
		t.Errorf("default: got %q, want %q", got, spec.DefaultBaseURL)
	}

	// Generic <PROVIDER>_BASE_URL beats the default. Confirms hyphen→underscore.
	t.Setenv("ZAI_CODING_CN_BASE_URL", "https://generic.example/v4")
	if got := resolveBaseURL(spec, ""); got != "https://generic.example/v4" {
		t.Errorf("generic env: got %q, want %q", got, "https://generic.example/v4")
	}

	// --base-url flag beats the generic env var.
	if got := resolveBaseURL(spec, "https://flag.example/v4"); got != "https://flag.example/v4" {
		t.Errorf("flag over generic: got %q, want %q", got, "https://flag.example/v4")
	}
}

// TestResolveBaseURLProviderSpecificEnv covers a provider that declares a
// provider-specific base-url env var (azure) and asserts it sits between the
// flag and the generic convention in precedence.
func TestResolveBaseURLProviderSpecificEnv(t *testing.T) {
	spec, ok := provider.LookupProviderSpec("azure-openai-responses")
	if !ok {
		t.Fatal("expected azure-openai-responses in registry")
	}
	if len(spec.BaseURLEnvVars) == 0 {
		t.Fatal("expected azure-openai-responses to declare BaseURLEnvVars")
	}

	// Provider-specific env var beats the default (which is empty here).
	t.Setenv("AZURE_OPENAI_BASE_URL", "https://specific.example")
	if got := resolveBaseURL(spec, ""); got != "https://specific.example" {
		t.Errorf("provider-specific env: got %q, want %q", got, "https://specific.example")
	}

	// Provider-specific env var beats the generic <PROVIDER>_BASE_URL.
	t.Setenv("AZURE_OPENAI_RESPONSES_BASE_URL", "https://generic.example")
	if got := resolveBaseURL(spec, ""); got != "https://specific.example" {
		t.Errorf("provider-specific beats generic: got %q, want %q", got, "https://specific.example")
	}

	// --base-url flag beats the provider-specific env var.
	if got := resolveBaseURL(spec, "https://flag.example"); got != "https://flag.example" {
		t.Errorf("flag beats provider-specific: got %q, want %q", got, "https://flag.example")
	}
}
