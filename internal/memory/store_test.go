package memory

import (
	"path/filepath"
	"testing"
)

// openTemp opens a Store backed by a temp-file DB and registers cleanup.
func openTemp(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "memory.db") // sub/ must be created by Open
	st, err := Open(dbPath, dir, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// tableExists reports whether a table or virtual table with the given name
// exists in sqlite_master.
func tableExists(t *testing.T, st *Store, name string) bool {
	t.Helper()
	var got string
	err := st.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&got)
	if err != nil {
		return false
	}
	return got == name
}

// TestFTS5SmokeTest de-risks the pure-Go SQLite FTS5 dependency: it asserts the
// FTS5 virtual table is created without error and is queryable.
func TestFTS5SmokeTest(t *testing.T) {
	st := openTemp(t)

	if !tableExists(t, st, "memory_fts") {
		t.Fatalf("memory_fts virtual table not created")
	}

	// A MATCH query must run without error (proves FTS5 is compiled in).
	rows, err := st.DB().Query(`SELECT rowid FROM memory_fts WHERE memory_fts MATCH ?`, "anything")
	if err != nil {
		t.Fatalf("FTS5 MATCH query failed (FTS5 not available?): %v", err)
	}
	rows.Close()
}

// TestSchemaObjectsCreated verifies the content table, indexes and triggers.
func TestSchemaObjectsCreated(t *testing.T) {
	st := openTemp(t)

	if !tableExists(t, st, "memory_index") {
		t.Fatalf("memory_index table not created")
	}

	for _, idx := range []string{"memory_index_scope_idx", "memory_index_type_idx"} {
		var n int
		if err := st.DB().QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %q missing (n=%d, err=%v)", idx, n, err)
		}
	}

	for _, trg := range []string{"memory_ai", "memory_ad", "memory_au"} {
		var n int
		if err := st.DB().QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trg,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("trigger %q missing (n=%d, err=%v)", trg, n, err)
		}
	}
}

// insertRow inserts a memory_index row and returns its id.
func insertRow(t *testing.T, st *Store, path, body string) int64 {
	t.Helper()
	res, err := st.DB().Exec(
		`INSERT INTO memory_index (path, scope, scope_id, type, body, fingerprint, last_indexed_at)
		 VALUES (?, 'global', '', 'free', ?, 'fp', 0)`, path, body)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

// ftsMatchCount returns how many FTS rows match the given single-term query.
func ftsMatchCount(t *testing.T, st *Store, term string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(
		`SELECT count(*) FROM memory_fts WHERE memory_fts MATCH ?`, term,
	).Scan(&n); err != nil {
		t.Fatalf("fts match count %q: %v", term, err)
	}
	return n
}

// TestTriggerSyncInsertDeleteUpdate verifies the AFTER INSERT/DELETE/UPDATE
// triggers keep memory_fts in sync with memory_index.
func TestTriggerSyncInsertDeleteUpdate(t *testing.T) {
	st := openTemp(t)

	// INSERT -> searchable.
	id := insertRow(t, st, "/mem/a.md", "alpha bravo charlie")
	if got := ftsMatchCount(t, st, "bravo"); got != 1 {
		t.Fatalf("after insert: MATCH bravo = %d, want 1", got)
	}

	// UPDATE -> re-synced (old term gone, new term present).
	if _, err := st.DB().Exec(`UPDATE memory_index SET body=? WHERE id=?`, "delta echo", id); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := ftsMatchCount(t, st, "bravo"); got != 0 {
		t.Fatalf("after update: MATCH bravo = %d, want 0", got)
	}
	if got := ftsMatchCount(t, st, "echo"); got != 1 {
		t.Fatalf("after update: MATCH echo = %d, want 1", got)
	}

	// DELETE -> removed from index.
	if _, err := st.DB().Exec(`DELETE FROM memory_index WHERE id=?`, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := ftsMatchCount(t, st, "echo"); got != 0 {
		t.Fatalf("after delete: MATCH echo = %d, want 0", got)
	}
}

// TestOpenIdempotent verifies that running the migration twice on the same file
// is safe and preserves data.
func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")

	st1, err := Open(dbPath, dir, "")
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	insertRow(t, st1, "/mem/keep.md", "persistent needle")
	if err := st1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	// Re-open: migration must not error and existing data must survive.
	st2, err := Open(dbPath, dir, "")
	if err != nil {
		t.Fatalf("second Open (migration not idempotent?): %v", err)
	}
	t.Cleanup(func() { st2.Close() })

	if got := ftsMatchCount(t, st2, "needle"); got != 1 {
		t.Fatalf("after reopen: MATCH needle = %d, want 1", got)
	}
}

// TestInMemoryOpen verifies the ":memory:" DSN works (no parent dir creation).
func TestInMemoryOpen(t *testing.T) {
	st, err := Open(":memory:", "", "")
	if err != nil {
		t.Fatalf("Open in-memory: %v", err)
	}
	defer st.Close()
	if !tableExists(t, st, "memory_fts") {
		t.Fatalf("memory_fts not created in in-memory db")
	}
}
