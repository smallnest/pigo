package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openTempWithRoots opens a Store backed by a temp-file DB with explicit mimo
// root and cc base directories (both created on disk).
func openTempWithRoots(t *testing.T) (st *Store, root, ccBase string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "mimo")
	ccBase = filepath.Join(base, "cc")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.MkdirAll(ccBase, 0o755); err != nil {
		t.Fatalf("mkdir ccBase: %v", err)
	}
	dbPath := filepath.Join(base, "sub", "memory.db")
	st, err := Open(dbPath, root, ccBase)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, root, ccBase
}

// writeFile writes body to a mimo path built from segments under root, creating
// parent directories. It returns the absolute path (cleaned).
func writeFile(t *testing.T, base string, body string, segs ...string) string {
	t.Helper()
	full := filepath.Join(append([]string{base}, segs...)...)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %q: %v", full, err)
	}
	return filepath.Clean(full)
}

// rowFor returns (scope, scopeId, type, fingerprint, body, found) for a path.
func rowFor(t *testing.T, st *Store, path string) (scope, scopeID, typ, fp, body string, found bool) {
	t.Helper()
	err := st.DB().QueryRow(
		`SELECT scope, scope_id, type, fingerprint, body FROM memory_index WHERE path = ?`, path,
	).Scan(&scope, &scopeID, &typ, &fp, &body)
	if err != nil {
		return "", "", "", "", "", false
	}
	return scope, scopeID, typ, fp, body, true
}

func countRows(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM memory_index`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// TestReconcileNewFileIndexed indexes a fresh mimo file and records its locator.
func TestReconcileNewFileIndexed(t *testing.T) {
	st, root, _ := openTempWithRoots(t)

	p := writeFile(t, root, "hello world", "global", "reference", "note.md")

	res, err := st.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Indexed != 1 || res.Pruned != 0 {
		t.Fatalf("Reconcile result = %+v, want {Indexed:1 Pruned:0}", res)
	}

	scope, scopeID, typ, fp, body, found := rowFor(t, st, p)
	if !found {
		t.Fatalf("row for %q not found", p)
	}
	if scope != string(ScopeGlobal) || scopeID != "" || typ != string(TypeReference) {
		t.Fatalf("row = scope=%q scope_id=%q type=%q, want global/''/reference", scope, scopeID, typ)
	}
	if body != "hello world" {
		t.Fatalf("body = %q, want %q", body, "hello world")
	}
	if fp == "" {
		t.Fatalf("fingerprint empty")
	}
}

// TestReconcileUnchangedFileHit re-runs reconcile with no changes and expects a
// fingerprint hit (no re-index).
func TestReconcileUnchangedFileHit(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	writeFile(t, root, "stable content", "global", "notes", "a.md")

	if res, err := st.Reconcile(); err != nil || res.Indexed != 1 {
		t.Fatalf("first Reconcile = %+v, err=%v, want Indexed:1", res, err)
	}
	res, err := st.Reconcile()
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if res.Indexed != 0 || res.Pruned != 0 {
		t.Fatalf("second Reconcile = %+v, want {Indexed:0 Pruned:0} (fingerprint hit)", res)
	}
}

// TestReconcileChangedFileReindexed bumps a file's size and mtime and expects a
// re-index.
func TestReconcileChangedFileReindexed(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	p := writeFile(t, root, "v1", "global", "notes", "a.md")

	if _, err := st.Reconcile(); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	_, _, _, fp1, _, _ := rowFor(t, st, p)

	// Rewrite with different size and force a later mtime to guarantee the
	// fingerprint changes regardless of filesystem timestamp resolution.
	if err := os.WriteFile(p, []byte("v2 longer body"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	res, err := st.Reconcile()
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if res.Indexed != 1 || res.Pruned != 0 {
		t.Fatalf("second Reconcile = %+v, want {Indexed:1 Pruned:0}", res)
	}
	_, _, _, fp2, body, _ := rowFor(t, st, p)
	if fp1 == fp2 {
		t.Fatalf("fingerprint unchanged after edit: %q", fp2)
	}
	if body != "v2 longer body" {
		t.Fatalf("body = %q, want re-indexed content", body)
	}
}

// TestReconcileDeletedFilePruned removes a file and expects its row pruned.
func TestReconcileDeletedFilePruned(t *testing.T) {
	st, root, _ := openTempWithRoots(t)
	p := writeFile(t, root, "temp", "global", "notes", "gone.md")

	if _, err := st.Reconcile(); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if countRows(t, st) != 1 {
		t.Fatalf("want 1 row after index")
	}

	if err := os.Remove(p); err != nil {
		t.Fatalf("remove: %v", err)
	}
	res, err := st.Reconcile()
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if res.Indexed != 0 || res.Pruned != 1 {
		t.Fatalf("second Reconcile = %+v, want {Indexed:0 Pruned:1}", res)
	}
	if _, _, _, _, _, found := rowFor(t, st, p); found {
		t.Fatalf("row for deleted file still present")
	}
}

// TestReconcileCcFrontmatterType indexes a cc-root file and derives its type
// from YAML frontmatter.
func TestReconcileCcFrontmatterType(t *testing.T) {
	st, _, ccBase := openTempWithRoots(t)

	const body = "---\nmetadata:\n  type: checkpoint\n---\ncc body text"
	// <ccBase>/<slug>/memory/**/*.md
	p := writeFile(t, ccBase, body, "my-project", "memory", "sub", "cp.md")

	res, err := st.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Indexed != 1 || res.Pruned != 0 {
		t.Fatalf("Reconcile = %+v, want {Indexed:1 Pruned:0}", res)
	}
	scope, scopeID, typ, _, _, found := rowFor(t, st, p)
	if !found {
		t.Fatalf("cc row not found")
	}
	if scope != string(ScopeCC) || scopeID != "my-project" || typ != string(TypeCheckpoint) {
		t.Fatalf("cc row = scope=%q scope_id=%q type=%q, want cc/my-project/checkpoint", scope, scopeID, typ)
	}
}

// TestReconcileBothRootsNoCrossPrune verifies that indexing both roots does not
// prune the other root's rows (the reconcile.ts correctness note).
func TestReconcileBothRootsNoCrossPrune(t *testing.T) {
	st, root, ccBase := openTempWithRoots(t)

	mimoP := writeFile(t, root, "mimo body", "projects", "proj1", "project", "m.md")
	ccP := writeFile(t, ccBase, "cc body", "slug1", "memory", "c.md")

	res, err := st.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Indexed != 2 || res.Pruned != 0 {
		t.Fatalf("Reconcile = %+v, want {Indexed:2 Pruned:0}", res)
	}
	if _, _, _, _, _, ok := rowFor(t, st, mimoP); !ok {
		t.Fatalf("mimo row missing")
	}
	if _, _, _, _, _, ok := rowFor(t, st, ccP); !ok {
		t.Fatalf("cc row missing")
	}

	// A second no-op reconcile must not prune either row.
	res, err = st.Reconcile()
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if res.Pruned != 0 {
		t.Fatalf("second Reconcile pruned %d, want 0", res.Pruned)
	}
	if countRows(t, st) != 2 {
		t.Fatalf("row count = %d, want 2", countRows(t, st))
	}
}

// TestReconcileMissingRoots verifies reconcile is a no-op when roots do not
// exist yet (ENOENT tolerated).
func TestReconcileMissingRoots(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "memory.db")
	st, err := Open(dbPath, filepath.Join(base, "nope"), filepath.Join(base, "nocc"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	res, err := st.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile on missing roots: %v", err)
	}
	if res.Indexed != 0 || res.Pruned != 0 {
		t.Fatalf("Reconcile = %+v, want zero", res)
	}
}
