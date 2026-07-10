// This file implements the model registry and provider directory (US-011): map
// a model id to its Model metadata and the Provider that serves it. The registry
// carries a built-in default catalog and can be extended from a data file.
package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// ModelRegistry resolves a model id to its Model metadata and owning Provider.
// It is safe for concurrent use.
type ModelRegistry struct {
	mu        sync.RWMutex
	models    map[string]Model    // id → metadata
	providers map[string]Provider // provider name → provider
}

// NewModelRegistry builds an empty registry. Use RegisterProvider to add a
// provider and its models, or LoadCatalog to seed metadata from a data file.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		models:    make(map[string]Model),
		providers: make(map[string]Provider),
	}
}

// RegisterProvider adds a provider and indexes all of its Models by id. A model
// id already registered by another provider is rejected to keep resolution
// unambiguous.
func (r *ModelRegistry) RegisterProvider(p Provider) error {
	if p == nil {
		return fmt.Errorf("model registry: nil provider")
	}
	name := p.Name()
	if name == "" {
		return fmt.Errorf("model registry: provider has empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("model registry: provider %q already registered", name)
	}
	for _, m := range p.Models() {
		if m.ID == "" {
			return fmt.Errorf("model registry: provider %q has a model with empty id", name)
		}
		if existing, dup := r.models[m.ID]; dup {
			return fmt.Errorf("model registry: model id %q already registered by provider %q", m.ID, existing.Provider)
		}
	}
	r.providers[name] = p
	for _, m := range p.Models() {
		// Ensure the model's Provider field matches its owner.
		if m.Provider == "" {
			m.Provider = name
		}
		r.models[m.ID] = m
	}
	return nil
}

// Model returns the metadata for a model id. The bool is false for an unknown
// id (callers must handle this explicitly — FR requires unknown ids to error).
func (r *ModelRegistry) Model(id string) (Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[id]
	return m, ok
}

// Resolve returns both the Model metadata and the Provider that serves it. It
// errors clearly for an unknown model id or a model whose provider is missing.
func (r *ModelRegistry) Resolve(id string) (Model, Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[id]
	if !ok {
		return Model{}, nil, fmt.Errorf("unknown model id %q", id)
	}
	p, ok := r.providers[m.Provider]
	if !ok {
		return Model{}, nil, fmt.Errorf("model %q references unregistered provider %q", id, m.Provider)
	}
	return m, p, nil
}

// Provider returns a registered provider by name.
func (r *ModelRegistry) Provider(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// Models lists all registered model metadata, sorted by id for stable output.
func (r *ModelRegistry) Models() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Model, 0, len(r.models))
	for _, m := range r.models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LoadCatalog merges Model metadata parsed from a JSON catalog into the
// registry. Catalog entries provide/override metadata for model ids; they do
// not register providers (a provider must still be registered to Resolve). This
// lets a data file supply capability metadata independently of provider code.
func (r *ModelRegistry) LoadCatalog(data []byte) error {
	var catalog []Model
	if err := json.Unmarshal(data, &catalog); err != nil {
		return fmt.Errorf("model registry: parse catalog: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range catalog {
		if m.ID == "" {
			return fmt.Errorf("model registry: catalog entry has empty id")
		}
		r.models[m.ID] = m
	}
	return nil
}

// DefaultCatalog is the built-in default model catalog. It is intentionally
// minimal — provider packages register the authoritative models; this seeds
// metadata for ids that may be referenced before their provider is wired.
var DefaultCatalog = []Model{
	{Provider: "anthropic", ID: "claude-opus-4-8", DisplayName: "Claude Opus 4.8", ContextWindow: 200000, MaxOutputTokens: 32000, SupportsThinking: true, SupportsTools: true},
	{Provider: "anthropic", ID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", ContextWindow: 200000, MaxOutputTokens: 64000, SupportsThinking: true, SupportsTools: true},
	{Provider: "anthropic", ID: "claude-haiku-4-5-20251001", DisplayName: "Claude Haiku 4.5", ContextWindow: 200000, MaxOutputTokens: 32000, SupportsThinking: false, SupportsTools: true},
	{Provider: "openai", ID: "gpt-5", DisplayName: "GPT-5", ContextWindow: 400000, MaxOutputTokens: 128000, SupportsThinking: true, SupportsTools: true},
}

// NewModelRegistryWithDefaults returns a registry seeded with DefaultCatalog
// metadata (no providers registered yet).
func NewModelRegistryWithDefaults() *ModelRegistry {
	r := NewModelRegistry()
	for _, m := range DefaultCatalog {
		r.models[m.ID] = m
	}
	return r
}
