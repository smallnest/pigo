package memory

import (
	"testing"
)

// TestCountByScopeEmpty returns an empty map for a fresh store with no entries.
func TestCountByScopeEmpty(t *testing.T) {
	st := openTemp(t)
	counts, err := st.CountByScope()
	if err != nil {
		t.Fatalf("CountByScope: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("counts = %v, want empty", counts)
	}
}

// TestCountByScopeGroups reconciles files across scopes and asserts the counts
// are grouped per scope.
func TestCountByScopeGroups(t *testing.T) {
	st, root, _ := openTempWithRoots(t)

	writeFile(t, root, "g1", "global", "reference", "a.md")
	writeFile(t, root, "g2", "global", "notes", "b.md")
	writeFile(t, root, "p1", "projects", "proj1", "project", "m.md")

	if _, err := st.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	counts, err := st.CountByScope()
	if err != nil {
		t.Fatalf("CountByScope: %v", err)
	}
	if counts[ScopeGlobal] != 2 {
		t.Fatalf("global count = %d, want 2 (counts=%v)", counts[ScopeGlobal], counts)
	}
	if counts[ScopeProjects] != 1 {
		t.Fatalf("projects count = %d, want 1 (counts=%v)", counts[ScopeProjects], counts)
	}
	if _, ok := counts[ScopeSessions]; ok {
		t.Fatalf("sessions should be absent, got %d", counts[ScopeSessions])
	}
}

// TestCountByScopeNilStore is safe on a nil store and yields an empty map.
func TestCountByScopeNilStore(t *testing.T) {
	var st *Store
	counts, err := st.CountByScope()
	if err != nil {
		t.Fatalf("CountByScope on nil: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("counts = %v, want empty", counts)
	}
}
