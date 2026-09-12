package prompts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// TestModelCommandSwitchesToBareProviderName verifies /model zai selects the
// zai provider's default model (issue #564): live.Model carries the concrete
// preset id so the next turn's wire request targets a real model instead of
// sending the literal provider name to OpenRouter.
func TestModelCommandSwitchesToBareProviderName(t *testing.T) {
	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openrouter"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model zai")
	if err != nil {
		t.Fatalf("ResolveOutcome /model zai: %v", err)
	}
	if live.Model != "glm-4.7" {
		t.Errorf("live.Model = %q, want glm-4.7", live.Model)
	}
	if live.ProviderName != "zai" {
		t.Errorf("live.ProviderName = %q, want zai", live.ProviderName)
	}
	if !strings.Contains(out.Message, "glm-4.7") || !strings.Contains(out.Message, "zai") {
		t.Errorf("message = %q, want it to mention glm-4.7 (zai)", out.Message)
	}
}

// TestModelCommandConcreteIdUnchanged verifies a concrete model id switches
// verbatim, and a bare provider name without preset models reports the
// mismatch instead of silently falling back to OpenRouter.
func TestModelCommandConcreteIdUnchanged(t *testing.T) {
	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openrouter"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/model glm-5.2")
	if err != nil {
		t.Fatalf("ResolveOutcome /model glm-5.2: %v", err)
	}
	if live.Model != "glm-5.2" || live.ProviderName != "zai" {
		t.Errorf("live = (%q, %q), want (glm-5.2, zai)", live.Model, live.ProviderName)
	}
	if !strings.Contains(out.Message, "glm-5.2") {
		t.Errorf("message = %q, want it to mention glm-5.2", out.Message)
	}

	// A concrete id for another provider switches verbatim as before.
	out, err = reg.ResolveOutcome("/model deepseek-v4-pro")
	if err != nil {
		t.Fatalf("ResolveOutcome /model deepseek-v4-pro: %v", err)
	}
	if live.Model != "deepseek-v4-pro" || live.ProviderName != "deepseek" {
		t.Errorf("live = (%q, %q), want (deepseek-v4-pro, deepseek)", live.Model, live.ProviderName)
	}
}

// TestModelsFetchCommandAndSwitch drives "/models fetch" against an
// httptest endpoint (issue #566): the catalog lands on live.FetchedModels,
// and switching to a fetched id stays on the gateway that served it instead
// of falling through the heuristic chain.
func TestModelsFetchCommandAndSwitch(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-b"},{"id":"m-a"},{"id":"m-b"}]}`))
	}))
	defer srv.Close()

	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openai", BaseURL: srv.URL + "/v1"}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("openai", "test-key")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/models fetch")
	if err != nil {
		t.Fatalf("ResolveOutcome /models fetch: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if len(live.FetchedModels) != 2 || live.FetchedModels[0] != "m-a" || live.FetchedModels[1] != "m-b" {
		t.Fatalf("live.FetchedModels = %v, want [m-a m-b]", live.FetchedModels)
	}
	if live.FetchedAt.IsZero() {
		t.Error("FetchedAt not stamped")
	}
	if !strings.Contains(out.Message, "2 models") {
		t.Errorf("message = %q, want it to mention 2 models", out.Message)
	}

	// Switching to a fetched id pins the live provider.
	out, err = reg.ResolveOutcome("/model m-b")
	if err != nil {
		t.Fatalf("ResolveOutcome /model m-b: %v", err)
	}
	if live.Model != "m-b" || live.ProviderName != "openai" {
		t.Fatalf("live = (%q, %q), want (m-b, openai)", live.Model, live.ProviderName)
	}
	if models := live.Provider.Models(); len(models) != 1 || models[0].ID != "m-b" || models[0].Provider != "openai" {
		t.Fatalf("wire models = %+v, want one openai/m-b entry", models)
	}
	if !strings.Contains(out.Message, "fetched catalog") {
		t.Errorf("message = %q, want the fetched-catalog note", out.Message)
	}
}

// TestModelsFetchDegrades verifies a failing endpoint degrades gracefully:
// the error is reported and the static preset listing still works.
func TestModelsFetchDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	live := &cli.LiveConfig{Model: "openrouter/free", ProviderName: "openai", BaseURL: srv.URL}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride("openai", "test-key")
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)

	out, err := reg.ResolveOutcome("/models fetch")
	if err != nil {
		t.Fatalf("ResolveOutcome /models fetch: %v", err)
	}
	if live.FetchedModels != nil {
		t.Errorf("live.FetchedModels = %v, want nil after failed fetch", live.FetchedModels)
	}
	if !strings.Contains(out.Message, "fetch failed") {
		t.Errorf("message = %q, want the fetch-failed note", out.Message)
	}
	if live.Model != "openrouter/free" {
		t.Errorf("live.Model = %q, want unchanged", live.Model)
	}
}
