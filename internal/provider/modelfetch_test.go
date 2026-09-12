package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchRemoteModelsOpenAICompatible verifies the OpenAI-compatible path:
// GET {base}/models with a Bearer header, ids parsed, deduplicated, sorted.
func TestFetchRemoteModelsOpenAICompatible(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.3"},{"id":"glm-4.7"},{"id":"glm-5.3"}]}`))
	}))
	defer srv.Close()

	ids, err := FetchRemoteModels(context.Background(), srv.URL+"/v1", "openai", "test-key")
	if err != nil {
		t.Fatalf("FetchRemoteModels: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if len(ids) != 2 || ids[0] != "glm-4.7" || ids[1] != "glm-5.3" {
		t.Errorf("ids = %v, want deduplicated and sorted [glm-4.7 glm-5.3]", ids)
	}
}

// TestFetchRemoteModelsAnthropic verifies the Anthropic path: GET {base}/v1/models
// with x-api-key + anthropic-version headers.
func TestFetchRemoteModelsAnthropic(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-fable-5","display_name":"Claude Fable 5"}]}`))
	}))
	defer srv.Close()

	ids, err := FetchRemoteModels(context.Background(), srv.URL, "anthropic", "test-key")
	if err != nil {
		t.Fatalf("FetchRemoteModels: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if gotKey != "test-key" || gotVersion != anthropicAPIVersion {
		t.Errorf("headers = (%q, %q), want x-api-key + version", gotKey, gotVersion)
	}
	if len(ids) != 1 || ids[0] != "claude-fable-5" {
		t.Errorf("ids = %v, want [claude-fable-5]", ids)
	}
}

// TestFetchRemoteModelsFailures verifies graceful failure reporting: a missing
// base URL, an unknown protocol, server errors, malformed bodies, and that an
// empty key sends no auth header at all.
func TestFetchRemoteModelsFailures(t *testing.T) {
	if _, err := FetchRemoteModels(context.Background(), "  ", "openai", "k"); err == nil {
		t.Error("empty base URL succeeded, want error")
	}
	if _, err := FetchRemoteModels(context.Background(), "http://x", "carrier-pigeon", "k"); err == nil {
		t.Error("unknown protocol succeeded, want error")
	}

	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	// An empty key sends no auth header at all (local gateways like Ollama).
	if _, err := FetchRemoteModels(context.Background(), srv.URL, "openai", ""); err == nil {
		t.Error("401 response succeeded, want error")
	}
	if sawAuth {
		t.Error("empty key sent an Authorization header, want none")
	}
	if _, err := FetchRemoteModels(context.Background(), srv.URL, "openai", "k"); err == nil {
		t.Error("401 response succeeded, want error")
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv2.Close()
	if _, err := FetchRemoteModels(context.Background(), srv2.URL, "openai", "k"); err == nil || !strings.Contains(err.Error(), "response shape") {
		t.Errorf("malformed body error = %v, want response-shape failure", err)
	}
}
