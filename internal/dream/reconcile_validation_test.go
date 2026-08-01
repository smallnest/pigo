package dream

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/smallnest/pigo/internal/memory"
)

// searchHits reopens the memory store that Runner.Run built at
// <root>/index.db and runs a full-text query, returning the set of hit paths.
// The store is reconciled-on-open so the query reflects the exact on-disk state
// left behind by the dream writeback (US-009 / FR-15). The score floor is
// disabled so recall — not ranking — is what the assertion measures.
func searchHits(t *testing.T, root, query string) map[string]bool {
	t.Helper()
	st, err := memory.Open(filepath.Join(root, "index.db"), root, "")
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	res, err := st.Search(query, memory.SearchOptions{ScoreFloor: -1, Limit: 50, ReconcileFirst: true})
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	hits := make(map[string]bool, len(res))
	for _, r := range res {
		hits[filepath.Clean(r.Path)] = true
	}
	return hits
}

// TestReconcileConvergesAfterConsolidation is the US-009 / FR-15 acceptance
// test: after a dream writeback that merges some entries and prunes others,
// memory.Reconcile must rebuild the FTS index and memory_search must converge on
// the compacted current state — never returning the merged-away or pruned
// fragments, always returning the compacted entry.
//
// The scenario uses unique, made-up tokens per fragment so BM25 recall is
// unambiguous: each token exists in exactly one fragment before the run, and the
// assertions check that stale tokens vanish from the index while the compacted
// token appears.
func TestReconcileConvergesAfterConsolidation(t *testing.T) {
	root := t.TempDir()

	// Seed distinct (non-duplicate, path-ref-free) fragments so the deterministic
	// dedupe/path-clean passes are no-ops and the Consolidator drives the change.
	fragA := writeMemFile(t, root, "global/project/frag_a.md", "zorptholine legacy architecture fragment")
	fragB := writeMemFile(t, root, "global/project/frag_b.md", "wibblequux duplicate architecture note")
	fragC := writeMemFile(t, root, "global/project/frag_c.md", "frobnitz stale prunable outdated entry")
	keep := writeMemFile(t, root, "global/user/keep.md", "unrelated grocery shopping list")

	// Stub Consolidator: rewrite frag_a into the compacted current state, merge
	// frag_b away into it (deletion), and prune the stale frag_c (deletion).
	stub := &stubConsolidator{result: ConsolidateResult{
		MergedBodies: map[string]string{
			fragA: "quombalter consolidated current architecture state",
		},
		Deletions: []string{fragB, fragC},
		Merged:    1,
		Pruned:    1,
	}}

	r := &Runner{MemoryRoot: root, Consolidator: stub}
	rep, err := r.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !stub.called {
		t.Fatal("Consolidator was not called")
	}

	// (1) memory.Reconcile ran during writeback and indexed the surviving files.
	// The store's index.db is created fresh inside Run, so every kept file is a
	// new index row: Indexed must cover the compacted frag_a and the untouched
	// keep.md (>=2), proving the FTS index was rebuilt.
	if rep.Reconciled.Indexed < 2 {
		t.Fatalf("Reconciled.Indexed = %d, want >= 2 (rebuilt index over surviving files)", rep.Reconciled.Indexed)
	}
	if rep.Merged != 1 || rep.Pruned != 1 {
		t.Fatalf("counters not surfaced: Merged=%d Pruned=%d, want 1/1", rep.Merged, rep.Pruned)
	}

	// (2) Stale fragments must be gone from the index: neither the merged-away
	// fragment (frag_b), the pruned fragment (frag_c), nor the overwritten body of
	// frag_a ("zorptholine") may still be searchable.
	for _, token := range []string{"zorptholine", "wibblequux", "frobnitz"} {
		if hits := searchHits(t, root, token); len(hits) != 0 {
			t.Fatalf("stale fragment token %q still searchable after consolidation: %v", token, hits)
		}
	}

	// (3) The compacted current state IS searchable and resolves to the surviving
	// consolidated file (frag_a rewritten in place).
	hits := searchHits(t, root, "quombalter")
	if !hits[fragA] {
		t.Fatalf("compacted entry %q not returned by memory_search for its token, got %v", fragA, hits)
	}

	// The unrelated memory must be untouched and still indexed.
	if hits := searchHits(t, root, "grocery"); !hits[keep] {
		t.Fatalf("untouched memory %q missing from index after consolidation, got %v", keep, hits)
	}
}
