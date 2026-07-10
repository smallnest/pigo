package runtime

// Tests for the layered configuration system (US-023, #42): the precedence
// order (default < global < project < env/CLI), per-provider credential merge,
// and the hard-error paths for malformed files and invalid field values.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// ptr is a helper for building pointer-valued config-layer fields in tests.
func ptr[T any](v T) *T { return &v }

// TestResolveConfigPrecedence is the acceptance-critical test: a field set in a
// higher layer overrides the same field in every lower layer, and the winner is
// always the highest layer that set it.
func TestResolveConfigPrecedence(t *testing.T) {
	def := DefaultConfigLayer()
	global := &ConfigLayer{Model: ptr("global/model"), ThinkingLevel: ptr("low")}
	project := &ConfigLayer{Model: ptr("project/model")}
	env := &ConfigLayer{Model: ptr("env/model"), Provider: ptr("bedrock")}

	cfg, err := ResolveConfig(&def, global, project, env)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	// Model set in all four layers → env (highest) wins.
	if cfg.Model != "env/model" {
		t.Errorf("Model = %q, want env/model (highest layer wins)", cfg.Model)
	}
	// Provider set only in env → env value.
	if cfg.Provider != "bedrock" {
		t.Errorf("Provider = %q, want bedrock", cfg.Provider)
	}
	// ThinkingLevel set in global only (not project/env) → global value shows through.
	if cfg.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("ThinkingLevel = %q, want low (from global, lower layers don't set it)", cfg.ThinkingLevel)
	}
	// ToolExecutionMode set only in default → default shows through.
	if cfg.ToolExecutionMode != agentcore.ToolExecutionParallel {
		t.Errorf("ToolExecutionMode = %q, want parallel (default)", cfg.ToolExecutionMode)
	}
}

// TestResolveConfigCredentialMerge verifies credentials merge per-provider: a
// higher layer overrides one provider's key while a lower layer's other-provider
// key is retained.
func TestResolveConfigCredentialMerge(t *testing.T) {
	def := DefaultConfigLayer()
	global := &ConfigLayer{Credentials: map[string]string{"openrouter": "or-low", "ollama": "ol-key"}}
	project := &ConfigLayer{Credentials: map[string]string{"openrouter": "or-high"}}

	cfg, err := ResolveConfig(&def, global, project)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.Credentials["openrouter"] != "or-high" {
		t.Errorf("openrouter key = %q, want or-high (project overrides global)", cfg.Credentials["openrouter"])
	}
	if cfg.Credentials["ollama"] != "ol-key" {
		t.Errorf("ollama key = %q, want ol-key (retained from global)", cfg.Credentials["ollama"])
	}
}

// TestResolveConfigInvalidValues verifies invalid field values are hard errors.
func TestResolveConfigInvalidValues(t *testing.T) {
	def := DefaultConfigLayer()
	cases := []struct {
		name  string
		layer *ConfigLayer
	}{
		{"bad mode", &ConfigLayer{ToolExecutionMode: ptr("concurrent")}},
		{"bad thinking", &ConfigLayer{ThinkingLevel: ptr("ultra")}},
		{"empty model", &ConfigLayer{Model: ptr("")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ResolveConfig(&def, tc.layer); err == nil {
				t.Errorf("%s must be a hard error, got nil", tc.name)
			}
		})
	}
}

// TestLoadConfigLayerMissingAndMalformed verifies a missing file is not an error
// (nil layer) while a malformed file is.
func TestLoadConfigLayerMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()

	// Missing file → nil, nil.
	layer, err := LoadConfigLayer(filepath.Join(dir, "nope.json"))
	if err != nil || layer != nil {
		t.Errorf("missing file: got (%v, %v), want (nil, nil)", layer, err)
	}

	// Malformed file → error.
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfigLayer(bad); err == nil {
		t.Error("malformed config file must return an error")
	}

	// Well-formed file → decoded layer.
	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"model":"m","provider":"p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	layer, err = LoadConfigLayer(good)
	if err != nil {
		t.Fatalf("good file: %v", err)
	}
	if layer == nil || layer.Model == nil || *layer.Model != "m" {
		t.Errorf("good file decoded incorrectly: %+v", layer)
	}
}

// TestEnvConfigLayer verifies env vars map to the right fields and unset vars
// leave fields nil.
func TestEnvConfigLayer(t *testing.T) {
	env := map[string]string{
		"PIGO_MODEL":               "env/m",
		"PIGO_THINKING_LEVEL":      "high",
		"PIGO_TOOL_EXECUTION_MODE": "sequential",
	}
	layer := EnvConfigLayer(func(k string) string { return env[k] })
	if layer.Model == nil || *layer.Model != "env/m" {
		t.Errorf("PIGO_MODEL not captured: %+v", layer.Model)
	}
	if layer.ThinkingLevel == nil || *layer.ThinkingLevel != "high" {
		t.Errorf("PIGO_THINKING_LEVEL not captured: %+v", layer.ThinkingLevel)
	}
	if layer.ToolExecutionMode == nil || *layer.ToolExecutionMode != "sequential" {
		t.Errorf("PIGO_TOOL_EXECUTION_MODE not captured: %+v", layer.ToolExecutionMode)
	}
	// Unset var → nil field.
	if layer.Provider != nil {
		t.Errorf("unset PIGO_PROVIDER should leave Provider nil, got %v", *layer.Provider)
	}
}

// TestResolveConfigDefaultsAlone verifies the default layer alone yields a valid
// config.
func TestResolveConfigDefaultsAlone(t *testing.T) {
	def := DefaultConfigLayer()
	cfg, err := ResolveConfig(&def)
	if err != nil {
		t.Fatalf("default-only config must be valid: %v", err)
	}
	if cfg.Model == "" || cfg.ToolExecutionMode == "" || cfg.ThinkingLevel == "" {
		t.Errorf("default config incomplete: %+v", cfg)
	}
}
