// This file defines the central provider registry (US-001): a single source of
// truth for every built-in provider's metadata — its name, the environment
// variables that carry its API key (in precedence order), its default base URL,
// wire protocol, auth scheme, any extra headers, and provider-specific base-URL
// override env vars.
//
// The registry is deliberately additive and self-contained: later nodes wire
// auth resolution (auth.go), the --provider flag (main.go), base_url overrides,
// and per-provider construction (providers.go) to READ from it. This node only
// introduces the data + a lookup, so it does not change existing behavior.
//
// Data source: the PRD "Technical Considerations" table
// (tasks/prd-provider-env-parity.md), derived from pi's env-api-keys.ts and the
// per-provider *.models.ts files.
//
// Security: the registry holds only env var NAMES, never secret values. Keys are
// resolved from the environment at request time (see auth.go) and never logged.
package provider

// ProviderSpec is the metadata describing one built-in provider. It is the
// single source of truth consumed by auth resolution, the --provider flag,
// base_url override handling, and per-provider wiring.
type ProviderSpec struct {
	// Name is the canonical provider name (e.g. "deepseek", "zai-coding-cn").
	Name string
	// EnvVars lists the environment variables checked (in precedence order) for
	// this provider's API key. The first non-empty value wins.
	EnvVars []string
	// DefaultBaseURL is the provider's default API endpoint. It may be a template
	// (containing placeholders like {region}) for providers whose endpoint is
	// composed from additional parameters (Bedrock, Vertex, Cloudflare, Azure).
	DefaultBaseURL string
	// Protocol is the wire protocol the provider speaks: "openai" (OpenAI Chat
	// Completions) or "anthropic" (Anthropic Messages).
	Protocol string
	// AuthScheme names how credentials are attached: "bearer", "x-api-key",
	// "aws", "azure", or "special".
	AuthScheme string
	// ExtraHeaders are provider-specific headers attached to every request (may
	// be nil).
	ExtraHeaders map[string]string
	// BaseURLEnvVars lists provider-specific base-URL override environment
	// variables (e.g. AZURE_OPENAI_BASE_URL), in precedence order. May be empty;
	// the generic <PROVIDER>_BASE_URL convention is handled by callers.
	BaseURLEnvVars []string
}

// Protocol values.
const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
)

// AuthScheme values.
const (
	AuthBearer  = "bearer"
	AuthXAPIKey = "x-api-key"
	AuthAWS     = "aws"
	AuthAzure   = "azure"
	AuthSpecial = "special"
)

// providerRegistry is the ordered list of all built-in provider specs. Order is
// stable so callers that enumerate providers (e.g. --help) get a deterministic
// list. LookupProviderSpec indexes it by name.
var providerRegistry = []ProviderSpec{
	{
		Name:           "anthropic",
		EnvVars:        []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
		DefaultBaseURL: anthropicBaseURL, // https://api.anthropic.com/v1
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthXAPIKey,
	},
	{
		Name:           "openai",
		EnvVars:        []string{"OPENAI_API_KEY"},
		DefaultBaseURL: "https://api.openai.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "ant-ling",
		EnvVars:        []string{"ANT_LING_API_KEY"},
		DefaultBaseURL: "https://api.ant-ling.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "deepseek",
		EnvVars:        []string{"DEEPSEEK_API_KEY"},
		DefaultBaseURL: "https://api.deepseek.com",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "nvidia",
		EnvVars:        []string{"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"},
		DefaultBaseURL: nvidiaBaseURL, // https://integrate.api.nvidia.com/v1
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "google",
		EnvVars:        []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "groq",
		EnvVars:        []string{"GROQ_API_KEY"},
		DefaultBaseURL: "https://api.groq.com/openai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "cerebras",
		EnvVars:        []string{"CEREBRAS_API_KEY"},
		DefaultBaseURL: "https://api.cerebras.ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "xai",
		EnvVars:        []string{"XAI_API_KEY"},
		DefaultBaseURL: "https://api.x.ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "openrouter",
		EnvVars:        []string{"OPENROUTER_API_KEY"},
		DefaultBaseURL: openRouterBaseURL, // https://openrouter.ai/api/v1
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "vercel-ai-gateway",
		EnvVars:        []string{"AI_GATEWAY_API_KEY"},
		DefaultBaseURL: "https://ai-gateway.vercel.sh",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "zai",
		EnvVars:        []string{"ZAI_API_KEY"},
		DefaultBaseURL: "https://api.z.ai/api/coding/paas/v4",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "zai-coding-cn",
		EnvVars:        []string{"ZAI_CODING_CN_API_KEY"},
		DefaultBaseURL: "https://open.bigmodel.cn/api/coding/paas/v4",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "mistral",
		EnvVars:        []string{"MISTRAL_API_KEY"},
		DefaultBaseURL: "https://api.mistral.ai",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "minimax",
		EnvVars:        []string{"MINIMAX_API_KEY"},
		DefaultBaseURL: "https://api.minimax.io/anthropic",
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthXAPIKey,
	},
	{
		Name:           "minimax-cn",
		EnvVars:        []string{"MINIMAX_CN_API_KEY"},
		DefaultBaseURL: "https://api.minimaxi.com/anthropic",
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthXAPIKey,
	},
	{
		Name:           "moonshotai",
		EnvVars:        []string{"MOONSHOT_API_KEY"},
		DefaultBaseURL: "https://api.moonshot.ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "moonshotai-cn",
		EnvVars:        []string{"MOONSHOT_API_KEY"},
		DefaultBaseURL: "https://api.moonshot.cn/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "huggingface",
		EnvVars:        []string{"HF_TOKEN"},
		DefaultBaseURL: "https://router.huggingface.co/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "fireworks",
		EnvVars:        []string{"FIREWORKS_API_KEY"},
		DefaultBaseURL: "https://api.fireworks.ai/inference",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "together",
		EnvVars:        []string{"TOGETHER_API_KEY"},
		DefaultBaseURL: "https://api.together.ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "opencode",
		EnvVars:        []string{"OPENCODE_API_KEY"},
		DefaultBaseURL: "https://opencode.ai/zen",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "opencode-go",
		EnvVars:        []string{"OPENCODE_API_KEY"},
		DefaultBaseURL: "https://opencode.ai/zen/go",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "kimi-coding",
		EnvVars:        []string{"KIMI_API_KEY"},
		DefaultBaseURL: "https://api.kimi.com/coding",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "xiaomi",
		EnvVars:        []string{"XIAOMI_API_KEY"},
		DefaultBaseURL: "https://api.xiaomimimo.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "xiaomi-token-plan-cn",
		EnvVars:        []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"},
		DefaultBaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "xiaomi-token-plan-ams",
		EnvVars:        []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
		DefaultBaseURL: "https://token-plan-ams.xiaomimimo.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "xiaomi-token-plan-sgp",
		EnvVars:        []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
		DefaultBaseURL: "https://token-plan-sgp.xiaomimimo.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:    "azure-openai-responses",
		EnvVars: []string{"AZURE_OPENAI_API_KEY"},
		// Endpoint is composed from AZURE_OPENAI_BASE_URL / AZURE_OPENAI_RESOURCE_NAME
		// per the Azure OpenAI convention; there is no fixed public default.
		DefaultBaseURL: "",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthAzure,
		BaseURLEnvVars: []string{"AZURE_OPENAI_BASE_URL"},
	},
	{
		Name:    "amazon-bedrock",
		EnvVars: []string{"AWS_BEARER_TOKEN_BEDROCK"},
		// Region-specific runtime endpoint; {AWS_REGION} defaults to us-east-1.
		DefaultBaseURL: "https://bedrock-runtime.{AWS_REGION}.amazonaws.com",
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthAWS,
	},
	{
		Name:    "google-vertex",
		EnvVars: []string{"GOOGLE_CLOUD_API_KEY"},
		// Location-specific endpoint; protocol varies by model (Gemini vs Claude).
		DefaultBaseURL: "https://{location}-aiplatform.googleapis.com",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthSpecial,
	},
	{
		Name:    "cloudflare-workers-ai",
		EnvVars: []string{"CLOUDFLARE_API_KEY"},
		// {id} is CLOUDFLARE_ACCOUNT_ID.
		DefaultBaseURL: "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:    "cloudflare-ai-gateway",
		EnvVars: []string{"CLOUDFLARE_API_KEY"},
		// {acct}/{gw} are CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_GATEWAY_ID.
		DefaultBaseURL: "https://gateway.ai.cloudflare.com/v1/{account_id}/{gateway_id}/anthropic",
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthXAPIKey,
	},
}

// providerRegistryByName indexes providerRegistry by provider name for O(1)
// lookup. Built once at package init.
var providerRegistryByName = func() map[string]ProviderSpec {
	m := make(map[string]ProviderSpec, len(providerRegistry))
	for _, spec := range providerRegistry {
		m[spec.Name] = spec
	}
	return m
}()

// LookupProviderSpec returns the ProviderSpec for a provider name and whether it
// is a known built-in provider. The returned spec is a copy; mutating its slice
// or map fields is discouraged as they are shared with the registry.
func LookupProviderSpec(name string) (ProviderSpec, bool) {
	spec, ok := providerRegistryByName[name]
	return spec, ok
}

// ProviderSpecs returns all built-in provider specs in registry (display) order.
// Callers must not mutate the returned specs' slice/map fields.
func ProviderSpecs() []ProviderSpec {
	out := make([]ProviderSpec, len(providerRegistry))
	copy(out, providerRegistry)
	return out
}

// ProviderNames returns all built-in provider names in registry order.
func ProviderNames() []string {
	out := make([]string, len(providerRegistry))
	for i, spec := range providerRegistry {
		out[i] = spec.Name
	}
	return out
}
