// This file implements credential resolution (US-012): API key lookup from
// environment variables and a config file (per provider), plus an OAuth token
// source that refreshes short-lived tokens on expiry. The resolver satisfies
// the LoopConfig.GetAPIKey shape (func(ctx, provider) string) so the agent loop
// can obtain a fresh key per request.
//
// Security (FR: 敏感值不写入日志): secret values are never logged or embedded in
// error messages. Errors and String()/redaction helpers reference credentials
// by key name / provider only.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// providerEnvVars maps a provider name to the environment variables checked (in
// order) for its API key. The first non-empty value wins.
var providerEnvVars = map[string][]string{
	"anthropic":  {"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
	"openai":     {"OPENAI_API_KEY"},
	"google":     {"GOOGLE_API_KEY", "GEMINI_API_KEY"},
	"deepseek":   {"DEEPSEEK_API_KEY"},
	"xai":        {"XAI_API_KEY"},
	"groq":       {"GROQ_API_KEY"},
	"openrouter": {"OPENROUTER_API_KEY"},
	"nvidia":     {"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"},
	"mistral":    {"MISTRAL_API_KEY"},
}

// APIKeyConfig is the on-disk config-file shape: a map of provider name to API
// key. It is parsed from JSON and holds only static keys (OAuth lives in
// TokenSource). Values are secrets and must not be logged.
type APIKeyConfig struct {
	// Keys maps provider name → API key.
	Keys map[string]string `json:"keys"`
}

// LoadAPIKeyConfig parses an APIKeyConfig from JSON bytes (e.g. a config file).
func LoadAPIKeyConfig(data []byte) (*APIKeyConfig, error) {
	var cfg APIKeyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("auth: parse api key config: %w", err)
	}
	if cfg.Keys == nil {
		cfg.Keys = make(map[string]string)
	}
	return &cfg, nil
}

// LoadAPIKeyConfigFile reads and parses an APIKeyConfig from a file path. A
// missing file is not an error — it returns an empty config so env/OAuth can
// still resolve keys.
func LoadAPIKeyConfigFile(path string) (*APIKeyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &APIKeyConfig{Keys: make(map[string]string)}, nil
		}
		return nil, fmt.Errorf("auth: read api key config %q: %w", path, err)
	}
	return LoadAPIKeyConfig(data)
}

// envAPIKey returns the API key for a provider from the environment, checking
// the provider's known variable names in order, then a generic
// <PROVIDER>_API_KEY fallback. Returns "" when none is set.
func envAPIKey(provider string) string {
	for _, name := range providerEnvVars[provider] {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	// Generic fallback for providers not in the table.
	generic := strings.ToUpper(provider) + "_API_KEY"
	return os.Getenv(generic)
}

// TokenSource yields an access token, refreshing it when expired. It models an
// OAuth credential whose access token is short-lived (FR-15: getApiKey refreshes
// on expiry). It is safe for concurrent use.
type TokenSource struct {
	mu           sync.Mutex
	accessToken  string
	expiry       time.Time
	refreshToken string
	// Refresh exchanges the current refresh token for a new access token. It
	// returns the new access token, its expiry, and (optionally) a rotated
	// refresh token. Required for a TokenSource to refresh; nil means the token
	// is static and never refreshed.
	Refresh func(ctx context.Context, refreshToken string) (OAuthToken, error)
	// Now is injectable for testing; defaults to time.Now.
	Now func() time.Time
	// Leeway refreshes the token this long before its actual expiry to avoid
	// racing the boundary. Defaults to 30s.
	Leeway time.Duration
}

// OAuthToken is the result of an OAuth exchange/refresh. Values are secrets.
type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// NewTokenSource builds a TokenSource seeded with an initial token and a refresh
// function. refresh may be nil for a static (never-expiring) token.
func NewTokenSource(initial OAuthToken, refresh func(ctx context.Context, refreshToken string) (OAuthToken, error)) *TokenSource {
	return &TokenSource{
		accessToken:  initial.AccessToken,
		expiry:       initial.Expiry,
		refreshToken: initial.RefreshToken,
		Refresh:      refresh,
	}
}

func (t *TokenSource) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *TokenSource) leeway() time.Duration {
	if t.Leeway > 0 {
		return t.Leeway
	}
	return 30 * time.Second
}

// expired reports whether the access token is missing or within leeway of its
// expiry. A zero expiry means "never expires" (static token).
func (t *TokenSource) expired() bool {
	if t.accessToken == "" {
		return true
	}
	if t.expiry.IsZero() {
		return false
	}
	return !t.now().Before(t.expiry.Add(-t.leeway()))
}

// Token returns a valid access token, refreshing it when expired. It errors if
// a refresh is needed but no Refresh func is set, or if Refresh fails. The
// returned error never contains the token value.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.expired() {
		return t.accessToken, nil
	}
	if t.Refresh == nil {
		return "", fmt.Errorf("auth: token expired and no refresh function configured")
	}
	tok, err := t.Refresh(ctx, t.refreshToken)
	if err != nil {
		return "", fmt.Errorf("auth: token refresh failed: %w", err)
	}
	t.accessToken = tok.AccessToken
	t.expiry = tok.Expiry
	if tok.RefreshToken != "" {
		t.refreshToken = tok.RefreshToken
	}
	return t.accessToken, nil
}

// CredentialStore resolves API keys per provider from three layers, in order:
// OAuth token source (if registered), environment variable, config file. It
// implements the LoopConfig.GetAPIKey shape via GetAPIKey.
//
// It is safe for concurrent use.
type CredentialStore struct {
	mu        sync.RWMutex
	config    *APIKeyConfig
	sources   map[string]*TokenSource // provider → OAuth token source
	overrides map[string]string       // provider → explicit key (highest static priority)
}

// NewCredentialStore builds a store over an optional config file. A nil config
// is treated as empty.
func NewCredentialStore(config *APIKeyConfig) *CredentialStore {
	if config == nil {
		config = &APIKeyConfig{Keys: make(map[string]string)}
	}
	return &CredentialStore{
		config:    config,
		sources:   make(map[string]*TokenSource),
		overrides: make(map[string]string),
	}
}

// SetOverride records an explicit API key for a provider that wins over the
// environment variable and config file (but not a live OAuth token, which is
// auto-refreshed). It is the seam for a CLI --api-key flag: an empty key is
// ignored so a bare flag does not clobber env/config resolution.
func (c *CredentialStore) SetOverride(provider, key string) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.overrides[provider] = key
}

// RegisterOAuth registers an OAuth TokenSource for a provider. Once registered,
// GetAPIKey prefers the (auto-refreshing) OAuth token over static keys.
func (c *CredentialStore) RegisterOAuth(provider string, src *TokenSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sources[provider] = src
}

// GetAPIKey resolves the API key for a provider. Resolution order: OAuth token
// (refreshed on expiry) → explicit override (--api-key) → environment variable
// → config file. Returns "" when no credential is available. This matches
// LoopConfig.GetAPIKey so it can be assigned directly.
//
// On OAuth refresh failure it falls back to override/env/config rather than
// returning a secret-bearing error; the empty return lets the caller fall back
// to a static key. It never logs secret values.
func (c *CredentialStore) GetAPIKey(ctx context.Context, provider string) string {
	c.mu.RLock()
	src := c.sources[provider]
	override := c.overrides[provider]
	cfgKey := ""
	if c.config != nil {
		cfgKey = c.config.Keys[provider]
	}
	c.mu.RUnlock()

	if src != nil {
		if tok, err := src.Token(ctx); err == nil && tok != "" {
			return tok
		}
		// Refresh failed → fall through to static layers.
	}
	if override != "" {
		return override
	}
	if env := envAPIKey(provider); env != "" {
		return env
	}
	return cfgKey
}

// HasCredential reports whether any credential (OAuth/env/config) is available
// for a provider, without exposing the value.
func (c *CredentialStore) HasCredential(ctx context.Context, provider string) bool {
	return c.GetAPIKey(ctx, provider) != ""
}
