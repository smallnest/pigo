package dream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/memory"
)

func TestUpdateScopeIndexesDropsDanglingLinks(t *testing.T) {
	root := t.TempDir()
	// A global MEMORY.md linking to two entries; b.md will be deleted.
	writeMemFile(t, root, "global/user/a.md", "keep me")
	b := writeMemFile(t, root, "global/user/b.md", "remove me")
	idx := writeMemFile(t, root, "global/MEMORY.md",
		"# Index\n- [a](user/a.md)\n- [b](user/b.md)\n- freeform note\n")

	deleted := map[string]struct{}{filepath.Clean(b): {}}
	if err := updateScopeIndexes(root, "", deleted); err != nil {
		t.Fatalf("updateScopeIndexes: %v", err)
	}
	raw, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Contains(got, "user/b.md") || strings.Contains(got, "b.md") {
		t.Fatalf("dangling link to b.md not removed:\n%s", got)
	}
	if !strings.Contains(got, "user/a.md") {
		t.Fatalf("live link to a.md wrongly removed:\n%s", got)
	}
	if !strings.Contains(got, "freeform note") {
		t.Fatalf("unrelated line wrongly removed:\n%s", got)
	}
}

// TestUpdateScopeIndexesBoundaryMatch: deleting b.md must not drop an index line
// referencing a different entry whose name merely contains "b.md" as a
// substring (e.g. club.md).
func TestUpdateScopeIndexesBoundaryMatch(t *testing.T) {
	root := t.TempDir()
	writeMemFile(t, root, "global/user/club.md", "keep me")
	b := writeMemFile(t, root, "global/user/b.md", "remove me")
	idx := writeMemFile(t, root, "global/MEMORY.md",
		"- [club](user/club.md)\n- [b](user/b.md)\n")

	deleted := map[string]struct{}{filepath.Clean(b): {}}
	if err := updateScopeIndexes(root, "", deleted); err != nil {
		t.Fatalf("updateScopeIndexes: %v", err)
	}
	got, _ := os.ReadFile(idx)
	if !strings.Contains(string(got), "user/club.md") {
		t.Fatalf("club.md link wrongly removed by substring match:\n%s", got)
	}
	if strings.Contains(string(got), "user/b.md") {
		t.Fatalf("b.md link not removed:\n%s", got)
	}
}

func TestUpdateScopeIndexesNoIndexIsNoOp(t *testing.T) {
	root := t.TempDir()
	writeMemFile(t, root, "global/user/a.md", "x")
	deleted := map[string]struct{}{filepath.Join(root, "global", "user", "a.md"): {}}
	if err := updateScopeIndexes(root, "", deleted); err != nil {
		t.Fatalf("updateScopeIndexes with no MEMORY.md should be a no-op, got %v", err)
	}
}

// TestRunEndToEndWithMerge exercises the full non-dry-run write path with a stub
// Consolidator that merges b.md into a.md and prunes c.md: files converge on
// disk, the MEMORY.md index loses its dangling links, Reconcile runs, the Report
// counters are correct, and a full-text search no longer hits the merged-away
// fragment (US-003 / US-006 / US-009).
func TestRunEndToEndWithMerge(t *testing.T) {
	root := t.TempDir()
	a := writeMemFile(t, root, "global/user/a.md", "shared topic original a")
	b := writeMemFile(t, root, "global/user/b.md", "shared topic zebrafragment only in b")
	c := writeMemFile(t, root, "global/user/c.md", "outdated standalone note")
	idx := writeMemFile(t, root, "global/MEMORY.md",
		"# Index\n- [a](user/a.md)\n- [b](user/b.md)\n- [c](user/c.md)\n")

	stub := &stubConsolidator{result: ConsolidateResult{
		MergedBodies: map[string]string{a: "shared topic merged and current"},
		Deletions:    []string{b, c},
		Merged:       1,
		Pruned:       1,
		Notes:        []string{"pruned c: outdated"},
	}}
	r := &Runner{MemoryRoot: root, Consolidator: stub}
	rep, err := r.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !stub.called {
		t.Fatal("Consolidator not called")
	}
	if rep.Merged != 1 || rep.Pruned != 1 {
		t.Fatalf("counters wrong: %+v", rep)
	}
	// a.md rewritten, b.md + c.md gone.
	if got, _ := os.ReadFile(a); string(got) != "shared topic merged and current" {
		t.Fatalf("a.md not rewritten: %q", got)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatal("b.md should be deleted")
	}
	if _, err := os.Stat(c); !os.IsNotExist(err) {
		t.Fatal("c.md should be deleted")
	}
	// MEMORY.md no longer links to the removed entries.
	rawIdx, _ := os.ReadFile(idx)
	if strings.Contains(string(rawIdx), "b.md") || strings.Contains(string(rawIdx), "c.md") {
		t.Fatalf("MEMORY.md retains dangling links:\n%s", rawIdx)
	}
	if !strings.Contains(string(rawIdx), "a.md") {
		t.Fatalf("MEMORY.md lost the live a.md link:\n%s", rawIdx)
	}
	// Reconcile ran and indexed the surviving files.
	if rep.Reconciled.Indexed == 0 {
		t.Fatalf("Reconcile did not index: %+v", rep.Reconciled)
	}
	if rep.FilesAfter != 2 { // a.md + MEMORY.md
		t.Fatalf("FilesAfter = %d, want 2", rep.FilesAfter)
	}

	// The merged-away fragment must no longer be searchable.
	store, err := memory.Open(filepath.Join(root, "index.db"), root, "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	hits, err := store.Search("zebrafragment", memory.SearchOptions{ReconcileFirst: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("merged-away fragment still searchable: %+v", hits)
	}
}
