package dream

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeLock writes a lock file with the given pid and started_at directly, for
// tests that need to simulate a pre-existing (possibly stale) lock.
func writeLock(t *testing.T, root string, pid int, startedAt time.Time) string {
	t.Helper()
	dir := filepath.Join(root, "global", "dream")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dream.lock")
	data, err := json.Marshal(lockInfo{PID: pid, StartedAt: startedAt})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAcquireLockMutualExclusion(t *testing.T) {
	root := t.TempDir()
	l1, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer l1.Release()

	// A second acquire while the first is live must fail with ErrLocked.
	l2, err := AcquireLock(root)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock err = %v, want ErrLocked", err)
	}
	if l2 != nil {
		t.Errorf("second AcquireLock returned non-nil Lock alongside error")
	}
}

func TestAcquireLockCreatesFileWithBody(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer l.Release()

	path := filepath.Join(root, "global", "dream", "dream.lock")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	var info lockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("lock body not valid JSON: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("lock pid = %d, want %d", info.PID, os.Getpid())
	}
	if info.StartedAt.IsZero() {
		t.Errorf("lock started_at is zero, want current time")
	}
}

func TestAcquireLockDoesNotTouchState(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer l.Release()
	if _, err := os.Stat(filepath.Join(root, "global", "dream", "state.json")); !os.IsNotExist(err) {
		t.Errorf("AcquireLock created/left state.json (err=%v); lock must be a separate file", err)
	}
}

func TestAcquireLockStaleTakeover(t *testing.T) {
	root := t.TempDir()
	// Existing lock older than staleAfter → takeable.
	writeLock(t, root, 99999, time.Now().Add(-DefaultStaleAfter-time.Minute))

	l, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock over stale lock err = %v, want takeover success", err)
	}
	defer l.Release()

	// The lock body should now reflect our pid.
	data, err := os.ReadFile(filepath.Join(root, "global", "dream", "dream.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var info lockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("after takeover pid = %d, want %d (our pid)", info.PID, os.Getpid())
	}
}

func TestAcquireLockFreshLockNotTakeable(t *testing.T) {
	root := t.TempDir()
	// A recently-started lock is live, not stale.
	writeLock(t, root, 99999, time.Now().Add(-time.Minute))

	if _, err := AcquireLock(root); !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireLock over fresh lock err = %v, want ErrLocked", err)
	}
}

func TestAcquireLockMalformedTakeover(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "global", "dream")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dream.lock"), []byte("{garbage not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock over malformed lock err = %v, want takeover success", err)
	}
	defer l.Release()
}

func TestReleaseAndReacquire(t *testing.T) {
	root := t.TempDir()
	l1, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	if err := l1.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// File must be gone after release.
	if _, err := os.Stat(filepath.Join(root, "global", "dream", "dream.lock")); !os.IsNotExist(err) {
		t.Errorf("lock file still present after Release (err=%v)", err)
	}
	// Re-acquire must succeed now that the lock is free.
	l2, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	defer l2.Release()
}

func TestReleaseDoubleCallSafe(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	// Second Release (and even after the file is gone) must not panic or error.
	if err := l.Release(); err != nil {
		t.Errorf("second Release err = %v, want nil", err)
	}
}

func TestReleaseNilSafe(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Errorf("nil Lock Release err = %v, want nil", err)
	}
}

func TestStaleLockBoundary(t *testing.T) {
	root := t.TempDir()
	path := writeLock(t, root, 1, time.Unix(0, 0).UTC())
	now := time.Unix(0, 0).UTC()
	if staleLock(path, now.Add(DefaultStaleAfter-time.Second)) {
		t.Errorf("lock within staleAfter reported stale")
	}
	if !staleLock(path, now.Add(DefaultStaleAfter+time.Second)) {
		t.Errorf("lock past staleAfter reported not stale")
	}
}
