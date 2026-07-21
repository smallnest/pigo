package provider

import (
	"strings"
	"testing"
)

// envFrom builds an env(string)string lookup from a map for hermetic tests: no
// process environment is read, so tests never depend on ambient state and make
// no network requests.
func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// baseURLOf extracts the composed base URL from a constructed driver by type
// asserting the two concrete driver shapes (same-package access to unexported
// fields). It fails the test if the provider is neither shape.
func baseURLOf(t *testing.T, p Provider) string {
	t.Helper()
	switch d := p.(type) {
	case *openAICompatDriver:
		return d.baseURL
	case *anthropicCompatDriver:
		return d.baseURL
	default:
		t.Fatalf("unexpected provider type %T", p)
		return ""
	}
}

func specFor(t *testing.T, name string) ProviderSpec {
	t.Helper()
	spec, ok := LookupProviderSpec(name)
	if !ok {
		t.Fatalf("registry missing provider %q", name)
	}
	return spec
}

func TestResolveSpecialProvider_Azure(t *testing.T) {
	spec := specFor(t, "azure-openai-responses")

	// Missing API key.
	if _, err := ResolveSpecialProvider(spec, "gpt-4o", "", envFrom(nil)); err == nil ||
		!strings.Contains(err.Error(), "AZURE_OPENAI_API_KEY") {
		t.Fatalf("expected AZURE_OPENAI_API_KEY error, got %v", err)
	}

	// Key present but no endpoint origin.
	env := envFrom(map[string]string{"AZURE_OPENAI_API_KEY": "k"})
	if _, err := ResolveSpecialProvider(spec, "gpt-4o", "", env); err == nil ||
		!strings.Contains(err.Error(), "AZURE_OPENAI_BASE_URL") ||
		!strings.Contains(err.Error(), "AZURE_OPENAI_RESOURCE_NAME") {
		t.Fatalf("expected endpoint-config error naming both env vars, got %v", err)
	}

	// Resource name → composed origin, default api version v1.
	env = envFrom(map[string]string{
		"AZURE_OPENAI_API_KEY":       "k",
		"AZURE_OPENAI_RESOURCE_NAME": "myres",
	})
	p, err := ResolveSpecialProvider(spec, "gpt-4o", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := baseURLOf(t, p), "https://myres.openai.azure.com/openai/v1"; got != want {
		t.Fatalf("azure base_url = %q, want %q", got, want)
	}

	// Explicit base URL env + custom api version.
	env = envFrom(map[string]string{
		"AZURE_OPENAI_API_KEY":     "k",
		"AZURE_OPENAI_BASE_URL":    "https://custom.example.com",
		"AZURE_OPENAI_API_VERSION": "2024-10-01",
	})
	p, err = ResolveSpecialProvider(spec, "gpt-4o", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := baseURLOf(t, p), "https://custom.example.com/openai/2024-10-01"; got != want {
		t.Fatalf("azure base_url = %q, want %q", got, want)
	}

	// Deployment name map → deployment-scoped path.
	env = envFrom(map[string]string{
		"AZURE_OPENAI_API_KEY":             "k",
		"AZURE_OPENAI_RESOURCE_NAME":       "myres",
		"AZURE_OPENAI_DEPLOYMENT_NAME_MAP": "gpt-4o=prod-4o , other=x",
	})
	p, err = ResolveSpecialProvider(spec, "gpt-4o", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := baseURLOf(t, p), "https://myres.openai.azure.com/openai/deployments/prod-4o"; got != want {
		t.Fatalf("azure deployment base_url = %q, want %q", got, want)
	}

	// --base-url flag wins over env origin.
	p, err = ResolveSpecialProvider(spec, "gpt-4o", "https://flag.example.com", envFrom(map[string]string{"AZURE_OPENAI_API_KEY": "k"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := baseURLOf(t, p), "https://flag.example.com/openai/v1"; got != want {
		t.Fatalf("azure flag base_url = %q, want %q", got, want)
	}
}

func TestParseDeploymentMap(t *testing.T) {
	m := parseDeploymentMap(" a=1, b = 2 ,,bad,c=,=d ")
	if m["a"] != "1" || m["b"] != "2" {
		t.Fatalf("parseDeploymentMap = %v, want a=1 b=2", m)
	}
	if _, ok := m["bad"]; ok {
		t.Fatalf("expected 'bad' (no '=') to be skipped: %v", m)
	}
	if _, ok := m["c"]; ok {
		t.Fatalf("expected 'c=' (empty value) to be skipped: %v", m)
	}
}

func TestResolveSpecialProvider_Bedrock(t *testing.T) {
	spec := specFor(t, "amazon-bedrock")

	// No credentials at all → names the bearer token.
	if _, err := ResolveSpecialProvider(spec, "claude", "", envFrom(nil)); err == nil ||
		!strings.Contains(err.Error(), "AWS_BEARER_TOKEN_BEDROCK") {
		t.Fatalf("expected AWS_BEARER_TOKEN_BEDROCK error, got %v", err)
	}

	// Only profile present → SigV4-unsupported error, still names bearer token.
	env := envFrom(map[string]string{"AWS_PROFILE": "default"})
	if _, err := ResolveSpecialProvider(spec, "claude", "", env); err == nil ||
		!strings.Contains(err.Error(), "SigV4") ||
		!strings.Contains(err.Error(), "AWS_BEARER_TOKEN_BEDROCK") {
		t.Fatalf("expected SigV4-unsupported error naming bearer token, got %v", err)
	}

	// Only static keys present → SigV4-unsupported error.
	env = envFrom(map[string]string{"AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "secret"})
	if _, err := ResolveSpecialProvider(spec, "claude", "", env); err == nil ||
		!strings.Contains(err.Error(), "SigV4") {
		t.Fatalf("expected SigV4-unsupported error for static keys, got %v", err)
	}

	// Bearer token + default region.
	env = envFrom(map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "tok"})
	p, err := ResolveSpecialProvider(spec, "claude", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := baseURLOf(t, p), "https://bedrock-runtime.us-east-1.amazonaws.com"; got != want {
		t.Fatalf("bedrock base_url = %q, want %q", got, want)
	}

	// Bearer token + explicit region.
	env = envFrom(map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "tok", "AWS_REGION": "eu-west-1"})
	p, err = ResolveSpecialProvider(spec, "claude", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := baseURLOf(t, p), "https://bedrock-runtime.eu-west-1.amazonaws.com"; got != want {
		t.Fatalf("bedrock base_url = %q, want %q", got, want)
	}
}

func TestResolveSpecialProvider_GoogleVertex(t *testing.T) {
	spec := specFor(t, "google-vertex")

	if _, err := ResolveSpecialProvider(spec, "gemini", "", envFrom(nil)); err == nil ||
		!strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("expected GOOGLE_CLOUD_PROJECT error, got %v", err)
	}

	env := envFrom(map[string]string{"GOOGLE_CLOUD_PROJECT": "proj"})
	if _, err := ResolveSpecialProvider(spec, "gemini", "", env); err == nil ||
		!strings.Contains(err.Error(), "GOOGLE_CLOUD_LOCATION") {
		t.Fatalf("expected GOOGLE_CLOUD_LOCATION error, got %v", err)
	}

	env = envFrom(map[string]string{"GOOGLE_CLOUD_PROJECT": "proj", "GOOGLE_CLOUD_LOCATION": "us-central1"})
	if _, err := ResolveSpecialProvider(spec, "gemini", "", env); err == nil ||
		!strings.Contains(err.Error(), "GOOGLE_CLOUD_API_KEY") ||
		!strings.Contains(err.Error(), "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Fatalf("expected credentials error naming both sources, got %v", err)
	}

	// Fully configured with API key.
	env = envFrom(map[string]string{
		"GOOGLE_CLOUD_PROJECT":  "proj",
		"GOOGLE_CLOUD_LOCATION": "us-central1",
		"GOOGLE_CLOUD_API_KEY":  "k",
	})
	p, err := ResolveSpecialProvider(spec, "gemini", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := baseURLOf(t, p), "https://us-central1-aiplatform.googleapis.com"; got != want {
		t.Fatalf("vertex base_url = %q, want %q", got, want)
	}

	// ADC credential source also satisfies.
	env = envFrom(map[string]string{
		"GOOGLE_CLOUD_PROJECT":           "proj",
		"GOOGLE_CLOUD_LOCATION":          "europe-west4",
		"GOOGLE_APPLICATION_CREDENTIALS": "/path/to/adc.json",
	})
	p, err = ResolveSpecialProvider(spec, "gemini", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := baseURLOf(t, p), "https://europe-west4-aiplatform.googleapis.com"; got != want {
		t.Fatalf("vertex ADC base_url = %q, want %q", got, want)
	}
}

func TestResolveSpecialProvider_CloudflareWorkersAI(t *testing.T) {
	spec := specFor(t, "cloudflare-workers-ai")

	if _, err := ResolveSpecialProvider(spec, "m", "", envFrom(nil)); err == nil ||
		!strings.Contains(err.Error(), "CLOUDFLARE_API_KEY") {
		t.Fatalf("expected CLOUDFLARE_API_KEY error, got %v", err)
	}

	env := envFrom(map[string]string{"CLOUDFLARE_API_KEY": "k"})
	if _, err := ResolveSpecialProvider(spec, "m", "", env); err == nil ||
		!strings.Contains(err.Error(), "CLOUDFLARE_ACCOUNT_ID") {
		t.Fatalf("expected CLOUDFLARE_ACCOUNT_ID error, got %v", err)
	}

	env = envFrom(map[string]string{"CLOUDFLARE_API_KEY": "k", "CLOUDFLARE_ACCOUNT_ID": "acct123"})
	p, err := ResolveSpecialProvider(spec, "m", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://api.cloudflare.com/client/v4/accounts/acct123/ai/v1"
	if got := baseURLOf(t, p); got != want {
		t.Fatalf("workers-ai base_url = %q, want %q", got, want)
	}
	if _, ok := p.(*openAICompatDriver); !ok {
		t.Fatalf("workers-ai should speak OpenAI wire, got %T", p)
	}
}

func TestResolveSpecialProvider_CloudflareAIGateway(t *testing.T) {
	spec := specFor(t, "cloudflare-ai-gateway")

	if _, err := ResolveSpecialProvider(spec, "m", "", envFrom(nil)); err == nil ||
		!strings.Contains(err.Error(), "CLOUDFLARE_API_KEY") {
		t.Fatalf("expected CLOUDFLARE_API_KEY error, got %v", err)
	}

	env := envFrom(map[string]string{"CLOUDFLARE_API_KEY": "k", "CLOUDFLARE_ACCOUNT_ID": "acct123"})
	if _, err := ResolveSpecialProvider(spec, "m", "", env); err == nil ||
		!strings.Contains(err.Error(), "CLOUDFLARE_GATEWAY_ID") {
		t.Fatalf("expected CLOUDFLARE_GATEWAY_ID error, got %v", err)
	}

	env = envFrom(map[string]string{
		"CLOUDFLARE_API_KEY":    "k",
		"CLOUDFLARE_ACCOUNT_ID": "acct123",
		"CLOUDFLARE_GATEWAY_ID": "gw456",
	})
	p, err := ResolveSpecialProvider(spec, "m", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://gateway.ai.cloudflare.com/v1/acct123/gw456/anthropic"
	if got := baseURLOf(t, p); got != want {
		t.Fatalf("ai-gateway base_url = %q, want %q", got, want)
	}
	if _, ok := p.(*anthropicCompatDriver); !ok {
		t.Fatalf("ai-gateway should speak Anthropic wire, got %T", p)
	}
}

func TestIsSpecialAuthProvider(t *testing.T) {
	special := []string{
		"azure-openai-responses", "amazon-bedrock", "google-vertex",
		"cloudflare-workers-ai", "cloudflare-ai-gateway",
	}
	for _, name := range special {
		if !IsSpecialAuthProvider(specFor(t, name)) {
			t.Errorf("%s should be a special-auth provider", name)
		}
	}
	for _, name := range []string{"openai", "anthropic", "deepseek"} {
		if IsSpecialAuthProvider(specFor(t, name)) {
			t.Errorf("%s should NOT be a special-auth provider", name)
		}
	}
}
