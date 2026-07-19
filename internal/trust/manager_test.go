package trust

// Tests for the trust store (US-018, #134). The store is a JSON map of
// directory path to a nullable boolean (true/false/null); these tests pin the
// tri-state semantics, the nearest-ancestor walk, session-vs-persisted trust,
// and the on-disk round-trip including the null value.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// newTestManager builds a Manager backed by a temp file, failing the test if
// construction fails. Every test starts from an empty store.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// reload reopens the manager at the same path, asserting no error, so a test
// can verify a decision survived a write.
func reload(t *testing.T, m *Manager) *Manager {
	t.Helper()
	m2, err := NewManager(m.path)
	if err != nil {
		t.Fatalf("reload NewManager: %v", err)
	}
	return m2
}

// TestNewManagerMissingFile verifies a missing trust file is not an error: the
// manager starts empty and the nearest lookup reports nothing found.
func TestNewManagerMissingFile(t *testing.T) {
	m := newTestManager(t)
	if res := m.NearestTrustDecision("/some/dir"); res.Found {
		t.Errorf("NearestTrustDecision on empty store: Found=true, want false")
	}
}

// TestNewManagerEmptyPath verifies an empty path disables persistence: Set and
// Forget are no-ops on disk and never error, and lookups still work in-memory.
func TestNewManagerEmptyPath(t *testing.T) {
	m, err := NewManager("")
	if err != nil {
		t.Fatalf("NewManager(\"\"): %v", err)
	}
	if err := m.SetDecision("/a", Trusted); err != nil {
		t.Fatalf("SetDecision on empty-path manager: %v", err)
	}
	if !m.IsTrusted("/a") {
		t.Error("IsTrusted(/a) = false after in-memory SetDecision, want true")
	}
}

// TestSetDecisionPersists verifies Trusted/Untrusted round-trip through disk:
// after SetDecision + reload, the nearest decision matches what was written.
func TestSetDecisionPersists(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetDecision("/a", Trusted); err != nil {
		t.Fatalf("SetDecision Trusted: %v", err)
	}
	if err := m.SetDecision("/b", Untrusted); err != nil {
		t.Fatalf("SetDecision Untrusted: %v", err)
	}
	m2 := reload(t, m)
	if got := m2.NearestTrustDecision("/a"); !got.Found || got.Decision != Trusted || got.Path != "/a" {
		t.Errorf("reload NearestTrustDecision(/a) = %+v, want Found/Trusted//a", got)
	}
	if got := m2.NearestTrustDecision("/b"); !got.Found || got.Decision != Untrusted || got.Path != "/b" {
		t.Errorf("reload NearestTrustDecision(/b) = %+v, want Found/Untrusted//b", got)
	}
}

// TestNearestAncestorWalk verifies the lookup walks up from cwd to root and
// returns the nearest ancestor (inclusive) with an entry: a decision saved for
// /a applies to /a/b/c, and the returned Path is the directory it was saved
// for, not the query directory.
func TestNearestAncestorWalk(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetDecision("/a", Trusted); err != nil {
		t.Fatalf("SetDecision: %v", err)
	}
	got := m.NearestTrustDecision("/a/b/c")
	if !got.Found || got.Decision != Trusted || got.Path != "/a" {
		t.Errorf("NearestTrustDecision(/a/b/c) = %+v, want Found/Trusted/Path=/a", got)
	}
	// A more specific entry shadows a broader one: /a/b/untrusted wins over
	// /a/trusted for anything under /a/b.
	if err := m.SetDecision("/a/b", Untrusted); err != nil {
		t.Fatalf("SetDecision /a/b: %v", err)
	}
	got = m.NearestTrustDecision("/a/b/c")
	if !got.Found || got.Decision != Untrusted || got.Path != "/a/b" {
		t.Errorf("NearestTrustDecision(/a/b/c) after shadow = %+v, want Found/Untrusted/Path=/a/b", got)
	}
	// /a/d is under /a but not /a/b, so it still sees /a/trusted.
	got = m.NearestTrustDecision("/a/d")
	if !got.Found || got.Decision != Trusted || got.Path != "/a" {
		t.Errorf("NearestTrustDecision(/a/d) = %+v, want Found/Trusted/Path=/a", got)
	}
}

// TestNearestNotFound verifies a query with no entry on the path returns
// Found=false and Undecided.
func TestNearestNotFound(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetDecision("/a", Trusted); err != nil {
		t.Fatalf("SetDecision: %v", err)
	}
	if got := m.NearestTrustDecision("/completely/unrelated"); got.Found {
		t.Errorf("NearestTrustDecision(unrelated) = %+v, want Found=false", got)
	}
}

// TestNullEntryRoundTrip verifies the "null" half of the "path -> bool|null"
// schema: SetDecision(Undecided) writes an explicit JSON null, which reloads as
// Found=true with Decision Undecided (recorded but not trusted).
func TestNullEntryRoundTrip(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetDecision("/a", Undecided); err != nil {
		t.Fatalf("SetDecision Undecided: %v", err)
	}
	// The on-disk value really is null, not omitted.
	raw, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatalf("read trust file: %v", err)
	}
	var got map[string]*bool
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse trust file: %v", err)
	}
	v, ok := got["/a"]
	if !ok {
		t.Fatal("entry /a missing from trust file")
	}
	if v != nil {
		t.Errorf("stored value = %v, want nil (JSON null)", *v)
	}
	// Reload: Found=true (an entry exists), Decision=Undecided (it is null).
	m2 := reload(t, m)
	res := m2.NearestTrustDecision("/a")
	if !res.Found || res.Decision != Undecided {
		t.Errorf("reload NearestTrustDecision(/a) = %+v, want Found=true/Undecided", res)
	}
}

// TestIsTrusted verifies the gating predicate: true only for Trusted (persisted
// or session), false for Untrusted/Undecided/absent, and that an ancestor
// Trusted decision covers a descendant.
func TestIsTrusted(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetDecision("/trusted", Trusted); err != nil {
		t.Fatalf("SetDecision Trusted: %v", err)
	}
	if err := m.SetDecision("/untrusted", Untrusted); err != nil {
		t.Fatalf("SetDecision Untrusted: %v", err)
	}
	cases := []struct {
		cwd  string
		want bool
	}{
		{"/trusted", true},          // exact trusted
		{"/trusted/sub/deep", true}, // ancestor trusted
		{"/untrusted", false},       // exact untrusted
		{"/untrusted/sub", false},   // ancestor untrusted
		{"/undecided", false},       // no entry
		{"/", false},                // root, no entry
	}
	for _, c := range cases {
		if got := m.IsTrusted(c.cwd); got != c.want {
			t.Errorf("IsTrusted(%q) = %v, want %v", c.cwd, got, c.want)
		}
	}
}

// TestSessionTrustNotPersisted verifies SetSessionTrust grants trust for the
// current process but does not survive a reload (matching the "just once" REPL
// choice): a fresh manager over the same file does not see the grant.
func TestSessionTrustNotPersisted(t *testing.T) {
	m := newTestManager(t)
	m.SetSessionTrust("/proj")
	if !m.IsTrusted("/proj") {
		t.Error("IsTrusted(/proj) = false after SetSessionTrust, want true")
	}
	if !m.IsTrusted("/proj/sub") {
		t.Error("IsTrusted(/proj/sub) = false, want true (session trust covers descendants)")
	}
	m2 := reload(t, m)
	if m2.IsTrusted("/proj") {
		t.Error("reload IsTrusted(/proj) = true, want false (session trust must not persist)")
	}
}

// TestClearSessionTrust verifies ClearSessionTrust revokes a session grant so
// IsTrusted reflects only the persisted decision, including grants on an
// ancestor (walkUp). This is the contract "/trust off" relies on to take effect
// immediately rather than only after a restart.
func TestClearSessionTrust(t *testing.T) {
	m := newTestManager(t)
	m.SetSessionTrust("/proj")
	if !m.IsTrusted("/proj") {
		t.Fatal("IsTrusted = false after SetSessionTrust, want true")
	}
	m.ClearSessionTrust("/proj")
	if m.IsTrusted("/proj") {
		t.Error("IsTrusted = true after ClearSessionTrust, want false")
	}
	// Clearing a descendant also revokes an ancestor's session grant, since
	// ClearSessionTrust walks up (matching IsTrusted's walkUp check).
	m.SetSessionTrust("/a")
	m.ClearSessionTrust("/a/b")
	if m.IsTrusted("/a/b") {
		t.Error("IsTrusted(/a/b) = true after ClearSessionTrust(/a/b), want false (ancestor /a grant revoked)")
	}
}

// TestForget verifies Forget removes an entry so the directory becomes
// undecided again, both in-memory and after reload.
func TestForget(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetDecision("/a", Trusted); err != nil {
		t.Fatalf("SetDecision: %v", err)
	}
	if err := m.Forget("/a"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if got := m.NearestTrustDecision("/a"); got.Found {
		t.Errorf("NearestTrustDecision after Forget = %+v, want Found=false", got)
	}
	// Forget is idempotent: forgetting a path with no entry is not an error.
	if err := m.Forget("/a"); err != nil {
		t.Errorf("Forget missing entry: %v", err)
	}
}

// TestDecisionForExactPath verifies the exact-path lookup (no walk) returns the
// stored decision and a Found flag, distinct from the walk-based nearest.
func TestDecisionForExactPath(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetDecision("/a", Trusted); err != nil {
		t.Fatalf("SetDecision: %v", err)
	}
	if dec, found := m.DecisionFor("/a"); !found || dec != Trusted {
		t.Errorf("DecisionFor(/a) = %v,%v, want Trusted,true", dec, found)
	}
	if dec, found := m.DecisionFor("/a/b"); found {
		t.Errorf("DecisionFor(/a/b) = %v,%v, want _,false (no exact entry)", dec, found)
	}
}

// TestDefaultPath verifies DefaultPath honors PIGO_HOME and falls back to
// ~/.pigo/trust.json.
func TestDefaultPath(t *testing.T) {
	t.Setenv("PIGO_HOME", "/custom/pigo")
	if got := DefaultPath(); got != "/custom/pigo/trust.json" {
		t.Errorf("DefaultPath with PIGO_HOME = %q, want /custom/pigo/trust.json", got)
	}
	t.Setenv("PIGO_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("home dir unavailable")
	}
	want := filepath.Join(home, ".pigo", "trust.json")
	if got := DefaultPath(); got != want {
		t.Errorf("DefaultPath default = %q, want %q", got, want)
	}
}

// TestSaveIsSorted verifies the written file has sorted keys (stable, diffable
// output) and is a valid JSON object.
func TestSaveIsSorted(t *testing.T) {
	m := newTestManager(t)
	for _, p := range []string{"/zeta", "/alpha", "/mid"} {
		if err := m.SetDecision(p, Trusted); err != nil {
			t.Fatalf("SetDecision %s: %v", p, err)
		}
	}
	raw, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(raw)
	i := strings.Index(s, "/alpha")
	j := strings.Index(s, "/mid")
	k := strings.Index(s, "/zeta")
	if i < 0 || j < 0 || k < 0 {
		t.Fatalf("expected /alpha, /mid, /zeta in output; got indices %d/%d/%d", i, j, k)
	}
	if !(i < j && j < k) {
		t.Errorf("keys not sorted in output: alpha@%d mid@%d zeta@%d", i, j, k)
	}
	var got map[string]*bool
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Errorf("output is not valid JSON: %v", err)
	}
}

// TestMalformedFileIsError verifies a corrupted trust file is a hard error
// rather than being silently overwritten, so the user's data is surfaced.
func TestMalformedFileIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed file: %v", err)
	}
	if _, err := NewManager(path); err == nil {
		t.Error("NewManager on malformed file returned nil error, want a parse error")
	}
}

// TestConcurrentAccess exercises the mutex under -race: many goroutines reading
// and writing concurrently must not trip the race detector.
func TestConcurrentAccess(t *testing.T) {
	m := newTestManager(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dir := filepath.Join("/p", "d"+strconv.Itoa(i))
			_ = m.SetDecision(dir, Trusted)
			_ = m.IsTrusted(dir)
			_ = m.NearestTrustDecision(dir)
			m.SetSessionTrust(dir)
			_, _ = m.DecisionFor(dir)
		}(i)
	}
	wg.Wait()
}
