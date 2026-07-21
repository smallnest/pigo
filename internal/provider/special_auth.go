// This file implements parameter validation and endpoint construction for the
// "special auth" providers (US-007 / FR-12): Azure OpenAI, Amazon Bedrock,
// Google Vertex, and Cloudflare (Workers AI + AI Gateway). Unlike the generic
// bearer/x-api-key providers, each of these composes its endpoint from several
// environment variables and/or needs a non-standard credential path, so a
// dedicated resolver validates the required parameters and builds the concrete
// base URL before handing off to the shared OpenAI-/Anthropic-compatible driver.
//
// Scope note (PRD Non-Goals): AWS SigV4 request signing is NOT implemented.
// Bedrock supports only the AWS_BEARER_TOKEN_BEDROCK bearer path; other AWS
// credential sources (AWS_PROFILE, AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY)
// are only *detected* so that a clear, actionable error is returned instead of
// an opaque auth failure.
//
// Security: this file reads env var NAMES and composes URLs from non-secret
// parameters (region, resource name, account id, …). Secret values (API keys,
// bearer tokens) are never logged or embedded in error text — errors name the
// absent env var, never a value.
package provider

import (
	"fmt"
	"strings"
)

// IsSpecialAuthProvider reports whether a provider spec needs the bespoke
// endpoint-construction / credential-validation handled by ResolveSpecialProvider,
// rather than the generic driver wiring. It matches the multi-parameter auth
// schemes (azure/aws/special) and the two Cloudflare providers (which keep a
// standard auth scheme but still compose their endpoint from env vars).
func IsSpecialAuthProvider(spec ProviderSpec) bool {
	switch spec.AuthScheme {
	case AuthAzure, AuthAWS, AuthSpecial:
		return true
	}
	return strings.HasPrefix(spec.Name, "cloudflare-")
}

// ResolveSpecialProvider validates the required parameters for a special-auth
// provider and constructs the matching wire driver against the composed base
// URL. flagBaseURL is the explicit --base-url override (highest precedence, wins
// over any composed default); env resolves environment variables (os.Getenv in
// production, a fake in tests). A missing required parameter yields an error
// naming exactly which env var is absent; no network request is made here.
func ResolveSpecialProvider(spec ProviderSpec, model, flagBaseURL string, env func(string) string) (Provider, error) {
	if env == nil {
		env = func(string) string { return "" }
	}
	models := []Model{{Provider: spec.Name, ID: model, SupportsImages: true}}
	switch spec.Name {
	case "azure-openai-responses":
		return resolveAzureOpenAI(spec, model, flagBaseURL, env, models)
	case "amazon-bedrock":
		return resolveBedrock(spec, flagBaseURL, env, models)
	case "google-vertex":
		return resolveGoogleVertex(spec, flagBaseURL, env, models)
	case "cloudflare-workers-ai":
		return resolveCloudflareWorkersAI(spec, flagBaseURL, env, models)
	case "cloudflare-ai-gateway":
		return resolveCloudflareAIGateway(spec, flagBaseURL, env, models)
	default:
		return nil, fmt.Errorf("provider %q is not a special-auth provider", spec.Name)
	}
}

// resolveAzureOpenAI composes the Azure OpenAI endpoint. The endpoint origin is
// AZURE_OPENAI_BASE_URL (or the --base-url override), else it is built from
// AZURE_OPENAI_RESOURCE_NAME as https://{resource}.openai.azure.com. The API
// version (AZURE_OPENAI_API_VERSION, default "v1") and an optional deployment
// mapping (AZURE_OPENAI_DEPLOYMENT_NAME_MAP) shape the path. Auth uses
// AZURE_OPENAI_API_KEY over the OpenAI wire.
func resolveAzureOpenAI(_ ProviderSpec, model, flagBaseURL string, env func(string) string, models []Model) (Provider, error) {
	if strings.TrimSpace(env("AZURE_OPENAI_API_KEY")) == "" {
		return nil, fmt.Errorf("azure-openai-responses: missing required env var AZURE_OPENAI_API_KEY")
	}
	origin := strings.TrimSpace(flagBaseURL)
	if origin == "" {
		origin = strings.TrimSpace(env("AZURE_OPENAI_BASE_URL"))
	}
	if origin == "" {
		resource := strings.TrimSpace(env("AZURE_OPENAI_RESOURCE_NAME"))
		if resource == "" {
			return nil, fmt.Errorf("azure-openai-responses: missing endpoint configuration; set AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME")
		}
		origin = fmt.Sprintf("https://%s.openai.azure.com", resource)
	}
	apiVersion := strings.TrimSpace(env("AZURE_OPENAI_API_VERSION"))
	if apiVersion == "" {
		apiVersion = "v1"
	}
	deployment := resolveAzureDeployment(env("AZURE_OPENAI_DEPLOYMENT_NAME_MAP"), model)
	baseURL := azureEndpoint(origin, apiVersion, deployment)
	return NewOpenAICompatibleProvider(baseURL, models), nil
}

// azureEndpoint builds the Azure OpenAI base URL from a validated origin. When a
// deployment is resolved for the model, the classic deployment-scoped path is
// used (…/openai/deployments/{deployment}); otherwise the version-scoped v1 path
// (…/openai/{apiVersion}) is used. The shared driver appends /chat/completions.
func azureEndpoint(origin, apiVersion, deployment string) string {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if deployment != "" {
		return fmt.Sprintf("%s/openai/deployments/%s", origin, deployment)
	}
	return fmt.Sprintf("%s/openai/%s", origin, apiVersion)
}

// resolveAzureDeployment parses AZURE_OPENAI_DEPLOYMENT_NAME_MAP (a
// comma-separated list of model=deployment pairs) and returns the deployment
// mapped to model, or "" when the map is empty or has no entry for the model.
func resolveAzureDeployment(raw, model string) string {
	m := parseDeploymentMap(raw)
	return m[strings.TrimSpace(model)]
}

// parseDeploymentMap parses a comma-separated "model=deployment" list into a
// map. Blank entries and entries without '=' are skipped; keys and values are
// trimmed. It never returns nil so lookups are always safe.
func parseDeploymentMap(raw string) map[string]string {
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// resolveBedrock composes the Amazon Bedrock runtime endpoint
// (https://bedrock-runtime.{region}.amazonaws.com; region defaults to
// us-east-1) and validates credentials. Only the AWS_BEARER_TOKEN_BEDROCK
// bearer path is supported (SigV4 is out of scope): if only AWS_PROFILE or
// static AWS keys are present, a clear error explains SigV4 is unsupported and
// names the missing AWS_BEARER_TOKEN_BEDROCK.
func resolveBedrock(_ ProviderSpec, flagBaseURL string, env func(string) string, models []Model) (Provider, error) {
	if strings.TrimSpace(env("AWS_BEARER_TOKEN_BEDROCK")) == "" {
		hasProfile := strings.TrimSpace(env("AWS_PROFILE")) != ""
		hasStaticKeys := strings.TrimSpace(env("AWS_ACCESS_KEY_ID")) != "" &&
			strings.TrimSpace(env("AWS_SECRET_ACCESS_KEY")) != ""
		if hasProfile || hasStaticKeys {
			return nil, fmt.Errorf("amazon-bedrock: detected AWS credentials (AWS_PROFILE / AWS_ACCESS_KEY_ID) but SigV4 request signing is not supported yet; set AWS_BEARER_TOKEN_BEDROCK to use the bearer-token path")
		}
		return nil, fmt.Errorf("amazon-bedrock: missing required env var AWS_BEARER_TOKEN_BEDROCK")
	}
	baseURL := strings.TrimSpace(flagBaseURL)
	if baseURL == "" {
		region := strings.TrimSpace(env("AWS_REGION"))
		if region == "" {
			region = "us-east-1"
		}
		baseURL = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", region)
	}
	return NewBedrockProvider(baseURL, models), nil
}

// resolveGoogleVertex composes the Vertex AI endpoint
// (https://{location}-aiplatform.googleapis.com) and validates that a project,
// a location, and a credential source (GOOGLE_CLOUD_API_KEY or ADC via
// GOOGLE_APPLICATION_CREDENTIALS) are present, naming any absent env var.
func resolveGoogleVertex(_ ProviderSpec, flagBaseURL string, env func(string) string, models []Model) (Provider, error) {
	if strings.TrimSpace(env("GOOGLE_CLOUD_PROJECT")) == "" {
		return nil, fmt.Errorf("google-vertex: missing required env var GOOGLE_CLOUD_PROJECT")
	}
	location := strings.TrimSpace(env("GOOGLE_CLOUD_LOCATION"))
	if location == "" {
		return nil, fmt.Errorf("google-vertex: missing required env var GOOGLE_CLOUD_LOCATION")
	}
	if strings.TrimSpace(env("GOOGLE_CLOUD_API_KEY")) == "" &&
		strings.TrimSpace(env("GOOGLE_APPLICATION_CREDENTIALS")) == "" {
		return nil, fmt.Errorf("google-vertex: missing credentials; set GOOGLE_CLOUD_API_KEY or GOOGLE_APPLICATION_CREDENTIALS (ADC)")
	}
	baseURL := strings.TrimSpace(flagBaseURL)
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com", location)
	}
	return NewOpenAICompatibleProvider(baseURL, models), nil
}

// resolveCloudflareWorkersAI composes the Workers AI endpoint
// (https://api.cloudflare.com/client/v4/accounts/{account}/ai/v1), requiring
// CLOUDFLARE_API_KEY and CLOUDFLARE_ACCOUNT_ID. OpenAI wire.
func resolveCloudflareWorkersAI(_ ProviderSpec, flagBaseURL string, env func(string) string, models []Model) (Provider, error) {
	if strings.TrimSpace(env("CLOUDFLARE_API_KEY")) == "" {
		return nil, fmt.Errorf("cloudflare-workers-ai: missing required env var CLOUDFLARE_API_KEY")
	}
	account := strings.TrimSpace(env("CLOUDFLARE_ACCOUNT_ID"))
	if account == "" {
		return nil, fmt.Errorf("cloudflare-workers-ai: missing required env var CLOUDFLARE_ACCOUNT_ID")
	}
	baseURL := strings.TrimSpace(flagBaseURL)
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/ai/v1", account)
	}
	return NewOpenAICompatibleProvider(baseURL, models), nil
}

// resolveCloudflareAIGateway composes the AI Gateway endpoint
// (https://gateway.ai.cloudflare.com/v1/{account}/{gateway}/anthropic),
// requiring CLOUDFLARE_API_KEY, CLOUDFLARE_ACCOUNT_ID, and CLOUDFLARE_GATEWAY_ID.
// Anthropic wire.
func resolveCloudflareAIGateway(_ ProviderSpec, flagBaseURL string, env func(string) string, models []Model) (Provider, error) {
	if strings.TrimSpace(env("CLOUDFLARE_API_KEY")) == "" {
		return nil, fmt.Errorf("cloudflare-ai-gateway: missing required env var CLOUDFLARE_API_KEY")
	}
	account := strings.TrimSpace(env("CLOUDFLARE_ACCOUNT_ID"))
	if account == "" {
		return nil, fmt.Errorf("cloudflare-ai-gateway: missing required env var CLOUDFLARE_ACCOUNT_ID")
	}
	gateway := strings.TrimSpace(env("CLOUDFLARE_GATEWAY_ID"))
	if gateway == "" {
		return nil, fmt.Errorf("cloudflare-ai-gateway: missing required env var CLOUDFLARE_GATEWAY_ID")
	}
	baseURL := strings.TrimSpace(flagBaseURL)
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://gateway.ai.cloudflare.com/v1/%s/%s/anthropic", account, gateway)
	}
	return NewAnthropicProvider(baseURL, models), nil
}
