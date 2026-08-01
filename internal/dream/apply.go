package dream

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// This file wires the llmConsolidator to a real provider (the main-session
// model, SPEC Q3) and holds the MEMORY.md index cleanup the Runner runs after
// writeback. The provider plumbing mirrors internal/cli/run.SetupEnv: resolve a
// Provider for the model/base-url/protocol/provider tuple, resolve the API key
// through a CredentialStore (--api-key override → env → config), and run a
// single StreamCompletion, draining the event stream to the final text.

// NewLLMConsolidator builds the production Consolidator backed by the given
// model configuration — the same tuple cmd/pigo resolves for the main session
// (CLI flags overlaid with config.toml). It resolves the Provider once so every
// Consolidate call reuses it. A resolution failure (bad model / missing
// provider) is returned so the caller can decide whether to fall back to the
// no-op Consolidator or fail the run.
func NewLLMConsolidator(model, baseURL, protocol, providerName, apiKey string, thinking agentcore.ThinkingLevel) (Consolidator, error) {
	complete, err := newModelCompleter(model, baseURL, protocol, providerName, apiKey, thinking)
	if err != nil {
		return nil, err
	}
	return &llmConsolidator{complete: complete}, nil
}

// newModelCompleter resolves the provider and returns a completeFn that performs
// one non-streaming-consuming completion: it sends the system+user prompt as a
// single user turn (no tools — the dream agent only reasons and replies) and
// returns the concatenated assistant text. A hard "cannot build the stream"
// error is returned directly; a runtime failure rides the stream as a terminal
// error event whose message we convert to an error (so the Runner marks the run
// failed rather than silently deleting nothing, SPEC §5.5).
func newModelCompleter(model, baseURL, protocol, providerName, apiKey string, thinking agentcore.ThinkingLevel) (completeFn, error) {
	prov, resolvedName, err := provider.ResolveProvider(model, baseURL, protocol, providerName, os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("dream: resolve provider: %w", err)
	}
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(resolvedName, apiKey)

	return func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		key := creds.GetAPIKey(ctx, resolvedName)
		llm := provider.LlmContext{
			SystemPrompt: systemPrompt,
			Messages: agentcore.MessageList{
				agentcore.UserMessage{
					RoleField: agentcore.RoleUser,
					Content:   agentcore.ContentList{agentcore.NewTextContent(userPrompt)},
				},
			},
		}
		stream, err := prov.StreamCompletion(ctx, provider.CompletionRequest{
			Model:   model,
			Context: llm,
			Config:  provider.StreamConfig{APIKey: key, ThinkingLevel: thinking},
		})
		if err != nil {
			return "", err
		}
		final, err := drainToMessage(ctx, stream)
		if err != nil {
			return "", err
		}
		if final.StopReason == agentcore.StopReasonError {
			if final.ErrorMessage != "" {
				return "", fmt.Errorf("model error: %s", final.ErrorMessage)
			}
			return "", fmt.Errorf("model returned an error response")
		}
		return agentcore.ContentToText(final.Content), nil
	}, nil
}

// drainToMessage consumes the provider event stream to completion and returns
// the terminal assistant message. It mirrors the loop's stream-drain contract:
// the done/error event carries the final message; if the stream closes without
// one, it falls back to the stream Result. Draining is required because the
// producer blocks on the event channel until consumed.
func drainToMessage(ctx context.Context, stream *provider.AssistantMessageEventStream) (agentcore.AssistantMessage, error) {
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case provider.StreamDoneEvent:
			return e.Message, nil
		case provider.StreamErrorEvent:
			return e.Message, nil
		}
	}
	final, err := stream.Result(ctx)
	if err != nil {
		return agentcore.AssistantMessage{}, err
	}
	return final, nil
}

// updateScopeIndexes rewrites each affected scope's MEMORY.md to drop any line
// that references a now-deleted memory file, keeping the index consistent with
// the entries on disk and free of dangling links (PRD US-003). It is safe to
// call when no MEMORY.md exists (no-op) and when deleted is empty. Each rewrite
// is atomic (temp+rename) and guarded by withinScope, so it cannot escape the
// memory store.
func updateScopeIndexes(memoryRoot, projectDir string, deleted map[string]struct{}) error {
	if len(deleted) == 0 {
		return nil
	}
	scopes := []string{filepath.Join(memoryRoot, "global")}
	if projectDir != "" {
		scopes = append(scopes, filepath.Join(memoryRoot, "projects", projectID(projectDir)))
	}
	for _, scope := range scopes {
		index := filepath.Join(scope, "MEMORY.md")
		if _, err := os.Stat(index); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !withinScope(memoryRoot, projectDir, index) {
			continue
		}
		tokens := indexRefTokens(memoryRoot, scope, deleted)
		if len(tokens) == 0 {
			continue
		}
		raw, err := os.ReadFile(index)
		if err != nil {
			return err
		}
		newBody, changed := stripDanglingIndexLines(string(raw), tokens)
		if changed {
			if err := atomicWrite(index, []byte(newBody)); err != nil {
				return err
			}
		}
	}
	return nil
}

// indexRefTokens is the set of substrings that identify a deleted file inside a
// MEMORY.md index line: its absolute path, its path relative to the memory root
// and to the scope root, and its bare basename. A line containing any of these
// is treated as a link/reference to the removed entry. MEMORY.md itself is never
// a token (it is never a consolidation deletion target).
func indexRefTokens(memoryRoot, scope string, deleted map[string]struct{}) map[string]struct{} {
	tokens := make(map[string]struct{})
	for p := range deleted {
		clean := filepath.Clean(p)
		add := func(s string) {
			if s != "" && s != "." {
				tokens[filepath.ToSlash(s)] = struct{}{}
			}
		}
		add(clean)
		if rel, err := filepath.Rel(memoryRoot, clean); err == nil && !strings.HasPrefix(rel, "..") {
			add(rel)
		}
		if rel, err := filepath.Rel(scope, clean); err == nil && !strings.HasPrefix(rel, "..") {
			add(rel)
		}
		add(filepath.Base(clean))
	}
	return tokens
}

// stripDanglingIndexLines removes every line of body that references any of the
// reference tokens, returning the rewritten body and whether anything changed.
// It matches on the forward-slash form of each line so Windows-style separators
// in the index still match the slash tokens. Matching is boundary-aware: a token
// (e.g. the basename "b.md") only matches when it is not embedded inside a longer
// filename token (so "club.md" or "b.mdx" is not mistaken for "b.md"), avoiding
// dropping unrelated index lines.
func stripDanglingIndexLines(body string, tokens map[string]struct{}) (string, bool) {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		probe := filepath.ToSlash(line)
		drop := false
		for tok := range tokens {
			if containsRefToken(probe, tok) {
				drop = true
				break
			}
		}
		if drop {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return body, false
	}
	return strings.Join(kept, "\n"), true
}

// containsRefToken reports whether tok occurs in line at a filename boundary:
// the characters immediately before and after the match must not be filename
// continuation characters ([A-Za-z0-9_-]). This lets "b.md" match "user/b.md",
// "(b.md)" and "- b.md" while rejecting "club.md" and "b.mdx".
func containsRefToken(line, tok string) bool {
	if tok == "" {
		return false
	}
	from := 0
	for {
		i := strings.Index(line[from:], tok)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(tok)
		if !isFilenameChar(byteAt(line, start-1)) && !isFilenameChar(byteAt(line, end)) {
			return true
		}
		from = start + 1
	}
}

// byteAt returns line[i], or 0 when i is out of range (treated as a boundary).
func byteAt(line string, i int) byte {
	if i < 0 || i >= len(line) {
		return 0
	}
	return line[i]
}

// isFilenameChar reports whether b can appear inside a bare filename token, used
// to detect whether a reference-token match is embedded in a longer name.
func isFilenameChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return true
	}
	return false
}

