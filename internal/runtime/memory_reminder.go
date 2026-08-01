package runtime

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/memory"
)

// defaultMemoryReminderMaxChars is the per-turn character budget for the
// injected memory body. It is deliberately modest so relevant memory context
// does not crowd out the live conversation in the model's window.
const defaultMemoryReminderMaxChars = 600

// defaultMemoryReminderLimit is the number of search hits fetched per turn
// before budget trimming.
const defaultMemoryReminderLimit = 5

// MemoryReminderProvider injects relevant persistent memory as ephemeral
// background context on each turn (issue #478). It derives a query from the
// most recent user message, runs a BM25 search over the memory store, and
// surfaces the top-ranked snippets — budget-capped and deduped so identical
// context is never repeated turn after turn.
//
// The returned body is RAW plain text; the reminder registry wraps it in
// <system-reminder> tags and injects it only into the per-turn LLM request, so
// it never enters persisted history.
type MemoryReminderProvider struct {
	// Store is the persistent memory database. When nil the provider never
	// fires.
	Store *memory.Store

	// MaxChars caps the injected body length. <=0 uses
	// defaultMemoryReminderMaxChars.
	MaxChars int

	// Limit caps the number of search hits considered. <=0 uses
	// defaultMemoryReminderLimit.
	Limit int

	// Scope and ScopeID, when non-empty, focus the search on a single memory
	// scope (e.g. the current project id) via SearchOptions.
	Scope   string
	ScopeID string

	// mu guards lastBody so Reminder is safe to call across turns.
	mu       sync.Mutex
	lastBody string
}

// Name implements ReminderProvider.
func (p *MemoryReminderProvider) Name() string { return "memory" }

// Reminder implements ReminderProvider. It fires when the latest user message
// yields a search query that matches stored memory, returning a concise,
// budget-capped body of ranked snippets. It stays silent when the store is nil,
// there is no user text to search on, the search errors or returns nothing, or
// the produced body is identical to the one injected on the previous firing
// turn (dedupe).
func (p *MemoryReminderProvider) Reminder(ctx context.Context, msgs agentcore.MessageList) (string, bool) {
	if p == nil || p.Store == nil {
		return "", false
	}

	query := latestUserText(msgs)
	if strings.TrimSpace(query) == "" {
		return "", false
	}

	limit := p.Limit
	if limit <= 0 {
		limit = defaultMemoryReminderLimit
	}

	results, err := p.Store.Search(query, memory.SearchOptions{
		Scope:          p.Scope,
		ScopeID:        p.ScopeID,
		Limit:          limit,
		ReconcileFirst: true,
	})
	if err != nil || len(results) == 0 {
		return "", false
	}

	body := p.buildBody(results)
	if body == "" {
		return "", false
	}

	// Dedupe: never re-inject the identical body on a subsequent firing turn.
	p.mu.Lock()
	defer p.mu.Unlock()
	if body == p.lastBody {
		return "", false
	}
	p.lastBody = body
	return body, true
}

// buildBody renders the ranked results into a concise, budget-capped body.
// MEMORY.md index files (and free-type entries) are ordered first as they are
// the most useful high-level context.
func (p *MemoryReminderProvider) buildBody(results []memory.SearchResult) string {
	maxChars := p.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMemoryReminderMaxChars
	}

	// Stable sort so index-type / MEMORY.md hits float to the top while
	// preserving the underlying BM25 order among equals.
	ordered := make([]memory.SearchResult, len(results))
	copy(ordered, results)
	sort.SliceStable(ordered, func(i, j int) bool {
		return memoryRank(ordered[i]) < memoryRank(ordered[j])
	})

	const heading = "Relevant memory:"
	var b strings.Builder
	b.WriteString(heading)
	for _, r := range ordered {
		snippet := strings.TrimSpace(strings.ReplaceAll(r.Snippet, "\n", " "))
		if snippet == "" {
			continue
		}
		line := "\n- " + r.Path + ": " + snippet
		// Enforce the budget: stop before exceeding maxChars, and never emit a
		// heading-only body.
		if b.Len()+len(line) > maxChars {
			if b.Len() > len(heading) {
				break
			}
			// The very first line already overflows: hard-truncate it so we
			// still surface something within budget.
			room := maxChars - b.Len()
			if room <= 0 {
				break
			}
			if room < len(line) {
				line = line[:room]
			}
			b.WriteString(line)
			break
		}
		b.WriteString(line)
	}

	if b.Len() <= len(heading) {
		return ""
	}
	return b.String()
}

// memoryRank returns a sort key that floats MEMORY.md index files and free-type
// entries to the front (rank 0) ahead of everything else (rank 1).
func memoryRank(r memory.SearchResult) int {
	if strings.HasSuffix(r.Path, "MEMORY.md") || r.Type == memory.TypeFree {
		return 0
	}
	return 1
}

// latestUserText returns the flattened text of the most recent user message in
// msgs, or "" if there is none. Reminder messages are user-role too, but they
// carry the <system-reminder> preamble; those are skipped so the search query
// reflects the genuine user request rather than previously injected context.
func latestUserText(msgs agentcore.MessageList) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		um, ok := msgs[i].(agentcore.UserMessage)
		if !ok {
			continue
		}
		text := agentcore.ContentToText(um.Content)
		if strings.Contains(text, "<system-reminder>") {
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		return text
	}
	return ""
}
