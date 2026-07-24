// This file implements the /btw model/thinking override config (US-005, #282):
// an optional per-command config that lets a side thread use a different model
// and/or reasoning effort than the main session, without touching the main
// session's settings (对标 pi-btw's pi-btw.json).
//
// The config lives at $PIGO_HOME/btw.json (or ~/.pigo/btw.json). It is read
// fresh on every /btw invocation, so editing it takes effect on the next call
// with no restart. A missing file, an empty object, or an absent field all mean
// "inherit the session default" silently — only a malformed file or an
// unusable model override produces a (non-fatal) warning.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// btwConfig is the on-disk shape of ~/.pigo/btw.json. Both fields are optional;
// an absent field (nil / empty) inherits the session default. Pointers/empty
// strings distinguish "not set" from a real value so a partial file still falls
// back per-field.
type btwConfig struct {
	Model         string `json:"model,omitempty"`
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

// btwRunSettings is the resolved model/provider/thinking a side run uses. It is
// computed once per /btw invocation from the session defaults overlaid with
// btw.json, and passed down to askSide so every turn of that invocation uses
// the same settings.
type btwRunSettings struct {
	model         string
	providerName  string
	provider      provider.Provider
	thinkingLevel agentcore.ThinkingLevel
}

// btwConfigPath returns the path to the /btw override config, or "" when the
// config directory cannot be resolved (then the config is treated as absent).
func btwConfigPath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "btw.json")
}

// loadBtwConfig reads and parses btw.json. A missing file returns a zero config
// with no error (inherit everything). A malformed file returns an error so the
// caller can warn and fall back. An empty object parses to a zero config.
func loadBtwConfig(path string) (btwConfig, error) {
	if path == "" {
		return btwConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return btwConfig{}, nil
		}
		return btwConfig{}, err
	}
	var cfg btwConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return btwConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// resolveBtwSettings computes the model/provider/thinking a side run should use.
// It starts from the session defaults (deps.live) and overlays btw.json:
//
//   - No config / empty object / absent fields → inherit the session values.
//   - thinkingLevel set → validate and override (invalid value warns, falls back).
//   - model set → resolve its provider (reusing resolveProvider like /model);
//     if the model cannot be resolved/authenticated, warn on one line and fall
//     back to the session model+provider.
//
// A malformed config file warns once and inherits everything. Nothing here
// mutates deps.live, so the override is confined to the side thread (FR-8).
func resolveBtwSettings(out io.Writer, deps *replDeps) btwRunSettings {
	s := btwRunSettings{
		model:         deps.live.model,
		providerName:  deps.live.providerName,
		provider:      deps.live.provider,
		thinkingLevel: deps.live.thinkingLevel,
	}

	cfg, err := loadBtwConfig(btwConfigPath())
	if err != nil {
		fmt.Fprintf(out, "%s\n", colorize(colorEnabled(), ansiDim, "btw: ignoring invalid btw.json: "+err.Error()))
		return s
	}

	if lvl := strings.TrimSpace(cfg.ThinkingLevel); lvl != "" {
		if v, ok := validThinkingLevel(lvl); ok {
			s.thinkingLevel = v
		} else {
			fmt.Fprintf(out, "%s\n", colorize(colorEnabled(), ansiDim, fmt.Sprintf("btw: ignoring invalid thinkingLevel %q, using %q", lvl, s.thinkingLevel)))
		}
	}

	if model := strings.TrimSpace(cfg.Model); model != "" && model != s.model {
		prov, providerName, perr := resolveProvider(model, deps.live.baseURL, deps.live.protocol, "")
		if perr != nil {
			fmt.Fprintf(out, "%s\n", colorize(colorEnabled(), ansiDim, fmt.Sprintf("btw: cannot use model %q (%v), falling back to %q", model, perr, s.model)))
		} else {
			s.model = model
			s.providerName = providerName
			s.provider = prov
		}
	}

	return s
}

// validThinkingLevel reports whether s is one of the known reasoning-effort
// levels and returns the typed value. It mirrors the enum in agentcore so an
// invalid btw.json value can be rejected without importing the config layer.
func validThinkingLevel(s string) (agentcore.ThinkingLevel, bool) {
	switch agentcore.ThinkingLevel(s) {
	case agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh:
		return agentcore.ThinkingLevel(s), true
	default:
		return "", false
	}
}
