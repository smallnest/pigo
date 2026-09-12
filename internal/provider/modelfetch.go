// This file implements online model discovery (issue #566, ported from dsh's
// "Fetch available models"): asking an endpoint which models it serves, so the
// /models catalog and /model switching can follow a provider's real lineup
// instead of only the static preset catalog.
//
// Both wire formats pigo speaks expose a plain GET for it:
//
//   - OpenAI-compatible (and Responses) gateways: GET {base}/models
//   - Anthropic Messages: GET {base}/v1/models
//
// and both answer {"data":[{"id": ...}]}, so one parser serves both. Auth
// follows each protocol's request convention (Bearer for OpenAI-compatible,
// x-api-key + anthropic-version for Anthropic). A local endpoint that needs no
// key is fine: the key is sent only when non-empty.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// remoteModelListTimeout bounds one catalog fetch. Slash actions run
// synchronously on the REPL goroutine, so an explicit /models fetch must
// return promptly even when the endpoint hangs; callers may pass their own
// deadline via ctx.
const remoteModelListTimeout = 10 * time.Second

// remoteModelList is the minimal response shape shared by OpenAI-compatible
// /models and Anthropic /v1/models. IDs are the wire ids /model accepts.
type remoteModelList struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

// FetchRemoteModels queries baseURL (with protocol selecting the URL shape and
// auth header convention) for its model catalog and returns the ids sorted and
// deduplicated. protocol may be empty, a canonical value ("openai",
// "openai/resp_api", "anthropic"), or a raw alias — it is normalized first, and
// the Responses protocol shares the Chat Completions /models path. An empty
// apiKey sends no auth header (local gateways like Ollama need none).
func FetchRemoteModels(ctx context.Context, baseURL, protocol, apiKey string) ([]string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("no base URL configured for model discovery")
	}
	canonical, err := NormalizeProtocol(protocol)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(baseURL, "/")
	path, auth := "/models", func(req *http.Request, key string) {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	switch canonical {
	case ProtocolAnthropic:
		path = "/v1/models"
		auth = func(req *http.Request, key string) {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", anthropicAPIVersion)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, remoteModelListTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		auth(req, apiKey)
	}

	resp, err := (&http.Client{Timeout: remoteModelListTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("model discovery request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("model discovery read failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model discovery: %s %s returned %d", req.Method, path, resp.StatusCode)
	}
	var list remoteModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("model discovery: unexpected response shape: %w", err)
	}
	seen := make(map[string]struct{}, len(list.Data))
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID == "" {
			continue
		}
		if _, dup := seen[m.ID]; dup {
			continue
		}
		seen[m.ID] = struct{}{}
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}
