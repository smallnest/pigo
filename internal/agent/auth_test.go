package agent

import (
	"context"
	"testing"
	"time"
)

func TestEnvAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env")
	if got := envAPIKey("anthropic"); got != "sk-ant-env" {
		t.Errorf("env key = %q, want sk-ant-env", got)
	}
	// Unknown provider uses generic <PROVIDER>_API_KEY fallback.
	t.Setenv("FOOBAR_API_KEY", "sk-foobar")
	if got := envAPIKey("foobar"); got != "sk-foobar" {
		t.Errorf("generic env key = %q, want sk-foobar", got)
	}
	if got := envAPIKey("nonesuch"); got != "" {
		t.Errorf("missing env key = %q, want empty", got)
	}
}

func TestLoadAPIKeyConfig(t *testing.T) {
	cfg, err := LoadAPIKeyConfig([]byte(`{"keys":{"openai":"sk-openai-cfg"}}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Keys["openai"] != "sk-openai-cfg" {
		t.Errorf("config key = %q", cfg.Keys["openai"])
	}
	if _, err := LoadAPIKeyConfig([]byte(`not json`)); err == nil {
		t.Fatal("bad JSON must error")
	}
}

func TestLoadAPIKeyConfigFileMissing(t *testing.T) {
	cfg, err := LoadAPIKeyConfigFile("/no/such/path/keys.json")
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(cfg.Keys) != 0 {
		t.Errorf("missing file must yield empty keys, got %v", cfg.Keys)
	}
}

// TestCredentialStoreResolutionOrder verifies OAuth > env > config precedence.
func TestCredentialStoreResolutionOrder(t *testing.T) {
	cfg, _ := LoadAPIKeyConfig([]byte(`{"keys":{"anthropic":"sk-cfg","openai":"sk-openai-cfg"}}`))
	store := NewCredentialStore(cfg)

	// Neutralize any ambient keys so config-only resolution is deterministic.
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_API_KEY", "")

	// Config-only provider resolves from config.
	if got := store.GetAPIKey(context.Background(), "openai"); got != "sk-openai-cfg" {
		t.Errorf("openai (config) = %q, want sk-openai-cfg", got)
	}

	// Env overrides config.
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	if got := store.GetAPIKey(context.Background(), "anthropic"); got != "sk-env" {
		t.Errorf("anthropic (env>config) = %q, want sk-env", got)
	}

	// OAuth overrides env + config.
	store.RegisterOAuth("anthropic", NewTokenSource(
		OAuthToken{AccessToken: "oauth-token", Expiry: time.Now().Add(time.Hour)}, nil))
	if got := store.GetAPIKey(context.Background(), "anthropic"); got != "oauth-token" {
		t.Errorf("anthropic (oauth>env) = %q, want oauth-token", got)
	}

	// Unknown provider → empty.
	if got := store.GetAPIKey(context.Background(), "ghost"); got != "" {
		t.Errorf("ghost = %q, want empty", got)
	}
}

// TestTokenSourceRefresh verifies an expired token triggers a refresh returning
// a new token.
func TestTokenSourceRefresh(t *testing.T) {
	now := time.Now()
	refreshCount := 0
	src := NewTokenSource(
		OAuthToken{AccessToken: "old", RefreshToken: "refresh-1", Expiry: now.Add(-time.Minute)},
		func(ctx context.Context, rt string) (OAuthToken, error) {
			refreshCount++
			if rt != "refresh-1" {
				t.Errorf("refresh token = %q, want refresh-1", rt)
			}
			return OAuthToken{AccessToken: "new", RefreshToken: "refresh-2", Expiry: now.Add(time.Hour)}, nil
		},
	)
	src.Now = func() time.Time { return now }

	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok != "new" {
		t.Errorf("token = %q, want new (refreshed)", tok)
	}
	if refreshCount != 1 {
		t.Errorf("refresh count = %d, want 1", refreshCount)
	}

	// Second call within validity does not refresh again.
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("token 2: %v", err)
	}
	if refreshCount != 1 {
		t.Errorf("refresh count after valid reuse = %d, want 1", refreshCount)
	}
}

func TestTokenSourceNoRefreshFunc(t *testing.T) {
	now := time.Now()
	// Expired token with no Refresh func → error.
	src := NewTokenSource(OAuthToken{AccessToken: "old", Expiry: now.Add(-time.Minute)}, nil)
	src.Now = func() time.Time { return now }
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("expired token without refresh must error")
	}

	// Static token (zero expiry) never expires.
	static := NewTokenSource(OAuthToken{AccessToken: "static"}, nil)
	tok, err := static.Token(context.Background())
	if err != nil || tok != "static" {
		t.Errorf("static token = %q, err = %v", tok, err)
	}
}

func TestTokenSourceRefreshError(t *testing.T) {
	now := time.Now()
	src := NewTokenSource(
		OAuthToken{AccessToken: "old", Expiry: now.Add(-time.Minute)},
		func(ctx context.Context, rt string) (OAuthToken, error) {
			return OAuthToken{}, context.DeadlineExceeded
		},
	)
	src.Now = func() time.Time { return now }
	_, err := src.Token(context.Background())
	if err == nil {
		t.Fatal("refresh error must propagate")
	}
	// Error must not leak the (empty) token but should mention refresh.
	if got := err.Error(); got == "" {
		t.Error("expected non-empty error")
	}
}

// TestCredentialStoreOAuthRefreshFallback verifies a failing OAuth refresh
// falls back to env/config rather than returning empty when a static key exists.
func TestCredentialStoreOAuthRefreshFallback(t *testing.T) {
	cfg, _ := LoadAPIKeyConfig([]byte(`{"keys":{"anthropic":"sk-cfg-fallback"}}`))
	store := NewCredentialStore(cfg)
	now := time.Now()
	src := NewTokenSource(
		OAuthToken{AccessToken: "old", Expiry: now.Add(-time.Minute)},
		func(ctx context.Context, rt string) (OAuthToken, error) {
			return OAuthToken{}, context.DeadlineExceeded
		},
	)
	src.Now = func() time.Time { return now }
	store.RegisterOAuth("anthropic", src)

	if got := store.GetAPIKey(context.Background(), "anthropic"); got != "sk-cfg-fallback" {
		t.Errorf("refresh-failed fallback = %q, want sk-cfg-fallback", got)
	}
}
