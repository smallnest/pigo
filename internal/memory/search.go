package memory

import (
	"database/sql"
	"fmt"
	"strings"
)

// SearchResult is a single ranked hit from Store.Search. Score is normalized so
// that higher = better (the raw FTS5 bm25 value, where lower is better, is
// negated).
type SearchResult struct {
	Path    string
	Snippet string
	Score   float64
	Scope   Scope
	ScopeID string
	Type    Type
}

// SearchOptions tunes a Store.Search call. All fields are optional; the zero
// value performs an unfiltered search with default limit and score floor.
type SearchOptions struct {
	// Scope, ScopeID and Type, when non-empty, restrict results to rows whose
	// corresponding memory_index column matches exactly.
	Scope   string
	ScopeID string
	Type    string
	// Limit caps the number of returned results; <=0 means the default of 10.
	Limit int
	// ReconcileFirst runs a lazy Store.Reconcile before searching so that
	// off-tool writes are picked up. Its counts are ignored; hard errors
	// propagate.
	ReconcileFirst bool
	// ScoreFloor is the relative floor ratio: trailing rows scoring below
	// topScore*ScoreFloor are dropped. <=0 keeps all matches when 0, but the
	// default of 0.15 is used when the field is left at its zero value; pass a
	// negative value to explicitly disable the floor.
	ScoreFloor float64
}

// defaultSearchLimit is the result count used when SearchOptions.Limit <= 0.
const defaultSearchLimit = 10

// maxFetchLimit caps the over-fetch used to feed the relative score floor.
const maxFetchLimit = 50

// defaultScoreFloor is the relative floor applied when SearchOptions.ScoreFloor
// is left at its zero value.
const defaultScoreFloor = 0.15

// Search runs a BM25 full-text query over the indexed memory bodies.
//
// The free-form query is tokenized and OR-joined by buildFtsQuery; an empty
// token set returns (nil, nil) without touching SQL. Results are ranked by
// BM25 (converted to higher = better), over-fetched 3x (capped at 50) so a
// relative score floor can trim common-word-only noise, then sliced to the
// requested limit. Optional scope/scope_id/type filters restrict the corpus.
func (s *Store) Search(query string, opts SearchOptions) ([]SearchResult, error) {
	if opts.ReconcileFirst {
		if _, err := s.Reconcile(); err != nil {
			return nil, fmt.Errorf("memory: reconcile before search: %w", err)
		}
	}

	match := buildFtsQuery(query)
	if match == "" {
		// No usable tokens: treat as empty query with no results, send no SQL.
		return nil, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	fetchLimit := limit * 3
	if fetchLimit > maxFetchLimit {
		fetchLimit = maxFetchLimit
	}

	// The FTS5 table `memory_fts` is external-content over `memory_index`, so
	// filter columns (scope/scope_id/type) and the display path live on
	// memory_index and the join is memory_index.id = memory_fts.rowid. snippet()
	// and bm25() operate on the FTS table; body is FTS column 0.
	var sb strings.Builder
	sb.WriteString(`
SELECT mi.path, mi.scope, mi.scope_id, mi.type,
       snippet(memory_fts, 0, '<<', '>>', '...', 32) AS snippet,
       bm25(memory_fts) AS score
FROM memory_fts
JOIN memory_index mi ON mi.id = memory_fts.rowid
WHERE memory_fts MATCH ?`)

	// MATCH parameter is always first; filter params follow in order.
	args := []any{match}
	if opts.Scope != "" {
		sb.WriteString(" AND mi.scope = ?")
		args = append(args, opts.Scope)
	}
	if opts.ScopeID != "" {
		sb.WriteString(" AND mi.scope_id = ?")
		args = append(args, opts.ScopeID)
	}
	if opts.Type != "" {
		sb.WriteString(" AND mi.type = ?")
		args = append(args, opts.Type)
	}
	// bm25(): lower = better, so ascending order puts the best hit first.
	sb.WriteString(" ORDER BY score ASC LIMIT ?")
	args = append(args, fetchLimit)

	rows, err := s.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("memory: search query: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var (
			path, scope, scopeID, typ string
			snippet                   sql.NullString
			bm25                      float64
		)
		if err := rows.Scan(&path, &scope, &scopeID, &typ, &snippet, &bm25); err != nil {
			return nil, fmt.Errorf("memory: scan search row: %w", err)
		}
		results = append(results, SearchResult{
			Path:    path,
			Snippet: snippet.String,
			Score:   -bm25, // convert to higher = better
			Scope:   Scope(scope),
			ScopeID: scopeID,
			Type:    Type(typ),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: iterate search rows: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	// Relative score floor. BM25 magnitudes are corpus-size dependent (in a
	// tiny corpus every score collapses toward 0 due to low IDF), so an
	// absolute floor would wrongly wipe real hits. We keep results scoring at
	// least topScore*floor. The #1 result is ALWAYS kept — a match is a match
	// even when BM25 can't discriminate. Default 0.15; a negative floor
	// disables the trimming entirely.
	floor := opts.ScoreFloor
	if floor == 0 {
		floor = defaultScoreFloor
	}

	// Rows come back ORDER BY score ASC (best first after negation), so
	// results[0] is the top hit.
	if floor > 0 {
		topScore := results[0].Score
		cutoff := topScore * floor
		kept := results[:1]
		for _, r := range results[1:] {
			if r.Score >= cutoff {
				kept = append(kept, r)
			}
		}
		results = kept
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
