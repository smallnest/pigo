// Tests for the Anthropic-Messages-protocol built-in providers (US-006, node
// #187): anthropic, minimax, minimax-cn. They assert the registry metadata
// (base_url, protocol, key env var) and that the constructed anthropic-compat
// driver attaches the auth header dictated by the provider's AuthScheme.
//
// No real network calls are made: the auth header is exercised by invoking the
// driver's authHeader func against a dummy *http.Request and inspecting the
// resulting headers (the same package can reach the unexported driver fields).
package provider

import (
	"net/http"
	"testing"
)

// TestAnthropicProtocolProviderRegistrySpecs asserts the registry metadata for
// each Anthropic-Messages-protocol built-in: default base URL, wire protocol,
// and the key env var (via envAPIKey + t.Setenv).
func TestAnthropicProtocolProviderRegistrySpecs(t *testing.T) {
	cases := []struct {
		name        string
		wantBaseURL string
		wantEnv     string
	}{
		{"anthropic", "https://api.anthropic.com/v1", "ANTHROPIC_API_KEY"},
		{"minimax", "https://api.minimax.io/anthropic", "MINIMAX_API_KEY"},
		{"minimax-cn", "https://api.minimaxi.com/anthropic", "MINIMAX_CN_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := LookupProviderSpec(tc.name)
			if !ok {
				t.Fatalf("provider %q not found in registry", tc.name)
			}
			if spec.Protocol != ProtocolAnthropic {
				t.Errorf("protocol = %q, want %q", spec.Protocol, ProtocolAnthropic)
			}
			if spec.DefaultBaseURL != tc.wantBaseURL {
				t.Errorf("base_url = %q, want %q", spec.DefaultBaseURL, tc.wantBaseURL)
			}
			// Key resolution: the provider's key comes from tc.wantEnv.
			t.Setenv(tc.wantEnv, "SEKRET-"+tc.name)
			if got := envAPIKey(tc.name); got != "SEKRET-"+tc.name {
				t.Errorf("envAPIKey(%q) = %q, want key resolved from %s", tc.name, got, tc.wantEnv)
			}
		})
	}
}

// TestAnthropicProtocolProviderAuthHeader verifies that the driver built for
// each provider (the way resolveNamedProvider builds it: name + resolved
// base_url + spec.AuthScheme) targets the provider's base URL and sets the auth
// header matching its AuthScheme. anthropic/minimax/minimax-cn are all
// x-api-key + anthropic-version per pi's convention.
func TestAnthropicProtocolProviderAuthHeader(t *testing.T) {
	for _, name := range []string{"anthropic", "minimax", "minimax-cn"} {
		t.Run(name, func(t *testing.T) {
			spec, ok := LookupProviderSpec(name)
			if !ok {
				t.Fatalf("provider %q not found in registry", name)
			}
			p := NewAnthropicProtocolProvider(spec.Name, spec.DefaultBaseURL, spec.AuthScheme, nil)
			d, ok := p.(*anthropicCompatDriver)
			if !ok {
				t.Fatalf("provider %q is not an *anthropicCompatDriver", name)
			}
			if d.baseURL != spec.DefaultBaseURL {
				t.Errorf("baseURL = %q, want %q", d.baseURL, spec.DefaultBaseURL)
			}
			req, err := http.NewRequest(http.MethodPost, d.baseURL+"/messages", nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			d.authHeader(req, "SEKRET")
			assertAuthHeaderForScheme(t, req, spec.AuthScheme)
			if got := req.Header.Get("Authorization"); got != "" && spec.AuthScheme != AuthBearer {
				t.Errorf("unexpected Authorization header %q for x-api-key scheme", got)
			}
		})
	}
}

// TestAnthropicAuthSchemeSelection proves the auth-header mechanism itself:
// AuthBearer yields Authorization: Bearer, AuthXAPIKey yields x-api-key plus the
// anthropic-version header. This guarantees an anthropic-protocol provider can
// select either header shape from its registry AuthScheme.
func TestAnthropicAuthSchemeSelection(t *testing.T) {
	t.Run("bearer", func(t *testing.T) {
		p := NewAnthropicProtocolProvider("gw", "https://example.test/anthropic", AuthBearer, nil)
		req := newDummyReq(t)
		p.(*anthropicCompatDriver).authHeader(req, "SEKRET")
		if got := req.Header.Get("Authorization"); got != "Bearer SEKRET" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer SEKRET")
		}
		if got := req.Header.Get("x-api-key"); got != "" {
			t.Errorf("x-api-key = %q, want empty for bearer scheme", got)
		}
	})
	t.Run("x-api-key", func(t *testing.T) {
		p := NewAnthropicProtocolProvider("anthropic", "", AuthXAPIKey, nil)
		req := newDummyReq(t)
		p.(*anthropicCompatDriver).authHeader(req, "SEKRET")
		if got := req.Header.Get("x-api-key"); got != "SEKRET" {
			t.Errorf("x-api-key = %q, want %q", got, "SEKRET")
		}
		if got := req.Header.Get("anthropic-version"); got != anthropicAPIVersion {
			t.Errorf("anthropic-version = %q, want %q", got, anthropicAPIVersion)
		}
	})
	// A non-crashing fallback for unwired schemes (e.g. Bedrock's AuthAWS): must
	// not panic and defaults to the x-api-key convention.
	t.Run("fallback", func(t *testing.T) {
		p := NewAnthropicProtocolProvider("bedrock", "", AuthAWS, nil)
		req := newDummyReq(t)
		p.(*anthropicCompatDriver).authHeader(req, "SEKRET")
		if got := req.Header.Get("x-api-key"); got != "SEKRET" {
			t.Errorf("fallback x-api-key = %q, want %q", got, "SEKRET")
		}
	})
}

// TestNewAnthropicProviderUnchanged guards against regressing the direct
// Anthropic constructor: it must still default to the public endpoint and use
// x-api-key + anthropic-version.
func TestNewAnthropicProviderUnchanged(t *testing.T) {
	d, ok := NewAnthropicProvider("", nil).(*anthropicCompatDriver)
	if !ok {
		t.Fatal("NewAnthropicProvider did not return *anthropicCompatDriver")
	}
	if d.baseURL != anthropicBaseURL {
		t.Errorf("baseURL = %q, want %q", d.baseURL, anthropicBaseURL)
	}
	req := newDummyReq(t)
	d.authHeader(req, "SEKRET")
	if got := req.Header.Get("x-api-key"); got != "SEKRET" {
		t.Errorf("x-api-key = %q, want %q", got, "SEKRET")
	}
	if got := req.Header.Get("anthropic-version"); got != anthropicAPIVersion {
		t.Errorf("anthropic-version = %q, want %q", got, anthropicAPIVersion)
	}
}

func newDummyReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://example.test/messages", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

func assertAuthHeaderForScheme(t *testing.T, req *http.Request, scheme string) {
	t.Helper()
	if scheme == AuthBearer {
		if got := req.Header.Get("Authorization"); got != "Bearer SEKRET" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer SEKRET")
		}
		return
	}
	if got := req.Header.Get("x-api-key"); got != "SEKRET" {
		t.Errorf("x-api-key = %q, want %q", got, "SEKRET")
	}
	if got := req.Header.Get("anthropic-version"); got != anthropicAPIVersion {
		t.Errorf("anthropic-version = %q, want %q", got, anthropicAPIVersion)
	}
}
