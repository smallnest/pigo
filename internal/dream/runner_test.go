package dream

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubConsolidator returns a fixed result and records that it was called.
type stubConsolidator struct {
	result ConsolidateResult
	called bool
}

func (s *stubConsolidator) Consolidate(context.Context, ConsolidateInput) (ConsolidateResult, error) {
	s.called = true
	return s.result, nil
}

// TestRunEmptyMemoryDir: an empty memory dir yields an all-zero Report with
// status ok (no error). Reconcile tolerates the missing scopes.
func TestRunEmptyMemoryDir(t *testing.T) {
	root := t.TempDir()
	r := &Runner{MemoryRoot: root}
	rep, err := r.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.FilesBefore != 0 || rep.BytesBefore != 0 || rep.FilesAfter != 0 || rep.BytesAfter != 0 {
		t.Fatalf("expected all-zero report, got %+v", rep)
	}
	if rep.Deduped != 0 || rep.Merged != 0 || rep.Pruned != 0 || rep.PathsCleaned != 0 {
		t.Fatalf("expected zero counters, got %+v", rep)
	}
	// State should be written with status ok for a non-dry-run.
	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.LastStatus != "ok" {
		t.Fatalf("LastStatus = %q, want ok", st.LastStatus)
	}
}

// TestRunDryRunWritesNothing: dry-run computes counts, writes no files, does not
// update state, but still acquires + releases the lock.
func TestRunDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	// Two byte-identical files → one dedupe candidate.
	a := writeMemFile(t, root, "global/user/a.md", "same content")
	b := writeMemFile(t, root, "global/user/b.md", "same content")

	r := &Runner{MemoryRoot: root}
	rep, err := r.Run(context.Background(), RunOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.DryRun {
		t.Fatal("Report.DryRun = false, want true")
	}
	if rep.Deduped != 1 {
		t.Fatalf("Deduped = %d, want 1 (predicted)", rep.Deduped)
	}
	// Both files must still exist — dry-run writes nothing.
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("file a removed in dry-run: %v", err)
	}
	if _, err := os.Stat(b); err != nil {
		t.Fatalf("file b removed in dry-run: %v", err)
	}
	// State must NOT be updated (never-run remains).
	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !st.LastRunAt.IsZero() || st.LastStatus != "" {
		t.Fatalf("dry-run updated state: %+v", st)
	}
	// Lock must have been released (a fresh acquire succeeds).
	lk, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("lock not released after dry-run: %v", err)
	}
	lk.Release()
}

// TestRunLockedSkips: when a live lock is already held, Run returns a zero-count
// report and NO error (exit-0 "skipped" semantics), and does not touch state.
func TestRunLockedSkips(t *testing.T) {
	root := t.TempDir()
	writeMemFile(t, root, "global/user/a.md", "content")

	held, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("pre-acquire lock: %v", err)
	}
	defer held.Release()

	r := &Runner{MemoryRoot: root}
	rep, err := r.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("Run under held lock should not error, got %v", err)
	}
	if rep.FilesBefore != 0 || rep.Deduped != 0 {
		t.Fatalf("skipped run should have zero report, got %+v", rep)
	}
	// State untouched (never ran).
	st, _ := LoadState(root)
	if !st.LastRunAt.IsZero() || st.LastStatus != "" {
		t.Fatalf("skipped run touched state: %+v", st)
	}
}

// TestRunAppliesDedupe: a non-dry-run removes exact duplicates, updates state,
// and reflects counts in the Report.
func TestRunAppliesDedupe(t *testing.T) {
	root := t.TempDir()
	a := writeMemFile(t, root, "global/user/a.md", "same content")
	b := writeMemFile(t, root, "global/user/b.md", "same content")

	r := &Runner{MemoryRoot: root}
	rep, err := r.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Deduped != 1 {
		t.Fatalf("Deduped = %d, want 1", rep.Deduped)
	}
	// Exactly one of the two duplicates must survive (the sorted-first: a.md).
	_, errA := os.Stat(a)
	_, errB := os.Stat(b)
	if errA != nil {
		t.Fatalf("representative a.md removed: %v", errA)
	}
	if errB == nil {
		t.Fatal("duplicate b.md should have been removed")
	}
	if rep.FilesAfter != 1 {
		t.Fatalf("FilesAfter = %d, want 1", rep.FilesAfter)
	}
	st, _ := LoadState(root)
	if st.LastStatus != "ok" || st.LastReport == nil {
		t.Fatalf("state not updated: %+v", st)
	}
}

// TestWithinScope: the path-boundary guard accepts in-scope targets and rejects
// everything outside <memoryRoot>/global and the active project's directory.
func TestWithinScope(t *testing.T) {
	root := t.TempDir()
	projectDir := t.TempDir()
	pid := projectID(projectDir)
	otherPID := "0123456789ab" // a different, unrelated project id
	cases := []struct {
		name    string
		project string
		target  string
		want    bool
	}{
		{"global file", projectDir, filepath.Join(root, "global", "user", "x.md"), true},
		{"active project file", projectDir, filepath.Join(root, "projects", pid, "notes", "y.md"), true},
		{"global root itself", projectDir, filepath.Join(root, "global"), true},
		{"unrelated project rejected", projectDir, filepath.Join(root, "projects", otherPID, "z.md"), false},
		{"any project rejected when global-only", "", filepath.Join(root, "projects", pid, "y.md"), false},
		{"global still ok when global-only", "", filepath.Join(root, "global", "x.md"), true},
		{"sessions scope rejected", projectDir, filepath.Join(root, "sessions", "s1", "checkpoint.md"), false},
		{"outside root rejected", projectDir, filepath.Join(root, "..", "evil.md"), false},
		{"sibling prefix not confused", projectDir, filepath.Join(root, "globalX", "z.md"), false},
		{"absolute escape rejected", projectDir, "/etc/passwd", false},
		{"empty target rejected", projectDir, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinScope(root, tc.project, tc.target); got != tc.want {
				t.Fatalf("withinScope(%q, %q, %q) = %v, want %v", root, tc.project, tc.target, got, tc.want)
			}
		})
	}
	if withinScope("", projectDir, filepath.Join(root, "global", "x.md")) {
		t.Fatal("empty memoryRoot must reject")
	}
}

// TestWithinScopeSymlinkEscape: a symlink planted inside an allowed scope must
// not let a target escape memoryRoot. The guard resolves symlinks on existing
// ancestors before the containment check (SPEC §7.1 defense-in-depth).
func TestWithinScopeSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // a directory fully outside memoryRoot

	globalDir := filepath.Join(root, "global")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	// <root>/global/escape -> <outside>
	link := filepath.Join(globalDir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// Lexically this looks in-scope (<root>/global/escape/evil.md) but resolves
	// to <outside>/evil.md, which must be rejected.
	target := filepath.Join(link, "evil.md")
	if withinScope(root, "", target) {
		t.Fatalf("symlink escape target accepted: %q", target)
	}
}

// TestRunPathClean: a memory file referencing a non-existent local path has that
// reference stripped and counted.
func TestRunPathClean(t *testing.T) {
	root := t.TempDir()
	proj := t.TempDir()
	missing := filepath.Join(proj, "does", "not", "exist.go")
	body := "See `" + missing + "` for details."
	f := writeMemFile(t, root, "global/reference/r.md", body)

	r := &Runner{MemoryRoot: root}
	rep, err := r.Run(context.Background(), RunOptions{ProjectDir: proj})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.PathsCleaned != 1 {
		t.Fatalf("PathsCleaned = %d, want 1", rep.PathsCleaned)
	}
	raw, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("read cleaned file: %v", err)
	}
	if got := string(raw); got == body {
		t.Fatalf("body unchanged after path-clean: %q", got)
	}
}

// TestRunConsolidatorApplied: an injected Consolidator's new-entry write and
// counters flow through, and the write lands within scope.
func TestRunConsolidatorApplied(t *testing.T) {
	root := t.TempDir()
	writeMemFile(t, root, "global/user/a.md", "hello")

	newPath := filepath.Join(root, "global", "user", "distilled.md")
	stub := &stubConsolidator{result: ConsolidateResult{
		NewEntries: []NewEntry{{Path: newPath, Body: "distilled fact"}},
		Distilled:  1,
		Merged:     2,
		Pruned:     3,
		Notes:      []string{"note"},
	}}
	r := &Runner{MemoryRoot: root, Consolidator: stub}
	rep, err := r.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !stub.called {
		t.Fatal("Consolidator was not called")
	}
	if rep.Distilled != 1 || rep.Merged != 2 || rep.Pruned != 3 {
		t.Fatalf("counters not surfaced: %+v", rep)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new entry not written: %v", err)
	}
}

// TestApplyConsolidationRejectsOutOfScope: a Consolidator that tries to write
// outside the memory root is rejected by the guard.
func TestApplyConsolidationRejectsOutOfScope(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "escape.md")
	cres := ConsolidateResult{
		NewEntries: []NewEntry{{Path: outside, Body: "x"}},
	}
	if err := applyConsolidation(root, "", cres, nil); err == nil {
		t.Fatal("expected out-of-scope write to be rejected")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("out-of-scope file must not be created")
	}
}

// TestRunDryRunLeavesStaleLockTakeable is a small guard that dry-run's lock is
// released promptly (no leftover live lock).
func TestRunDryRunLockReleased(t *testing.T) {
	root := t.TempDir()
	r := &Runner{MemoryRoot: root}
	if _, err := r.Run(context.Background(), RunOptions{DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Fresh acquire must succeed immediately (lock released, not merely stale).
	// Force a long stale window so a leftover *live* lock would block acquisition,
	// proving Run released it rather than leaving it to be reclaimed as stale.
	// Restore the package default afterward so this does not leak into other tests.
	orig := DefaultStaleAfter
	DefaultStaleAfter = time.Hour
	defer func() { DefaultStaleAfter = orig }()
	lk, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	lk.Release()
}
