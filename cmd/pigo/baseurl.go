package main

import (
	"os"
	"strings"

	"github.com/smallnest/pigo/internal/provider"
)

// resolveBaseURL determines the effective base URL for a selected provider,
// applying the base_url override precedence (US-004 / FR-8, FR-9). The first
// non-empty source wins, in this order:
//
//  1. flagBaseURL — the explicit --base-url/-u flag (highest).
//  2. provider-specific base-url env var(s) from spec.BaseURLEnvVars, in the
//     order the registry declares them (e.g. AZURE_OPENAI_BASE_URL).
//  3. the generic <PROVIDER>_BASE_URL env var, where <PROVIDER> is the provider
//     name uppercased with '-' rewritten to '_' (e.g. zai-coding-cn →
//     ZAI_CODING_CN_BASE_URL).
//  4. spec.DefaultBaseURL — the registry default (lowest).
//
// Values are trimmed of surrounding whitespace before the non-empty check, so a
// whitespace-only env var does not shadow a lower-precedence source.
func resolveBaseURL(spec provider.ProviderSpec, flagBaseURL string) string {
	// 1. Explicit flag wins over every env-var convention.
	if v := strings.TrimSpace(flagBaseURL); v != "" {
		return v
	}
	// 2. Provider-specific override env vars, in registry precedence order.
	for _, name := range spec.BaseURLEnvVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	// 3. Generic <PROVIDER>_BASE_URL convention.
	if envName := genericBaseURLEnvVar(spec.Name); envName != "" {
		if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
			return v
		}
	}
	// 4. Registry default.
	return spec.DefaultBaseURL
}

// genericBaseURLEnvVar derives the generic base-url override env var name for a
// provider: the provider name uppercased with hyphens rewritten to underscores,
// suffixed with _BASE_URL. For example "zai-coding-cn" → "ZAI_CODING_CN_BASE_URL"
// and "deepseek" → "DEEPSEEK_BASE_URL". An empty provider name yields "".
func genericBaseURLEnvVar(providerName string) string {
	n := strings.TrimSpace(providerName)
	if n == "" {
		return ""
	}
	n = strings.ReplaceAll(n, "-", "_")
	return strings.ToUpper(n) + "_BASE_URL"
}
