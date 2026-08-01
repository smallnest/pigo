package dream

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements the real, LLM-backed Consolidator (SPEC §5.1 step 5,
// §5.1.1). It turns the deterministic Plan into a prompt for the main-session
// model, asks it to confirm semantic merges and conservative prunes, and parses
// the strict-JSON response back into a ConsolidateResult the Runner applies.
//
// The deterministic half (exact dedup, dead-path cleanup, MEMORY.md rewrite,
// Reconcile, counters) stays in the Runner; this file is purely the semantic
// LLM step. It never touches disk — it only produces decisions the Runner
// path-guards and applies.

// completeFn performs a single LLM completion: given the system and user
// prompts it returns the model's text response, or an error for a hard
// transport/provider failure. The provider-backed implementation lives in
// apply.go (newModelCompleter); tests inject a canned function so no live model
// is ever called.
type completeFn func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// defaultBodyBudget caps how many bytes of each entry body are shown to the
// model, bounding prompt size on large memory libraries (SPEC §8.2 token
// budget). Bodies longer than this are truncated with a marker so the model
// still sees the leading, usually most salient, content.
const defaultBodyBudget = 6000

// llmConsolidator is the production Consolidator: it drives the main-session
// model through complete and parses the response. bodyBudget (0 → default)
// bounds per-entry body size in the prompt.
type llmConsolidator struct {
	complete   completeFn
	bodyBudget int
}

// Consolidate builds the prompt from the plan, runs one model completion, and
// parses the response into merge/prune decisions. A hard model/transport error
// is returned (the Runner maps it to a failed run, SPEC §5.5). A well-formed
// call whose text cannot be parsed is NOT an error: it yields an empty result
// with an explanatory note, so an unparseable response conservatively KEEPs
// everything (PRD FR-14) and the deterministic pass still applies.
func (c *llmConsolidator) Consolidate(ctx context.Context, in ConsolidateInput) (ConsolidateResult, error) {
	if c.complete == nil {
		return ConsolidateResult{}, fmt.Errorf("dream: llmConsolidator has no completion function")
	}
	eligible := eligibleFiles(in.Plan)
	if len(eligible) == 0 {
		// Nothing the model could act on (empty library, or only MEMORY.md /
		// duplicates). Skip the call entirely.
		return ConsolidateResult{}, nil
	}
	budget := c.bodyBudget
	if budget <= 0 {
		budget = defaultBodyBudget
	}
	prompt := buildConsolidatePrompt(in, eligible, budget)

	raw, err := c.complete(ctx, dreamSystemPrompt, prompt)
	if err != nil {
		return ConsolidateResult{}, fmt.Errorf("dream: model completion: %w", err)
	}

	allowed := make(map[string]struct{}, len(eligible))
	for _, f := range eligible {
		allowed[filepath.Clean(f.Path)] = struct{}{}
	}
	return parseConsolidateResponse(raw, allowed), nil
}

// eligibleFiles is the subset of plan files the model may act on: it excludes
// MEMORY.md index files (they are indexes, not entries — #521 NEW_WORK: we
// special-case MEMORY.md out of merge/prune so the index is never folded into an
// entry) and the redundant members of an exact-dedupe group (g.Paths[1:], which
// the deterministic pass removes anyway — offering them would let the model
// merge into a path about to be deleted). The representative g.Paths[0] stays.
func eligibleFiles(plan Plan) []MemoryFile {
	drop := make(map[string]struct{})
	for _, g := range plan.DedupeGroups {
		for _, p := range g.Paths[1:] {
			drop[filepath.Clean(p)] = struct{}{}
		}
	}
	var out []MemoryFile
	for _, f := range plan.Files {
		if isMemoryIndex(f.Path) {
			continue
		}
		if _, dup := drop[filepath.Clean(f.Path)]; dup {
			continue
		}
		out = append(out, f)
	}
	return out
}

// isMemoryIndex reports whether path is a scope MEMORY.md index file, which the
// consolidation step must never merge, rewrite, or prune.
func isMemoryIndex(path string) bool {
	return strings.EqualFold(filepath.Base(path), "MEMORY.md")
}

// buildConsolidatePrompt renders the user prompt: the scope, the eligible
// entries (path + scope/type + body, truncated to budget), and the deterministic
// hints (near-dup candidate pairs, dead local-path references). It only lists
// paths that are eligible, so the model is naturally steered away from MEMORY.md
// and duplicate paths.
func buildConsolidatePrompt(in ConsolidateInput, eligible []MemoryFile, budget int) string {
	var b strings.Builder
	b.WriteString("# Memory consolidation request\n\n")
	b.WriteString("Memory root: ")
	b.WriteString(in.MemoryRoot)
	b.WriteByte('\n')
	if in.ProjectDir != "" {
		b.WriteString("Active project scope: ")
		b.WriteString(in.ProjectDir)
		b.WriteByte('\n')
	} else {
		b.WriteString("Scope: global only\n")
	}
	b.WriteString(fmt.Sprintf("\n## Entries (%d)\n\n", len(eligible)))
	for i, f := range eligible {
		typ := f.Type
		if typ == "" {
			typ = "(root)"
		}
		b.WriteString(fmt.Sprintf("### [%d] %s\n", i+1, f.Path))
		b.WriteString(fmt.Sprintf("scope=%s type=%s bytes=%d\n\n", f.Scope, typ, f.Size))
		b.WriteString("```\n")
		b.WriteString(truncateBody(f.Body, budget))
		b.WriteString("\n```\n\n")
	}

	if pairs := eligiblePairs(in.Plan, eligible); len(pairs) > 0 {
		b.WriteString("## Near-duplicate candidate pairs (merge only if truly overlapping)\n\n")
		for _, p := range pairs {
			b.WriteString(fmt.Sprintf("- %s  <->  %s  (similarity %.2f)\n", p.A, p.B, p.Similarity))
		}
		b.WriteByte('\n')
	}

	if refs := eligibleInvalidRefs(in.Plan, eligible); len(refs) > 0 {
		b.WriteString("## Entries referencing local files that no longer exist\n")
		b.WriteString("(the dead reference text is cleaned automatically; only PRUNE an entry if losing that reference leaves it meaningless)\n\n")
		for _, r := range refs {
			b.WriteString(fmt.Sprintf("- %s references missing %s\n", r.File, r.Ref))
		}
		b.WriteByte('\n')
	}

	b.WriteString("Return the JSON object described in your instructions. When in doubt, KEEP.\n")
	return b.String()
}

// truncateBody trims body to at most budget bytes on a rune boundary, appending
// a marker when truncation occurred so the model knows content was elided.
func truncateBody(body string, budget int) string {
	if budget <= 0 || len(body) <= budget {
		return body
	}
	cut := budget
	for cut > 0 && !isRuneStart(body[cut]) {
		cut--
	}
	return body[:cut] + "\n…[truncated]"
}

// isRuneStart reports whether b is not a UTF-8 continuation byte, so a truncation
// cut there does not split a multi-byte rune.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// eligiblePairs filters the plan's near-dup pairs to those whose BOTH members
// are eligible (both are still offered to the model), so we never point the
// model at a MEMORY.md or a to-be-deduped duplicate.
func eligiblePairs(plan Plan, eligible []MemoryFile) []NearDupPair {
	ok := make(map[string]struct{}, len(eligible))
	for _, f := range eligible {
		ok[filepath.Clean(f.Path)] = struct{}{}
	}
	var out []NearDupPair
	for _, p := range plan.NearDupPairs {
		_, a := ok[filepath.Clean(p.A)]
		_, b := ok[filepath.Clean(p.B)]
		if a && b {
			out = append(out, p)
		}
	}
	return out
}

// eligibleInvalidRefs filters dead-path references to those on eligible files.
func eligibleInvalidRefs(plan Plan, eligible []MemoryFile) []InvalidPathRef {
	ok := make(map[string]struct{}, len(eligible))
	for _, f := range eligible {
		ok[filepath.Clean(f.Path)] = struct{}{}
	}
	var out []InvalidPathRef
	for _, r := range plan.InvalidPathRefs {
		if _, found := ok[filepath.Clean(r.File)]; found {
			out = append(out, r)
		}
	}
	return out
}

// modelDecision mirrors the strict JSON output schema the dream prompt requires.
type modelDecision struct {
	Merges []struct {
		Keep   string   `json:"keep"`
		Body   string   `json:"body"`
		Remove []string `json:"remove"`
	} `json:"merges"`
	Prunes []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"prunes"`
	Notes []string `json:"notes"`
}

// parseConsolidateResponse turns the model's text into a ConsolidateResult,
// validating every path against allowed (the eligible input paths). It is
// conservative by construction: any decision that references an unknown path, a
// MEMORY.md index, an empty merged body, or an empty prune reason is dropped
// rather than obeyed, and an unparseable response yields an empty result with a
// note (PRD FR-14 — default to KEEP on uncertainty). It never returns an error;
// callers treat a hard model failure separately.
func parseConsolidateResponse(raw string, allowed map[string]struct{}) ConsolidateResult {
	body := extractJSONObject(raw)
	if body == "" {
		return ConsolidateResult{Notes: []string{"dream: model response contained no JSON object; kept all entries"}}
	}
	var dec modelDecision
	if err := json.Unmarshal([]byte(body), &dec); err != nil {
		return ConsolidateResult{Notes: []string{"dream: model response was not valid JSON; kept all entries"}}
	}

	var res ConsolidateResult
	res.MergedBodies = make(map[string]string)
	delSet := make(map[string]struct{})

	valid := func(p string) (string, bool) {
		clean := filepath.Clean(strings.TrimSpace(p))
		if clean == "" || clean == "." {
			return "", false
		}
		if isMemoryIndex(clean) {
			return "", false
		}
		if _, ok := allowed[clean]; !ok {
			return "", false
		}
		return clean, true
	}

	for _, m := range dec.Merges {
		keep, ok := valid(m.Keep)
		if !ok || strings.TrimSpace(m.Body) == "" {
			// Unknown/invalid target or an empty rewrite: skip, keep everything.
			continue
		}
		removed := 0
		for _, r := range m.Remove {
			rp, ok := valid(r)
			if !ok || rp == keep {
				continue
			}
			if _, dup := delSet[rp]; dup {
				continue
			}
			delSet[rp] = struct{}{}
			removed++
		}
		if removed == 0 {
			// A merge that removes nothing is a no-op rewrite; ignore it to avoid
			// gratuitously touching a file the model merely echoed back.
			continue
		}
		res.MergedBodies[keep] = m.Body
		res.Merged += removed
	}

	for _, p := range dec.Prunes {
		pp, ok := valid(p.Path)
		if !ok || strings.TrimSpace(p.Reason) == "" {
			// No path or no stated reason → conservative KEEP.
			continue
		}
		if _, dup := delSet[pp]; dup {
			continue
		}
		// Never prune an entry we are simultaneously keeping as a merge target.
		if _, kept := res.MergedBodies[pp]; kept {
			continue
		}
		delSet[pp] = struct{}{}
		res.Pruned++
		res.Notes = append(res.Notes, fmt.Sprintf("pruned %s: %s", pp, strings.TrimSpace(p.Reason)))
	}

	if len(res.MergedBodies) == 0 {
		res.MergedBodies = nil
	}
	for p := range delSet {
		res.Deletions = append(res.Deletions, p)
	}
	sort.Strings(res.Deletions)

	for _, n := range dec.Notes {
		if s := strings.TrimSpace(n); s != "" {
			res.Notes = append(res.Notes, s)
		}
	}
	return res
}

// extractJSONObject returns the outermost {...} span of s, tolerating models
// that wrap the object in prose or Markdown code fences. It returns "" when no
// balanced object is found.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
