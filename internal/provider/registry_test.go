package provider

import (
	"sort"
	"testing"
)

func TestLookupProviderSpec_Hit(t *testing.T) {
	spec, ok := LookupProviderSpec("deepseek")
	if !ok {
		t.Fatalf("LookupProviderSpec(deepseek): expected hit, got miss")
	}
	if spec.Name != "deepseek" {
		t.Errorf("Name = %q, want deepseek", spec.Name)
	}
	if spec.DefaultBaseURL != "https://api.deepseek.com" {
		t.Errorf("DefaultBaseURL = %q, want https://api.deepseek.com", spec.DefaultBaseURL)
	}
	if spec.Protocol != ProtocolOpenAI {
		t.Errorf("Protocol = %q, want %q", spec.Protocol, ProtocolOpenAI)
	}
	if len(spec.EnvVars) != 1 || spec.EnvVars[0] != "DEEPSEEK_API_KEY" {
		t.Errorf("EnvVars = %v, want [DEEPSEEK_API_KEY]", spec.EnvVars)
	}
}

func TestLookupProviderSpec_Miss(t *testing.T) {
	if _, ok := LookupProviderSpec("does-not-exist"); ok {
		t.Errorf("LookupProviderSpec(does-not-exist): expected miss, got hit")
	}
}

func TestAnthropicEnvVarOrder(t *testing.T) {
	spec, ok := LookupProviderSpec("anthropic")
	if !ok {
		t.Fatal("LookupProviderSpec(anthropic): expected hit")
	}
	want := []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}
	if len(spec.EnvVars) != len(want) {
		t.Fatalf("EnvVars = %v, want %v", spec.EnvVars, want)
	}
	for i := range want {
		if spec.EnvVars[i] != want[i] {
			t.Errorf("EnvVars[%d] = %q, want %q (OAuth must be first)", i, spec.EnvVars[i], want[i])
		}
	}
	if spec.Protocol != ProtocolAnthropic {
		t.Errorf("Protocol = %q, want %q", spec.Protocol, ProtocolAnthropic)
	}
}

func TestHuggingfaceEnvVar(t *testing.T) {
	spec, ok := LookupProviderSpec("huggingface")
	if !ok {
		t.Fatal("LookupProviderSpec(huggingface): expected hit")
	}
	if len(spec.EnvVars) != 1 || spec.EnvVars[0] != "HF_TOKEN" {
		t.Errorf("EnvVars = %v, want [HF_TOKEN]", spec.EnvVars)
	}
}

func TestAzureBaseURLEnvVars(t *testing.T) {
	spec, ok := LookupProviderSpec("azure-openai-responses")
	if !ok {
		t.Fatal("LookupProviderSpec(azure-openai-responses): expected hit")
	}
	found := false
	for _, v := range spec.BaseURLEnvVars {
		if v == "AZURE_OPENAI_BASE_URL" {
			found = true
		}
	}
	if !found {
		t.Errorf("BaseURLEnvVars = %v, want to contain AZURE_OPENAI_BASE_URL", spec.BaseURLEnvVars)
	}
}

func TestRegistryContainsAllExpectedProviders(t *testing.T) {
	expected := []string{
		"anthropic", "openai", "ant-ling", "deepseek", "nvidia", "google",
		"groq", "cerebras", "xai", "openrouter", "vercel-ai-gateway", "zai",
		"zai-coding-cn", "mistral", "minimax", "minimax-cn", "moonshotai",
		"moonshotai-cn", "huggingface", "fireworks", "together", "opencode",
		"opencode-go", "kimi-coding", "xiaomi", "xiaomi-token-plan-cn",
		"xiaomi-token-plan-ams", "xiaomi-token-plan-sgp",
		"azure-openai-responses", "amazon-bedrock", "google-vertex",
		"cloudflare-workers-ai", "cloudflare-ai-gateway",
	}
	for _, name := range expected {
		if _, ok := LookupProviderSpec(name); !ok {
			t.Errorf("registry missing expected provider %q", name)
		}
	}
	names := ProviderNames()
	if len(names) != len(expected) {
		t.Errorf("registry has %d providers, want %d", len(names), len(expected))
	}

	// Every spec must have a name, at least one env var, and a valid protocol.
	for _, spec := range ProviderSpecs() {
		if spec.Name == "" {
			t.Error("found spec with empty Name")
		}
		if len(spec.EnvVars) == 0 {
			t.Errorf("provider %q has no EnvVars", spec.Name)
		}
		if spec.Protocol != ProtocolOpenAI && spec.Protocol != ProtocolAnthropic {
			t.Errorf("provider %q has invalid Protocol %q", spec.Name, spec.Protocol)
		}
	}

	// No duplicate provider names.
	seen := make(map[string]bool, len(names))
	dupSorted := append([]string(nil), names...)
	sort.Strings(dupSorted)
	for _, n := range dupSorted {
		if seen[n] {
			t.Errorf("duplicate provider name %q in registry", n)
		}
		seen[n] = true
	}
}
