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

	// --- NVIDIA NIM (hosted, OpenAI-compatible) ---
	{Provider: "nvidia", ID: "meta/llama-3.3-70b-instruct", DisplayName: "Llama 3.3 70B (NVIDIA)"},
	{Provider: "nvidia", ID: "meta/llama-3.1-405b-instruct", DisplayName: "Llama 3.1 405B (NVIDIA)"},
	{Provider: "nvidia", ID: "deepseek-ai/deepseek-r1", DisplayName: "DeepSeek R1 (NVIDIA)"},
	{Provider: "nvidia", ID: "qwen/qwen2.5-coder-32b-instruct", DisplayName: "Qwen 2.5 Coder 32B (NVIDIA)"},
	{Provider: "nvidia", ID: "nvidia/llama-3.1-nemotron-70b-instruct", DisplayName: "Nemotron 70B (NVIDIA)"},
	{Provider: "nvidia", ID: "mistralai/mixtral-8x22b-instruct-v0.1", DisplayName: "Mixtral 8x22B (NVIDIA)"},

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
