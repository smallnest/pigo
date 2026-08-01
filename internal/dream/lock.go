package dream

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked is returned by AcquireLock when a live (non-stale) lock is already
// held by another process. Callers detect it (via errors.Is) to exit "skipped"
// rather than treating it as a real failure. It is deliberately distinct from
// the I/O errors AcquireLock may also return.
var ErrLocked = errors.New("dream: consolidation already running")

// DefaultStaleAfter is how long after a lock's started_at the lock is considered
// abandoned (e.g. the holder crashed) and may be taken over. See spec §5.4.
var DefaultStaleAfter = 30 * time.Minute

// lockInfo is the JSON body persisted in the lock file.
type lockInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// Lock represents an acquired dream single-instance lock backed by the file
// <memoryRoot>/global/dream/dream.lock. It guarantees at most one consolidation
// runs at a time across processes, with crash-safe stale takeover.
type Lock struct {
	path     string
	released bool
}

// lockPath is the lock file location under the memory root. It is a separate
// file from state.json and never touches it.
func lockPath(memoryRoot string) string {
	return filepath.Join(memoryRoot, "global", "dream", "dream.lock")
}

// AcquireLock attempts to acquire the dream single-instance lock under
// memoryRoot. On success it returns a *Lock the caller must Release (typically
// via defer). If a live lock is already held it returns ErrLocked. If the
// existing lock is stale (its started_at is older than now-DefaultStaleAfter) or
// malformed/unparseable, it is treated as abandoned and taken over. Any other
// error (permissions, unexpected I/O) is returned as-is so the caller can
// distinguish it from the ErrLocked "skipped" case.
func AcquireLock(memoryRoot string) (*Lock, error) {
	dir := filepath.Join(memoryRoot, "global", "dream")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := lockPath(memoryRoot)

	// First attempt: atomic exclusive create.
	l, err := createLock(path)
	if err == nil {
		return l, nil
	}
	if !os.IsExist(err) {
		// Real I/O error (permissions, etc.), not a contention signal.
		return nil, err
	}

	// A lock file already exists. Decide whether it is stale and takeable.
	if !staleLock(path, time.Now()) {
		return nil, ErrLocked
	}

	// Stale (or malformed) lock: take it over. Removing then re-creating with
	// O_EXCL keeps the create atomic. A racing process that recreates the file
	// between our Remove and create will cause our create to fail with EEXIST;
	// we surface that as ErrLocked (the other process won the race).
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	l, err = createLock(path)
	if err != nil {
		if os.IsExist(err) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return l, nil
}

// createLock atomically creates the lock file with O_EXCL and writes the current
// pid + start time as JSON. On EEXIST it returns an error for which os.IsExist
// is true.
func createLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_EXCL|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(lockInfo{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	if err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return &Lock{path: path}, nil
}

// staleLock reports whether the lock file at path is stale (takeable) as of now.
// A lock is stale when its started_at is older than now-DefaultStaleAfter. A
// missing, unreadable, or malformed/unparseable lock file is also treated as
// stale so a corrupt lock never wedges dream permanently.
func staleLock(path string, now time.Time) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		// Missing or unreadable: treat as takeable.
		return true
	}
	var info lockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		// Malformed lock body: treat as stale.
		return true
	}
	if info.StartedAt.IsZero() {
		// No usable timestamp: treat as stale.
		return true
	}
	return now.Sub(info.StartedAt) > DefaultStaleAfter
}

// Release removes the lock file. It is safe to call in a defer and safe to
// double-call: a second call (or a call after the file was already removed) is a
// no-op and never panics. A missing file is not an error.
func (l *Lock) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
