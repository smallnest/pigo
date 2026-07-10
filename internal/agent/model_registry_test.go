package agent

import (
	"context"
	"testing"
)

// dirProvider is a minimal Provider for registry tests.
type dirProvider struct {
	name   string
	models []Model
}

func (p dirProvider) Name() string    { return p.name }
func (p dirProvider) Models() []Model { return p.models }
func (p dirProvider) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	return NewAssistantMessageEventStream(0), nil
}

func TestModelRegistryResolve(t *testing.T) {
	r := NewModelRegistry()
	p := dirProvider{name: "anthropic", models: []Model{
		{Provider: "anthropic", ID: "claude-opus-4-8", ContextWindow: 200000},
	}}
	if err := r.RegisterProvider(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	m, prov, err := r.Resolve("claude-opus-4-8")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if m.ContextWindow != 200000 || prov.Name() != "anthropic" {
		t.Errorf("resolved wrong: model=%+v provider=%s", m, prov.Name())
	}
}

func TestModelRegistryUnknownID(t *testing.T) {
	r := NewModelRegistry()
	if _, _, err := r.Resolve("ghost"); err == nil {
		t.Fatal("unknown model id must error")
	}
	if _, ok := r.Model("ghost"); ok {
		t.Error("unknown model id must report not found")
	}
}

func TestModelRegistryDuplicateID(t *testing.T) {
	r := NewModelRegistry()
	p1 := dirProvider{name: "a", models: []Model{{Provider: "a", ID: "shared"}}}
	p2 := dirProvider{name: "b", models: []Model{{Provider: "b", ID: "shared"}}}
	if err := r.RegisterProvider(p1); err != nil {
		t.Fatalf("register p1: %v", err)
	}
	if err := r.RegisterProvider(p2); err == nil {
		t.Fatal("duplicate model id across providers must error")
	}
}

func TestModelRegistryDuplicateProvider(t *testing.T) {
	r := NewModelRegistry()
	p := dirProvider{name: "a", models: []Model{{Provider: "a", ID: "x"}}}
	if err := r.RegisterProvider(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := r.RegisterProvider(dirProvider{name: "a"}); err == nil {
		t.Fatal("duplicate provider name must error")
	}
}

func TestModelRegistryLoadCatalog(t *testing.T) {
	r := NewModelRegistry()
	data := []byte(`[{"provider":"openai","id":"gpt-5","contextWindow":400000,"supportsTools":true}]`)
	if err := r.LoadCatalog(data); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	m, ok := r.Model("gpt-5")
	if !ok || m.ContextWindow != 400000 || !m.SupportsTools {
		t.Errorf("catalog entry wrong: %+v (ok=%v)", m, ok)
	}
	// Catalog alone does not register a provider → Resolve fails cleanly.
	if _, _, err := r.Resolve("gpt-5"); err == nil {
		t.Error("catalog-only model must fail Resolve (no provider)")
	}
}

func TestModelRegistryLoadCatalogBadJSON(t *testing.T) {
	r := NewModelRegistry()
	if err := r.LoadCatalog([]byte(`not json`)); err == nil {
		t.Fatal("bad catalog JSON must error")
	}
}

func TestModelRegistryDefaults(t *testing.T) {
	r := NewModelRegistryWithDefaults()
	if _, ok := r.Model("claude-opus-4-8"); !ok {
		t.Error("default catalog must include claude-opus-4-8")
	}
	models := r.Models()
	if len(models) != len(DefaultCatalog) {
		t.Errorf("Models() count = %d, want %d", len(models), len(DefaultCatalog))
	}
	// Sorted by id.
	for i := 1; i < len(models); i++ {
		if models[i-1].ID > models[i].ID {
			t.Errorf("Models() not sorted: %q > %q", models[i-1].ID, models[i].ID)
		}
	}
}
