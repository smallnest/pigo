// This file implements the layered configuration system (US-023, #42), the
// pigo port of pi/zero's config resolution. A resolved Config is produced by
// merging partial layers in precedence order:
//
//	default < global < project < environment/CLI
//
// Each layer is a *ConfigLayer whose fields are pointers, so "unset" (nil) is
// distinguishable from "set to the zero value" — only set fields override lower
// layers (field-level replacement, no deep merge). The final Config is
// validated: an unknown thinking level or tool-execution mode is a hard error,
// as is a malformed layer file.
package runtime

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/smallnest/pigo/internal/agentcore"
)

// Config is the fully resolved configuration a run operates under, after all
// layers are merged and validated.
type Config struct {
	// Model is the default model id used when a run does not specify one.
	Model string
	// Provider is the default provider name.
	Provider string
	// Credentials maps provider name → API key. Merged per-provider across
	// layers (a higher layer's key for provider X overrides a lower one, but
	// providers only present in a lower layer are retained).
	Credentials map[string]string
	// ToolExecutionMode is the default execution mode for tools that do not pin
	// their own mode.
	ToolExecutionMode agentcore.ToolExecutionMode
	// ThinkingLevel is the default reasoning-effort level.
	ThinkingLevel agentcore.ThinkingLevel
}

// ConfigLayer is one partial layer of configuration. Pointer/optional fields
// distinguish "not set in this layer" (nil/empty) from an explicit value, so a
// higher layer only overrides the fields it actually sets.
type ConfigLayer struct {
	Model             *string           `json:"model,omitempty"`
	Provider          *string           `json:"provider,omitempty"`
	Credentials       map[string]string `json:"credentials,omitempty"`
	ToolExecutionMode *string           `json:"toolExecutionMode,omitempty"`
	ThinkingLevel     *string           `json:"thinkingLevel,omitempty"`
}

// DefaultConfigLayer is the base layer applied before all others. It gives a
// usable configuration out of the box.
func DefaultConfigLayer() ConfigLayer {
	model := "openrouter/free"
	provider := "openrouter"
	mode := string(agentcore.ToolExecutionParallel)
	level := string(agentcore.ThinkingMedium)
	return ConfigLayer{
		Model:             &model,
		Provider:          &provider,
		ToolExecutionMode: &mode,
		ThinkingLevel:     &level,
	}
}

// LoadConfigLayer reads and decodes a single JSON config layer from path. A
// missing file yields a nil layer and no error (an absent layer is not a
// failure); a present-but-malformed file is a hard error.
func LoadConfigLayer(path string) (*ConfigLayer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var layer ConfigLayer
	if err := json.Unmarshal(data, &layer); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &layer, nil
}

// ResolveConfig merges the given layers in ascending precedence order (earlier
// layers are overridden by later ones) and validates the result. Nil layers are
// skipped, so callers can pass the output of LoadConfigLayer directly. The
// merge is field-level: a later layer only overrides fields it sets, except
// Credentials, which merges per-provider.
func ResolveConfig(layers ...*ConfigLayer) (Config, error) {
	var cfg Config
	for _, layer := range layers {
		if layer == nil {
			continue
		}
		if layer.Model != nil {
			cfg.Model = *layer.Model
		}
		if layer.Provider != nil {
			cfg.Provider = *layer.Provider
		}
		if layer.ToolExecutionMode != nil {
			cfg.ToolExecutionMode = agentcore.ToolExecutionMode(*layer.ToolExecutionMode)
		}
		if layer.ThinkingLevel != nil {
			cfg.ThinkingLevel = agentcore.ThinkingLevel(*layer.ThinkingLevel)
		}
		for provider, key := range layer.Credentials {
			if cfg.Credentials == nil {
				cfg.Credentials = make(map[string]string)
			}
			cfg.Credentials[provider] = key
		}
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// EnvConfigLayer builds a config layer from environment variables, the highest
// file-independent layer (below only explicit CLI flags). Recognized:
//
//	PIGO_MODEL, PIGO_PROVIDER, PIGO_TOOL_EXECUTION_MODE, PIGO_THINKING_LEVEL
//
// Only set variables contribute; unset ones leave the field nil so lower layers
// show through. Credential env vars are intentionally NOT captured here — keys
// are resolved lazily by the CredentialStore and never merged into a struct
// that might be logged (US-012).
func EnvConfigLayer(getenv func(string) string) ConfigLayer {
	if getenv == nil {
		getenv = os.Getenv
	}
	var layer ConfigLayer
	if v := getenv("PIGO_MODEL"); v != "" {
		layer.Model = &v
	}
	if v := getenv("PIGO_PROVIDER"); v != "" {
		layer.Provider = &v
	}
	if v := getenv("PIGO_TOOL_EXECUTION_MODE"); v != "" {
		layer.ToolExecutionMode = &v
	}
	if v := getenv("PIGO_THINKING_LEVEL"); v != "" {
		layer.ThinkingLevel = &v
	}
	return layer
}

// validate reports the first invalid field in the resolved config: an unknown
// tool-execution mode or thinking level. An empty model is also rejected, since
// a run cannot proceed without one.
func (c Config) validate() error {
	if c.Model == "" {
		return fmt.Errorf("config: model must not be empty")
	}
	switch c.ToolExecutionMode {
	case agentcore.ToolExecutionParallel, agentcore.ToolExecutionSequential:
	default:
		return fmt.Errorf("config: invalid toolExecutionMode %q (want parallel|sequential)", c.ToolExecutionMode)
	}
	switch c.ThinkingLevel {
	case agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow, agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh:
	default:
		return fmt.Errorf("config: invalid thinkingLevel %q", c.ThinkingLevel)
	}
	return nil
}
