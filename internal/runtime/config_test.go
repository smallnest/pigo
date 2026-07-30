package runtime

// Tests for the layered configuration system (US-023, #42): the precedence
// order (default < global < project < env/CLI), per-provider credential merge,
// and the hard-error paths for malformed files and invalid field values.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/hooks"
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

// TestResolveConfigHooksAppendMerge verifies hooks are append-merged per event
// type across layers (FR-2) in ascending order, not overridden like scalars.
func TestResolveConfigHooksAppendMerge(t *testing.T) {
	def := DefaultConfigLayer()
	global := &ConfigLayer{Hooks: hooks.HookSet{
		"PreToolUse": {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: "global-pre"}}}},
		"Stop":       {{Matcher: "", Hooks: []hooks.HookConfig{{Command: "global-stop"}}}},
	}}
	project := &ConfigLayer{Hooks: hooks.HookSet{
		"PreToolUse": {{Matcher: "bash", Hooks: []hooks.HookConfig{{Command: "project-pre"}}}},
	}}

	cfg, err := ResolveConfig(&def, global, project)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	pre := cfg.Hooks["PreToolUse"]
	if len(pre) != 2 {
		t.Fatalf("PreToolUse matchers = %d, want 2 (global+project appended)", len(pre))
	}
	// Ascending layer order: global before project.
	if pre[0].Hooks[0].Command != "global-pre" || pre[1].Hooks[0].Command != "project-pre" {
		t.Errorf("PreToolUse order wrong: %q, %q", pre[0].Hooks[0].Command, pre[1].Hooks[0].Command)
	}
	if len(cfg.Hooks["Stop"]) != 1 {
		t.Errorf("Stop matchers = %d, want 1 (only global set it)", len(cfg.Hooks["Stop"]))
	}
}

// TestResolveConfigHooksNilWhenAbsent verifies the no-hooks path leaves
// cfg.Hooks nil (FR-18), so downstream can cheaply skip hook dispatch.
func TestResolveConfigHooksNilWhenAbsent(t *testing.T) {
	def := DefaultConfigLayer()
	cfg, err := ResolveConfig(&def)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.Hooks != nil {
		t.Errorf("Hooks = %v, want nil when no layer defines hooks", cfg.Hooks)
	}
	// An empty (but non-nil) hook map in a layer must not allocate cfg.Hooks.
	empty := &ConfigLayer{Hooks: hooks.HookSet{"PreToolUse": {}}}
	cfg2, err := ResolveConfig(&def, empty)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg2.Hooks != nil {
		t.Errorf("Hooks = %v, want nil when layer's event has no matchers", cfg2.Hooks)
	}
}

// TestLoadConfigLayerHooks verifies a layer's hooks decode from JSON, including
// per-hook timeout and matcher fields.
func TestLoadConfigLayerHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"model": "x/y",
		"hooks": {
			"PreToolUse": [
				{"matcher": "bash", "hooks": [{"type": "command", "command": "echo hi", "timeout": 5}]}
			]
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	layer, err := LoadConfigLayer(path)
	if err != nil {
		t.Fatalf("LoadConfigLayer: %v", err)
	}
	pre := layer.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "bash" {
		t.Fatalf("unexpected matchers: %+v", pre)
	}
	h := pre[0].Hooks[0]
	if h.Command != "echo hi" || h.TimeoutSeconds() != 5 {
		t.Errorf("unexpected hook: cmd=%q timeout=%d", h.Command, h.TimeoutSeconds())
	}
}
