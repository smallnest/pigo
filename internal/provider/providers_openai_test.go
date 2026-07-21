// Node #186: end-to-end wiring test for every OpenAI-protocol built-in provider.
//
// For each provider reachable via --provider (US-005) this asserts three things:
//   (a) the registry spec reports Protocol "openai" and the PRD-mandated default
//       base URL,
//   (b) its primary API-key env var resolves through envAPIKey (the same path
//       auth.go uses at request time), and
//   (c) the generic OpenAI-compatible construction path used by main.go's
//       resolveNamedProvider builds a non-nil driver bound to the spec's model.
//
// It also pins the restored legacy key aliases (CLAUDE_API_KEY, GOOGLE_API_KEY,
// NVIDIA_NIM_API_KEY) and the non-standard HF_TOKEN key name.
//
// This file is intentionally separate from providers_test.go: a sibling node
// edits that file concurrently.
package provider

import "testing"

// openAIWiringCase describes one OpenAI-protocol provider's expected registry
// metadata and primary key env var.
type openAIWiringCase struct {
	name       string
	baseURL    string
	primaryEnv string
}

// openAIProviderCases lists every OpenAI-protocol provider that must work
// end-to-end via --provider (US-005). Base URLs mirror the PRD's Technical
// Considerations table.
var openAIProviderCases = []openAIWiringCase{
	{"deepseek", "https://api.deepseek.com", "DEEPSEEK_API_KEY"},
	{"groq", "https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	{"xai", "https://api.x.ai/v1", "XAI_API_KEY"},
	{"cerebras", "https://api.cerebras.ai/v1", "CEREBRAS_API_KEY"},
	{"mistral", "https://api.mistral.ai", "MISTRAL_API_KEY"},
	{"moonshotai", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"},
	{"moonshotai-cn", "https://api.moonshot.cn/v1", "MOONSHOT_API_KEY"},
	{"fireworks", "https://api.fireworks.ai/inference", "FIREWORKS_API_KEY"},
	{"together", "https://api.together.ai/v1", "TOGETHER_API_KEY"},
	{"openrouter", "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
	{"nvidia", "https://integrate.api.nvidia.com/v1", "NVIDIA_API_KEY"},
	{"zai", "https://api.z.ai/api/coding/paas/v4", "ZAI_API_KEY"},
	{"zai-coding-cn", "https://open.bigmodel.cn/api/coding/paas/v4", "ZAI_CODING_CN_API_KEY"},
	{"kimi-coding", "https://api.kimi.com/coding", "KIMI_API_KEY"},
	{"opencode", "https://opencode.ai/zen", "OPENCODE_API_KEY"},
	{"opencode-go", "https://opencode.ai/zen/go", "OPENCODE_API_KEY"},
	{"huggingface", "https://router.huggingface.co/v1", "HF_TOKEN"},
	{"ant-ling", "https://api.ant-ling.com/v1", "ANT_LING_API_KEY"},
	{"vercel-ai-gateway", "https://ai-gateway.vercel.sh", "AI_GATEWAY_API_KEY"},
	{"xiaomi", "https://api.xiaomimimo.com/v1", "XIAOMI_API_KEY"},
	{"xiaomi-token-plan-cn", "https://token-plan-cn.xiaomimimo.com/v1", "XIAOMI_TOKEN_PLAN_CN_API_KEY"},
	{"xiaomi-token-plan-ams", "https://token-plan-ams.xiaomimimo.com/v1", "XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
	{"xiaomi-token-plan-sgp", "https://token-plan-sgp.xiaomimimo.com/v1", "XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
}

func TestOpenAIProviderWiring(t *testing.T) {
	for _, tc := range openAIProviderCases {
		t.Run(tc.name, func(t *testing.T) {
			// (a) registry spec: protocol + default base URL + primary env var.
			spec, ok := LookupProviderSpec(tc.name)
			if !ok {
				t.Fatalf("LookupProviderSpec(%q): not found in registry", tc.name)
			}
			if spec.Protocol != ProtocolOpenAI {
				t.Errorf("Protocol = %q, want %q", spec.Protocol, ProtocolOpenAI)
			}
			if spec.DefaultBaseURL != tc.baseURL {
				t.Errorf("DefaultBaseURL = %q, want %q", spec.DefaultBaseURL, tc.baseURL)
			}
			if len(spec.EnvVars) == 0 || spec.EnvVars[0] != tc.primaryEnv {
				t.Errorf("primary EnvVar = %v, want first = %q", spec.EnvVars, tc.primaryEnv)
			}

			// (b) key resolution via the primary env var, using the same
			// envAPIKey path auth.go relies on at request time.
			t.Setenv(tc.primaryEnv, "sk-"+tc.name)
			if got := envAPIKey(tc.name); got != "sk-"+tc.name {
				t.Errorf("envAPIKey(%q) = %q, want %q", tc.name, got, "sk-"+tc.name)
			}

			// (c) construction path equivalent to main.go's resolveNamedProvider
			// for an openai-protocol spec: build a generic OpenAI-compatible
			// driver against the spec's base URL, bound to the spec's model.
			models := []Model{{Provider: spec.Name, ID: "test-model", SupportsImages: true}}
			drv := NewOpenAICompatibleProvider(spec.DefaultBaseURL, models)
			if drv == nil {
				t.Fatalf("NewOpenAICompatibleProvider(%q) returned nil", spec.DefaultBaseURL)
			}
			got := drv.Models()
			if len(got) != 1 || got[0].Provider != spec.Name || got[0].ID != "test-model" {
				t.Errorf("driver Models() = %+v, want one model bound to provider %q", got, spec.Name)
			}
		})
	}
}

// TestLegacyKeyAliases pins the secondary env vars restored to the registry so
// credentials set under older names still resolve.
func TestLegacyKeyAliases(t *testing.T) {
	aliases := []struct {
		provider string
		primary  string // primary env var (must NOT be set for the alias to be exercised)
		alias    string // legacy alias env var under test
	}{
		{"anthropic", "ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
		{"google", "GEMINI_API_KEY", "GOOGLE_API_KEY"},
		{"nvidia", "NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"},
	}
	for _, a := range aliases {
		t.Run(a.provider, func(t *testing.T) {
			// Ensure the alias is listed in the registry's EnvVars.
			spec, ok := LookupProviderSpec(a.provider)
			if !ok {
				t.Fatalf("LookupProviderSpec(%q): not found", a.provider)
			}
			found := false
			for _, e := range spec.EnvVars {
				if e == a.alias {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("EnvVars %v missing legacy alias %q", spec.EnvVars, a.alias)
			}
			// Clear the primary so only the alias can satisfy resolution, then
			// assert the alias resolves.
			t.Setenv(a.primary, "")
			t.Setenv(a.alias, "legacy-"+a.provider)
			if got := envAPIKey(a.provider); got != "legacy-"+a.provider {
				t.Errorf("envAPIKey(%q) via %s = %q, want %q", a.provider, a.alias, got, "legacy-"+a.provider)
			}
		})
	}
}

// TestHuggingFaceTokenEnv asserts the non-standard HF_TOKEN key name resolves
// for huggingface (it does not follow the <PROVIDER>_API_KEY convention).
func TestHuggingFaceTokenEnv(t *testing.T) {
	spec, ok := LookupProviderSpec("huggingface")
	if !ok {
		t.Fatal("LookupProviderSpec(\"huggingface\"): not found")
	}
	if len(spec.EnvVars) == 0 || spec.EnvVars[0] != "HF_TOKEN" {
		t.Fatalf("huggingface EnvVars = %v, want first = HF_TOKEN", spec.EnvVars)
	}
	t.Setenv("HF_TOKEN", "hf-secret")
	if got := envAPIKey("huggingface"); got != "hf-secret" {
		t.Errorf("envAPIKey(\"huggingface\") = %q, want %q", got, "hf-secret")
	}
}
