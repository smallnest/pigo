package memory

import "testing"

// seedSearchCorpus writes a small memory corpus and indexes it. "checkpoint" is
// deliberately a common word (appears in many docs) while "permission" and
// "deadlock" are rare, so BM25 + the relative floor can be exercised.
func seedSearchCorpus(t *testing.T, st *Store, root string) map[string]string {
	t.Helper()
	paths := map[string]string{}
	paths["rare"] = writeFile(t, root,
		"permission deadlock encountered during checkpoint save then retry succeeded",
		"projects", "proj1", "notes", "rare.md")
	paths["c1"] = writeFile(t, root, "checkpoint state alpha", "global", "checkpoint", "c1.md")
	paths["c2"] = writeFile(t, root, "checkpoint state beta", "global", "checkpoint", "c2.md")
	paths["c3"] = writeFile(t, root, "checkpoint state gamma", "global", "checkpoint", "c3.md")
	paths["c4"] = writeFile(t, root, "checkpoint state delta", "global", "checkpoint", "c4.md")
	paths["user"] = writeFile(t, root, "unrelated grocery shopping list", "global", "user", "u1.md")
	if _, err := st.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return paths
}

func resultPaths(rs []SearchResult) map[string]bool {
	m := make(map[string]bool, len(rs))
	for _, r := range rs {
		m[r.Path] = true
	}
	return m
}

func TestSearchMultiWordOrRecall(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	p := seedSearchCorpus(t, st, root)

	// OR recall: a query spanning a rare word (in one doc) and the common word
	// (in several) should surface docs matching either. Disable the floor so we
	// verify raw OR recall independent of trimming.
	res, err := st.Search("permission checkpoint", SearchOptions{ScoreFloor: -1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := resultPaths(res)
	if !got[p["rare"]] {
		t.Fatalf("expected rare doc in recall results, got %v", got)
	}
	if !got[p["c1"]] {
		t.Fatalf("expected a checkpoint doc in recall results, got %v", got)
	}
}

func TestSearchScoreFloorDropsCommonWordOnly(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	p := seedSearchCorpus(t, st, root)

	// The rare doc matches permission+deadlock+checkpoint; the c* docs match
	// only the common "checkpoint". With the default floor the multi-rare doc
	// ranks top and the common-word-only docs are trimmed.
	res, err := st.Search("permission deadlock checkpoint", SearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("expected at least the top result")
	}
	if res[0].Path != p["rare"] {
		t.Fatalf("expected rare doc ranked top, got %q", res[0].Path)
	}
	// Higher = better after negation: the top score should be positive-most.
	for _, r := range res[1:] {
		if r.Score > res[0].Score {
			t.Fatalf("result %q outscored the top hit", r.Path)
		}
	}
	got := resultPaths(res)
	for _, key := range []string{"c1", "c2", "c3", "c4"} {
		if got[p[key]] {
			t.Fatalf("common-word-only doc %s should have been dropped by floor, got %v", key, got)
		}
	}
}

func TestSearchScopeAndTypeFilters(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	p := seedSearchCorpus(t, st, root)

	// scope filter: only the projects doc should match under scope=projects.
	res, err := st.Search("checkpoint permission", SearchOptions{Scope: "projects", ScoreFloor: -1})
	if err != nil {
		t.Fatalf("Search scope: %v", err)
	}
	got := resultPaths(res)
	if !got[p["rare"]] || len(got) != 1 {
		t.Fatalf("scope=projects should return only the rare doc, got %v", got)
	}

	// type filter: only global/checkpoint docs, none of the projects/user docs.
	res, err = st.Search("checkpoint permission", SearchOptions{Type: string(TypeCheckpoint), ScoreFloor: -1})
	if err != nil {
		t.Fatalf("Search type: %v", err)
	}
	got = resultPaths(res)
	if got[p["rare"]] || got[p["user"]] {
		t.Fatalf("type=checkpoint should exclude non-checkpoint docs, got %v", got)
	}
	if !got[p["c1"]] {
		t.Fatalf("type=checkpoint should include checkpoint docs, got %v", got)
	}
}

func TestSearchEmptyQueryReturnsNil(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	seedSearchCorpus(t, st, root)

	for _, q := range []string{"", "   ", "!!! ??? ---"} {
		res, err := st.Search(q, SearchOptions{})
		if err != nil {
			t.Fatalf("Search(%q): unexpected error %v", q, err)
		}
		if res != nil {
			t.Fatalf("Search(%q): expected nil results, got %v", q, res)
		}
	}
}

func TestSearchReconcileFirst(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	// Write a file but do NOT reconcile manually; ReconcileFirst should index it.
	writeFile(t, root, "lazy reconciled permission deadlock content", "global", "notes", "lazy.md")

	res, err := st.Search("permission deadlock", SearchOptions{ReconcileFirst: true})
	if err != nil {
		t.Fatalf("Search ReconcileFirst: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("ReconcileFirst should have indexed and matched the lazy doc")
	}
}

func TestSearchLimit(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	seedSearchCorpus(t, st, root)

	res, err := st.Search("checkpoint", SearchOptions{Limit: 2, ScoreFloor: -1})
	if err != nil {
		t.Fatalf("Search limit: %v", err)
	}
	if len(res) > 2 {
		t.Fatalf("limit=2 should cap results, got %d", len(res))
	}
}
