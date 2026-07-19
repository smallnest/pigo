// Package trust persists per-directory trust decisions so pigo can avoid running
// side-effect tools (bash/write/edit) in directories the user has not trusted
// (US-018, #134). Decisions are stored as a JSON map of directory path to a
// nullable boolean: true = trusted, false = untrusted, null/absent = undecided.
//
// The package is intentionally free of any REPL or tool-execution concerns: it
// only loads, queries, and persists decisions. The interactive prompt and the
// permission-hook integration live in cmd/pigo.
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Decision is the tri-state trust value for a directory.
type Decision int

const (
	// Undecided means no decision is saved for the directory (or an explicit
	// null entry). The caller should prompt the user and treat side-effect
	// tools as requiring confirmation.
	Undecided Decision = iota
	// Trusted means side-effect tools may run without confirmation.
	Trusted
	// Untrusted means side-effect tools require confirmation.
	Untrusted
)

// String returns a human-readable label for the decision.
func (d Decision) String() string {
	switch d {
	case Trusted:
		return "trusted"
	case Untrusted:
		return "untrusted"
	default:
		return "undecided"
	}
}

// decisionFromBool maps a saved nullable boolean to a Decision. A nil pointer
// (JSON null, or an absent entry) is Undecided.
func decisionFromBool(b *bool) Decision {
	if b == nil {
		return Undecided
	}
	if *b {
		return Trusted
	}
	return Untrusted
}

// boolFromDecision maps a Decision to the nullable boolean persisted to disk.
// Undecided maps to nil so the JSON value is null, preserving the
// "path -> bool|null" schema even for an explicitly-recorded undecided entry.
func boolFromDecision(d Decision) *bool {
	switch d {
	case Trusted:
		v := true
		return &v
	case Untrusted:
		v := false
		return &v
	default:
		return nil
	}
}

// Result is the outcome of a nearest-decision lookup.
type Result struct {
	// Decision is the nearest saved decision, or Undecided when none is found.
	Decision Decision
	// Path is the directory whose saved decision applies (the nearest ancestor
	// of cwd with an entry, inclusive of cwd itself). Empty when no entry was
	// found anywhere up the tree.
	Path string
	// Found reports whether any entry (true/false/null) existed for cwd or an
	// ancestor. When false, Decision is Undecided and the caller should prompt
	// the user for a fresh decision.
	Found bool
}

// Manager loads and persists trust decisions to a JSON file (path -> *bool).
// The zero value is not usable; construct with NewManager. It is safe for
// concurrent use: every method takes the manager mutex.
type Manager struct {
	path string
	mu   sync.Mutex
	data map[string]*bool
	// session marks directories trusted for the current process only ("just
	// once"). It is never persisted and is consulted by IsTrusted before the
	// on-disk data, so a one-shot grant takes effect immediately.
	session map[string]bool
}

// DefaultPath returns the trust file location: $PIGO_HOME/trust.json, or
// ~/.pigo/trust.json when PIGO_HOME is unset. It returns "" when the home
// directory cannot be resolved and no override is set, so a caller can treat
// trust as disabled rather than guessing a path.
func DefaultPath() string {
	if dir := os.Getenv("PIGO_HOME"); dir != "" {
		return filepath.Join(dir, "trust.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pigo", "trust.json")
}

// NewManager loads the trust file at path. A missing file is not an error: the
// manager starts empty and the file is created lazily on the first SetDecision
// / Forget. A present-but-malformed file is a hard error so a corrupted trust
// store is surfaced rather than silently overwritten.
func NewManager(path string) (*Manager, error) {
	m := &Manager{
		path:    path,
		data:    make(map[string]*bool),
		session: make(map[string]bool),
	}
	if path == "" {
		return m, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("trust: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m.data); err != nil {
		return nil, fmt.Errorf("trust: parse %s: %w", path, err)
	}
	if m.data == nil {
		m.data = make(map[string]*bool)
	}
	return m, nil
}

// walkUp returns the directory chain from cwd (inclusive) up to the filesystem
// root, in nearest-first order. Paths are cleaned. An empty cwd yields no
// entries.
func walkUp(cwd string) []string {
	cwd = filepath.Clean(cwd)
	if cwd == "" || cwd == "." {
		return nil
	}
	var dirs []string
	cur := cwd
	for {
		dirs = append(dirs, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return dirs
}

// nearestLocked computes the nearest saved decision for cwd without taking the
// mutex, so it can be reused inside already-locked methods.
func (m *Manager) nearestLocked(cwd string) Result {
	for _, dir := range walkUp(cwd) {
		if v, ok := m.data[dir]; ok {
			return Result{Decision: decisionFromBool(v), Path: dir, Found: true}
		}
	}
	return Result{Decision: Undecided, Found: false}
}

// NearestTrustDecision walks up from cwd (inclusive) to the filesystem root and
// returns the nearest saved decision. When no entry is found anywhere up the
// tree it returns {Decision: Undecided, Found: false} so the caller knows to
// prompt the user.
func (m *Manager) NearestTrustDecision(cwd string) Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nearestLocked(cwd)
}

// IsTrusted reports whether cwd is trusted for side-effect execution. It
// returns true when the nearest persisted decision is Trusted, or when cwd (or
// an ancestor) was granted session trust via SetSessionTrust. Everything else
// (Untrusted, Undecided, or no entry) returns false, meaning side-effect tools
// require confirmation.
func (m *Manager) IsTrusted(cwd string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, dir := range walkUp(cwd) {
		if m.session[dir] {
			return true
		}
	}
	return m.nearestLocked(cwd).Decision == Trusted
}

// SetDecision persists a decision for dir. Trusted and Untrusted write true and
// false respectively; Undecided writes an explicit null entry (recorded but
// undecided, distinct from a forgotten/absent entry). The directory is created
// lazily when the trust file is first written.
func (m *Manager) SetDecision(dir string, dec Decision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir = filepath.Clean(dir)
	m.data[dir] = boolFromDecision(dec)
	return m.saveLocked()
}

// SetSessionTrust grants trust for dir for the current process only. It is not
// persisted: a future pigo launch re-prompts. Used by the "just once" REPL
// choice and by the confirmation prompt's "always" response.
func (m *Manager) SetSessionTrust(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session[filepath.Clean(dir)] = true
}

// ClearSessionTrust revokes any session trust granted for dir or an ancestor,
// so a subsequent IsTrusted reflects only the persisted decision. It does not
// touch the on-disk store. Used by "/trust off" so an active session grant
// (from a prior "always") does not override a freshly-persisted Untrusted
// entry - IsTrusted checks session before persisted, so without this clear the
// "off" command would be ineffective until restart.
func (m *Manager) ClearSessionTrust(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range walkUp(dir) {
		delete(m.session, d)
	}
}

// Forget removes any saved decision for dir (both true/false and an explicit
// null entry), so the directory is treated as undecided on the next lookup.
func (m *Manager) Forget(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir = filepath.Clean(dir)
	delete(m.data, dir)
	return m.saveLocked()
}

// DecisionFor returns the raw saved value for a single path (nil when absent or
// null). It is the exact-path lookup (no walk), used by /trust status to show
// what is stored for the current directory itself.
func (m *Manager) DecisionFor(dir string) (Decision, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[filepath.Clean(dir)]
	if !ok {
		return Undecided, false
	}
	return decisionFromBool(v), true
}

// saveLocked writes the trust map to disk atomically so a crash mid-write
// cannot leave a truncated store. json.Marshal sorts map keys, so the output is
// stable and diff-friendly; a nil *bool marshals as JSON null, preserving the
// "path -> bool|null" schema. The temp file is created with os.CreateTemp
// (mode 0o600, process-unique name) so two concurrent pigo processes writing
// the shared store cannot clobber each other's temp file before the rename.
// The caller must hold m.mu.
func (m *Manager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("trust: create dir: %w", err)
	}
	b, err := json.Marshal(m.data)
	if err != nil {
		return fmt.Errorf("trust: marshal: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Dir(m.path)
	f, err := os.CreateTemp(dir, filepath.Base(m.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("trust: create temp file: %w", err)
	}
	tmpPath := f.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := f.Write(b); err != nil {
		f.Close()
		cleanup()
		return fmt.Errorf("trust: write %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("trust: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, m.path); err != nil {
		cleanup()
		return fmt.Errorf("trust: rename %s: %w", m.path, err)
	}
	return nil
}
