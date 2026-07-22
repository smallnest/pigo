// This file defines the built-in preset catalog: a curated set of ready-to-use
// (provider, model) pairs a user can pick from without knowing each gateway's
// wire details (对标 pi agent's preset provider/model picker). It covers the
// OpenAI-compatible gateways pigo ships — OpenRouter and NVIDIA NIM — plus a few
// local Ollama defaults.
//
// A preset binds a model id to the provider that serves it and that provider's
// default endpoint, so selecting a preset is enough to build a working Provider.
// The naive prefix-based mapping (ollama/…) still works for arbitrary ids; the
// preset catalog is the "menu" of vetted choices surfaced to the user.
//
// Security: presets carry no secrets. Each provider resolves its API key by name
// from the environment at request time (see auth.go); keys are never embedded
// here or logged.
package provider

// PresetModel is one entry in the preset catalog: a model the user can select by
// id, the provider that serves it, and a short human label for the picker.
type PresetModel struct {
	// Provider is the owning provider name (e.g. "openrouter", "nvidia").
	Provider string
	// ID is the model id passed to the provider (e.g. "openai/gpt-4o").
	ID string
	// DisplayName is a friendly label shown in the picker; falls back to ID.
	DisplayName string
}

// Label returns the display label for a preset, falling back to the id.
func (p PresetModel) Label() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.ID
}

// PresetProviders lists the providers the preset catalog draws from, with the
// environment variable each expects its API key in (referenced by name only,
// never a value). Order is the display order in the picker.
var PresetProviders = []struct {
	Name   string
	EnvVar string
}{
	{Name: "openrouter", EnvVar: "OPENROUTER_API_KEY"},
	{Name: "nvidia", EnvVar: "NVIDIA_API_KEY"},
	{Name: "deepseek", EnvVar: "DEEPSEEK_API_KEY"},
	{Name: "groq", EnvVar: "GROQ_API_KEY"},
	{Name: "xai", EnvVar: "XAI_API_KEY"},
	{Name: "cerebras", EnvVar: "CEREBRAS_API_KEY"},
	{Name: "mistral", EnvVar: "MISTRAL_API_KEY"},
	{Name: "moonshotai", EnvVar: "MOONSHOT_API_KEY"},
	{Name: "zai", EnvVar: "ZAI_API_KEY"},
	{Name: "fireworks", EnvVar: "FIREWORKS_API_KEY"},
	{Name: "together", EnvVar: "TOGETHER_API_KEY"},
	{Name: "minimax", EnvVar: "MINIMAX_API_KEY"},
	{Name: "xiaomi", EnvVar: "XIAOMI_API_KEY"},
	{Name: "qianfan", EnvVar: "QIANFAN_API_KEY"},
	{Name: "volcengine", EnvVar: "ARK_API_KEY"},
	{Name: "dashscope", EnvVar: "DASHSCOPE_API_KEY"},
	{Name: "hunyuan", EnvVar: "HUNYUAN_API_KEY"},
	{Name: "ollama", EnvVar: ""}, // local, no key
}

// PresetCatalog is the built-in curated list of selectable models, grouped by
// provider in the order PresetProviders declares. These ids are the ones a user
// can `/model <id>` into or pick from `/models`; the list is representative, not
// exhaustive — any valid id for a known provider still works.
var PresetCatalog = []PresetModel{
	// --- OpenRouter (routes to many upstreams via one OpenAI-compatible API) ---
	{Provider: "openrouter", ID: "openai/gpt-4o", DisplayName: "GPT-4o (OpenRouter)"},
	{Provider: "openrouter", ID: "openai/gpt-4o-mini", DisplayName: "GPT-4o mini (OpenRouter)"},
	{Provider: "openrouter", ID: "anthropic/claude-3.5-sonnet", DisplayName: "Claude 3.5 Sonnet (OpenRouter)"},
	{Provider: "openrouter", ID: "anthropic/claude-3.7-sonnet", DisplayName: "Claude 3.7 Sonnet (OpenRouter)"},
	{Provider: "openrouter", ID: "google/gemini-2.0-flash-001", DisplayName: "Gemini 2.0 Flash (OpenRouter)"},
	{Provider: "openrouter", ID: "google/gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro (OpenRouter)"},
	{Provider: "openrouter", ID: "meta-llama/llama-3.3-70b-instruct", DisplayName: "Llama 3.3 70B (OpenRouter)"},
	{Provider: "openrouter", ID: "deepseek/deepseek-chat", DisplayName: "DeepSeek V3 (OpenRouter)"},
	{Provider: "openrouter", ID: "deepseek/deepseek-r1", DisplayName: "DeepSeek R1 (OpenRouter)"},
	{Provider: "openrouter", ID: "qwen/qwen-2.5-72b-instruct", DisplayName: "Qwen 2.5 72B (OpenRouter)"},
	{Provider: "openrouter", ID: "mistralai/mistral-large", DisplayName: "Mistral Large (OpenRouter)"},
	{Provider: "openrouter", ID: "x-ai/grok-2-1212", DisplayName: "Grok 2 (OpenRouter)"},

	// --- OpenRouter free tier (":free" ids are rate-limited but cost nothing) ---
	{Provider: "openrouter", ID: "deepseek/deepseek-r1:free", DisplayName: "DeepSeek R1 · free (OpenRouter)"},
	{Provider: "openrouter", ID: "deepseek/deepseek-chat-v3-0324:free", DisplayName: "DeepSeek V3 · free (OpenRouter)"},
	{Provider: "openrouter", ID: "meta-llama/llama-3.3-70b-instruct:free", DisplayName: "Llama 3.3 70B · free (OpenRouter)"},
	{Provider: "openrouter", ID: "google/gemini-2.0-flash-exp:free", DisplayName: "Gemini 2.0 Flash · free (OpenRouter)"},
	{Provider: "openrouter", ID: "qwen/qwen-2.5-72b-instruct:free", DisplayName: "Qwen 2.5 72B · free (OpenRouter)"},
	{Provider: "openrouter", ID: "qwen/qwq-32b:free", DisplayName: "QwQ 32B · free (OpenRouter)"},
	{Provider: "openrouter", ID: "mistralai/mistral-small-3.1-24b-instruct:free", DisplayName: "Mistral Small 3.1 24B · free (OpenRouter)"},
	{Provider: "openrouter", ID: "meta-llama/llama-4-maverick:free", DisplayName: "Llama 4 Maverick · free (OpenRouter)"},

	// --- NVIDIA NIM (hosted, OpenAI-compatible) ---
	{Provider: "nvidia", ID: "meta/llama-3.3-70b-instruct", DisplayName: "Llama 3.3 70B (NVIDIA)"},
	{Provider: "nvidia", ID: "meta/llama-3.1-405b-instruct", DisplayName: "Llama 3.1 405B (NVIDIA)"},
	{Provider: "nvidia", ID: "deepseek-ai/deepseek-r1", DisplayName: "DeepSeek R1 (NVIDIA)"},
	{Provider: "nvidia", ID: "qwen/qwen2.5-coder-32b-instruct", DisplayName: "Qwen 2.5 Coder 32B (NVIDIA)"},
	{Provider: "nvidia", ID: "nvidia/llama-3.1-nemotron-70b-instruct", DisplayName: "Nemotron 70B (NVIDIA)"},
	{Provider: "nvidia", ID: "mistralai/mixtral-8x22b-instruct-v0.1", DisplayName: "Mixtral 8x22B (NVIDIA)"},
	// NVIDIA's hosted NIM endpoint is free to call with a build.nvidia.com key.
	{Provider: "nvidia", ID: "meta/llama-3.1-8b-instruct", DisplayName: "Llama 3.1 8B (NVIDIA)"},
	{Provider: "nvidia", ID: "meta/llama-3.1-70b-instruct", DisplayName: "Llama 3.1 70B (NVIDIA)"},
	{Provider: "nvidia", ID: "deepseek-ai/deepseek-v3", DisplayName: "DeepSeek V3 (NVIDIA)"},
	{Provider: "nvidia", ID: "qwen/qwen2.5-7b-instruct", DisplayName: "Qwen 2.5 7B (NVIDIA)"},
	{Provider: "nvidia", ID: "google/gemma-2-9b-it", DisplayName: "Gemma 2 9B (NVIDIA)"},
	{Provider: "nvidia", ID: "microsoft/phi-3.5-mini-instruct", DisplayName: "Phi-3.5 Mini (NVIDIA)"},

	// --- DeepSeek (direct, OpenAI-compatible; ids from pi deepseek.models.ts) ---
	{Provider: "deepseek", ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash"},
	{Provider: "deepseek", ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro"},

	// --- Groq (fast inference; ids from pi groq.models.ts) ---
	{Provider: "groq", ID: "llama-3.3-70b-versatile", DisplayName: "Llama 3.3 70B (Groq)"},
	{Provider: "groq", ID: "openai/gpt-oss-120b", DisplayName: "GPT OSS 120B (Groq)"},
	{Provider: "groq", ID: "qwen/qwen3-32b", DisplayName: "Qwen3 32B (Groq)"},

	// --- xAI Grok (ids from pi xai.models.ts) ---
	{Provider: "xai", ID: "grok-4.5", DisplayName: "Grok 4.5"},
	{Provider: "xai", ID: "grok-4.3", DisplayName: "Grok 4.3"},

	// --- Cerebras (fast inference; ids from pi cerebras.models.ts) ---
	{Provider: "cerebras", ID: "gpt-oss-120b", DisplayName: "GPT OSS 120B (Cerebras)"},
	{Provider: "cerebras", ID: "zai-glm-4.7", DisplayName: "Z.AI GLM-4.7 (Cerebras)"},
	{Provider: "cerebras", ID: "gemma-4-31b", DisplayName: "Gemma 4 31B (Cerebras)"},

	// --- Mistral (ids from pi mistral.models.ts) ---
	{Provider: "mistral", ID: "mistral-large-latest", DisplayName: "Mistral Large (latest)"},
	{Provider: "mistral", ID: "mistral-medium-latest", DisplayName: "Mistral Medium (latest)"},
	{Provider: "mistral", ID: "codestral-latest", DisplayName: "Codestral (latest)"},
	{Provider: "mistral", ID: "devstral-medium-latest", DisplayName: "Devstral Medium (latest)"},

	// --- Moonshot AI Kimi (ids from pi moonshotai.models.ts) ---
	{Provider: "moonshotai", ID: "kimi-k2-thinking", DisplayName: "Kimi K2 Thinking"},
	{Provider: "moonshotai", ID: "kimi-k2.6", DisplayName: "Kimi K2.6"},
	{Provider: "moonshotai", ID: "kimi-k3", DisplayName: "Kimi K3"},

	// --- Z.AI GLM (ids from pi zai.models.ts) ---
	{Provider: "zai", ID: "glm-4.7", DisplayName: "GLM-4.7"},
	{Provider: "zai", ID: "glm-5.1", DisplayName: "GLM-5.1"},
	{Provider: "zai", ID: "glm-5.2", DisplayName: "GLM-5.2"},

	// --- Fireworks (ids from pi fireworks.models.ts) ---
	{Provider: "fireworks", ID: "accounts/fireworks/models/deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro (Fireworks)"},
	{Provider: "fireworks", ID: "accounts/fireworks/models/gpt-oss-120b", DisplayName: "GPT OSS 120B (Fireworks)"},
	{Provider: "fireworks", ID: "accounts/fireworks/models/kimi-k2p7-code", DisplayName: "Kimi K2.7 Code (Fireworks)"},

	// --- Together AI (ids from pi together.models.ts) ---
	{Provider: "together", ID: "deepseek-ai/DeepSeek-V4-Pro", DisplayName: "DeepSeek V4 Pro (Together)"},
	{Provider: "together", ID: "Qwen/Qwen3.7-Max", DisplayName: "Qwen3.7 Max (Together)"},
	{Provider: "together", ID: "meta-llama/Llama-3.3-70B-Instruct-Turbo", DisplayName: "Llama 3.3 70B Turbo (Together)"},

	// --- MiniMax (Anthropic-protocol; ids from pi minimax.models.ts) ---
	{Provider: "minimax", ID: "MiniMax-M2.7", DisplayName: "MiniMax-M2.7"},
	{Provider: "minimax", ID: "MiniMax-M3", DisplayName: "MiniMax-M3"},

	// --- Xiaomi MiMo (ids from pi xiaomi.models.ts) ---
	{Provider: "xiaomi", ID: "mimo-v2-pro", DisplayName: "MiMo-V2-Pro"},
	{Provider: "xiaomi", ID: "mimo-v2.5", DisplayName: "MiMo-V2.5"},
	{Provider: "xiaomi", ID: "mimo-v2.5-pro", DisplayName: "MiMo-V2.5-Pro"},

	// --- 百度智能云千帆 Qianfan (OpenAI-compatible; ERNIE family) ---
	{Provider: "qianfan", ID: "ernie-4.5-turbo-32k", DisplayName: "ERNIE 4.5 Turbo (百度千帆)"},

	// --- 字节火山引擎方舟 Volcengine Ark (OpenAI-compatible; Doubao family) ---
	// Some Ark models require a "推理接入点 ID (endpoint id)" instead of a model
	// name — use --base-url / -m to target those; this preset uses a model name.
	{Provider: "volcengine", ID: "doubao-seed-1-6", DisplayName: "Doubao Seed 1.6 (火山方舟)"},

	// --- 阿里云百炼 DashScope (OpenAI-compatible; Qwen family) ---
	{Provider: "dashscope", ID: "qwen-max", DisplayName: "Qwen Max (阿里百炼)"},

	// --- 腾讯混元 Hunyuan (OpenAI-compatible) ---
	{Provider: "hunyuan", ID: "hunyuan-turbos-latest", DisplayName: "Hunyuan TurboS (腾讯混元)"},

	// --- Ollama (local, no API key) ---
	{Provider: "ollama", ID: "ollama/llama3.3", DisplayName: "Llama 3.3 (local Ollama)"},
	{Provider: "ollama", ID: "ollama/qwen2.5-coder", DisplayName: "Qwen 2.5 Coder (local Ollama)"},
}

// LookupPreset returns the preset entry for a model id, if the id is in the
// catalog. Used to resolve a selected id to its owning provider.
func LookupPreset(id string) (PresetModel, bool) {
	for _, p := range PresetCatalog {
		if p.ID == id {
			return p, true
		}
	}
	return PresetModel{}, false
}

// PresetsByProvider returns the presets served by a given provider name, in
// catalog order.
func PresetsByProvider(providerName string) []PresetModel {
	var out []PresetModel
	for _, p := range PresetCatalog {
		if p.Provider == providerName {
			out = append(out, p)
		}
	}
	return out
}
