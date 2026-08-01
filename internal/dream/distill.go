package dream

// This file implements the JSONL distillation step of a /dream run (SPEC §5.3,
// PRD US-005 / FR-13): it selects the current project's recent sessions, reads
// their transcripts (truncated to a token/byte budget, SPEC §8.2), and — via the
// LLM distiller in consolidator.go — turns durable facts into new memory
// entries, deduped against the existing library so nothing already recorded is
// re-added.
//
// The deterministic half lives here (session-window selection, project filter,
// transcript rendering, dedup, path construction); the semantic half (which
// facts are durable) is a separate LLM call driven by the llmConsolidator. This
// keeps the merge/prune pass and its parser (consolidator.go) untouched: the
// Runner gathers the transcripts, the consolidator runs a distinct distill
// completion with its own prompt/schema, and the distilled NewEntries are folded
// into the same ConsolidateResult the Runner already applies (wiring choice "b"
// from #523: a separate step appended to the result, chosen over folding it into
// the merge/prune prompt so each concern keeps its own input, prompt and schema
// and the well-tested merge/prune parser is not perturbed).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

// defaultTranscriptBudget bounds the total bytes of session transcript text fed
// to the distiller, keeping the distill prompt within the model context even on
// long or numerous recent sessions (SPEC §8.2 token budget). Sessions are added
// most-recent-first until the budget is reached.
const defaultTranscriptBudget = 24000

// distillNewEntryTypes is the closed set of durable memory types the distiller
// may emit (PRD FR-13: user / feedback / project / reference). Ephemeral kinds
// (checkpoint / progress) are intentionally excluded — one-shot task state is
// not distilled into long-term memory.
var distillNewEntryTypes = map[string]struct{}{
	"user":      {},
	"feedback":  {},
	"project":   {},
	"reference": {},
}

// SessionSource is the read-only view of the session store the distiller needs:
// list session headers and load a session's messages. *session.Store satisfies
// it; tests inject a stub so no real session files (or LLM) are required.
type SessionSource interface {
	List() ([]session.SessionHeader, error)
	Load(id string) (session.SessionHeader, agentcore.MessageList, error)
}

// resolveSessionStore opens the default session store rooted at
// $PIGO_HOME/sessions (else ~/.pigo/sessions), mirroring
// headless.SessionStore's resolution. It is duplicated here rather than imported
// to keep internal/dream free of a dependency on the CLI assembly layer, exactly
// as ResolveMemoryRoot duplicates the memory-root resolution. A resolution
// failure yields a nil source and no error: distillation then degrades to a
// no-op rather than failing the whole dream run.
func resolveSessionStore() (SessionSource, error) {
	dir := os.Getenv("PIGO_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil
		}
		dir = filepath.Join(home, ".pigo")
	}
	store, err := session.NewStore(filepath.Join(dir, "sessions"))
	if err != nil {
		return nil, nil
	}
	return store, nil
}

// sessionMatchesProject reports whether a session belongs to the project rooted
// at projectDir. Attribution is by the session's recorded Cwd: two directories
// belong to the same project when their stable project ids match (the same id
// internal/memory and BuildPlan derive). A session with an empty Cwd is
// unattributed and never matches; an empty projectDir (a global-only run) has no
// project to match, so nothing is selected.
func sessionMatchesProject(h session.SessionHeader, projectDir string) bool {
	if projectDir == "" || h.Cwd == "" {
		return false
	}
	return projectID(h.Cwd) == projectID(projectDir)
}

// collectRecentSessions selects the recent sessions to distill, applying the
// SPEC §5.3 combined window over the project-filtered set:
//   - state.LastRunAt non-zero → every matching session updated strictly after
//     the last run (incremental distillation since the last dream).
//   - state.LastRunAt zero (never run) → the most-recent recentN matching
//     sessions by UpdatedAt descending (a bounded first-run window).
//
// recentN falls back to DefaultRecentSessions when non-positive. The result is
// ordered most-recent-first so transcript budgeting keeps the freshest context.
func collectRecentSessions(sessions []session.SessionHeader, state State, projectDir string, recentN int) []session.SessionHeader {
	if recentN <= 0 {
		recentN = DefaultRecentSessions
	}
	var matched []session.SessionHeader
	for _, h := range sessions {
		if sessionMatchesProject(h, projectDir) {
			matched = append(matched, h)
		}
	}
	// Most-recent-first regardless of the source ordering.
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].UpdatedAt.After(matched[j].UpdatedAt)
	})

	if !state.LastRunAt.IsZero() {
		var out []session.SessionHeader
		for _, h := range matched {
			if h.UpdatedAt.After(state.LastRunAt) {
				out = append(out, h)
			}
		}
		return out
	}
	if len(matched) > recentN {
		matched = matched[:recentN]
	}
	return matched
}

// collectTranscripts selects the recent project sessions (collectRecentSessions)
// and renders their messages into a single transcript string truncated to
// budget bytes (SPEC §8.2). It returns "" when the source is nil or no session
// matches — the no-matching-sessions no-op (SPEC §5.5): the caller then records
// Distilled=0 with a "无新增" note. A per-session load error is skipped rather
// than failing the run (a single corrupt session must not abort distillation).
func collectTranscripts(src SessionSource, state State, projectDir string, recentN, budget int) string {
	if src == nil {
		return ""
	}
	if budget <= 0 {
		budget = defaultTranscriptBudget
	}
	headers, err := src.List()
	if err != nil {
		return ""
	}
	selected := collectRecentSessions(headers, state, projectDir, recentN)
	if len(selected) == 0 {
		return ""
	}

	var b strings.Builder
	for _, h := range selected {
		if b.Len() >= budget {
			break
		}
		_, msgs, err := src.Load(h.ID)
		if err != nil {
			continue
		}
		section := renderTranscript(h, msgs)
		if section == "" {
			continue
		}
		remaining := budget - b.Len()
		if len(section) > remaining {
			section = truncateBody(section, remaining)
		}
		b.WriteString(section)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// renderTranscript renders one session's messages as a compact, role-tagged
// transcript for the distiller. Tool-result bodies are included but bounded so a
// single huge tool output cannot dominate the budget; empty messages are
// skipped. The header line carries the session id and update time for context.
func renderTranscript(h session.SessionHeader, msgs agentcore.MessageList) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Session %s (updated %s)\n", h.ID, h.UpdatedAt.UTC().Format("2006-01-02")))
	wrote := false
	for _, m := range msgs {
		var role, text string
		switch mm := m.(type) {
		case agentcore.UserMessage:
			role, text = "user", agentcore.ContentToText(mm.Content)
		case agentcore.AssistantMessage:
			role, text = "assistant", agentcore.ContentToText(mm.Content)
		case agentcore.ToolResultMessage:
			role, text = "tool", clip(agentcore.ContentToText(mm.Content), 500)
		case agentcore.CompactionMessage:
			role, text = "summary", mm.Summary
		default:
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(text)
		b.WriteString("\n")
		wrote = true
	}
	if !wrote {
		return ""
	}
	return b.String()
}

// clip trims s to at most n bytes on a rune boundary, appending an ellipsis when
// truncation occurred. Used to bound individual tool-result bodies inside a
// transcript so one large output does not crowd out the rest.
func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// distilledEntry mirrors one element of the distiller's strict-JSON output: a
// proposed new memory entry with its semantic type, target scope, a short title
// (turned into a filename slug) and the Markdown body to persist.
type distilledEntry struct {
	Type  string `json:"type"`
	Scope string `json:"scope"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// distillResponse is the full strict-JSON schema the distill prompt requires.
type distillResponse struct {
	Entries []distilledEntry `json:"entries"`
	Notes   []string         `json:"notes"`
}

// parseDistillResponse turns the distiller's text into concrete NewEntry writes,
// deduped against the existing memory bodies. It is conservative by
// construction: an unparseable response, an unknown/ephemeral type, an empty
// body, or an entry that is a near-duplicate of an existing memory (or of an
// already-accepted new entry) is dropped rather than written (PRD FR-13/FR-14).
// It never errors; a hard model failure is handled by the caller.
//
// memoryRoot + projectDir place each entry within the scope the Runner's
// withinScope guard permits: global entries under <root>/global/<type>/, project
// entries under <root>/projects/<id>/<type>/. A "project" entry on a global-only
// run (empty projectDir) has nowhere valid to live and is skipped.
func parseDistillResponse(raw string, existing []MemoryFile, memoryRoot, projectDir string) ([]NewEntry, []string) {
	body := extractJSONObject(raw)
	if body == "" {
		return nil, nil
	}
	var resp distillResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, nil
	}

	// Precompute token sets of existing bodies for near-duplicate rejection.
	existingTokens := make([]map[string]struct{}, 0, len(existing))
	for _, f := range existing {
		existingTokens = append(existingTokens, tokenize(f.Body))
	}

	var out []NewEntry
	var notes []string
	acceptedTokens := make([]map[string]struct{}, 0)
	usedPaths := make(map[string]struct{})

	for _, e := range resp.Entries {
		typ := strings.ToLower(strings.TrimSpace(e.Type))
		if _, ok := distillNewEntryTypes[typ]; !ok {
			continue
		}
		body := strings.TrimSpace(e.Body)
		if body == "" {
			continue
		}
		scopeDir, ok := distillScopeDir(e.Scope, projectDir)
		if !ok {
			continue
		}
		tok := tokenize(body)
		if isNearDup(tok, existingTokens) || isNearDup(tok, acceptedTokens) {
			notes = append(notes, fmt.Sprintf("distill: skipped near-duplicate of existing memory (%s)", firstLine(e.Title, body)))
			continue
		}
		path := distillPath(memoryRoot, scopeDir, typ, e.Title, body, usedPaths)
		usedPaths[filepath.Clean(path)] = struct{}{}
		acceptedTokens = append(acceptedTokens, tok)
		out = append(out, NewEntry{Path: path, Body: ensureTrailingNewline(body)})
	}

	for _, n := range resp.Notes {
		if s := strings.TrimSpace(n); s != "" {
			notes = append(notes, s)
		}
	}
	return out, notes
}

// distillScopeDir maps a requested scope ("global"/"project") to its on-disk
// scope directory relative to the memory root. A "project" scope requires a
// project dir; without one the entry cannot be placed and ok is false. An empty
// or unrecognized scope defaults to global (the safe, always-valid target).
func distillScopeDir(scope, projectDir string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "project", "projects":
		if projectDir == "" {
			return "", false
		}
		return filepath.Join("projects", projectID(projectDir)), true
	default:
		return "global", true
	}
}

// distillPath builds the absolute file path for a new distilled entry:
// <memoryRoot>/<scopeDir>/<type>/<slug>.md. The slug derives from the title
// (sanitized) or, when absent, a short content hash, so a re-run of the same
// durable fact lands on a stable path (and is caught by the near-dup check
// against the now-existing file, keeping distillation idempotent). If the path
// is already taken within this run, a short body hash disambiguates it.
func distillPath(memoryRoot, scopeDir, typ, title, body string, used map[string]struct{}) string {
	slug := slugify(title)
	if slug == "" {
		slug = shortHash(body)
	}
	name := slug + ".md"
	path := filepath.Join(memoryRoot, scopeDir, typ, name)
	if _, taken := used[filepath.Clean(path)]; taken {
		path = filepath.Join(memoryRoot, scopeDir, typ, slug+"-"+shortHash(body)+".md")
	}
	return path
}

// isNearDup reports whether tok is at or above NearDupThreshold Jaccard
// similarity with any set in others — the same conservative near-duplicate
// signal the deterministic plan uses for merge candidates, reused here so a
// distilled fact that already lives in memory is not re-added (PRD FR-13).
func isNearDup(tok map[string]struct{}, others []map[string]struct{}) bool {
	for _, o := range others {
		if jaccard(tok, o) >= NearDupThreshold {
			return true
		}
	}
	return false
}

// slugify turns a title into a lowercase, dash-separated filename stem of ASCII
// alphanumerics, bounding the length so paths stay reasonable. Non-alphanumeric
// runs collapse to a single dash; leading/trailing dashes are trimmed. A title
// with no usable characters yields "".
func slugify(title string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	const maxLen = 60
	if len(slug) > maxLen {
		slug = strings.Trim(slug[:maxLen], "-")
	}
	return slug
}

// firstLine returns a short human-readable label for a note: the trimmed title
// when present, else the first line of body, truncated.
func firstLine(title, body string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		s = strings.TrimSpace(body)
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clip(s, 60)
}

// ensureTrailingNewline guarantees body ends with exactly one newline so written
// memory files match the one-entry-per-file convention and diff cleanly.
func ensureTrailingNewline(body string) string {
	return strings.TrimRight(body, "\n") + "\n"
}

// shortHash returns the first 8 hex chars of sha256(s), a stable disambiguator
// for slugs derived from identical/absent titles.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
