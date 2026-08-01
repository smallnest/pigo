package dream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMemFile writes body to <root>/<rel> creating parent dirs, returning the
// absolute path.
func writeMemFile(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(p)
}

func TestBuildPlanEnumeratesGlobalAndProjectExcludingSessionsAndCheckpoint(t *testing.T) {
	root := t.TempDir()
	projectDir := t.TempDir()
	pid := projectID(projectDir)

	gUser := writeMemFile(t, root, "global/user/prefs.md", "global user prefs\n")
	pProj := writeMemFile(t, root, filepath.Join("projects", pid, "project", "arch.md"), "project architecture notes\n")
	// Must be excluded:
	writeMemFile(t, root, "sessions/sess1/notes/x.md", "session scoped note\n")
	writeMemFile(t, root, "global/checkpoint/cp.md", "checkpoint transient\n")
	writeMemFile(t, root, filepath.Join("projects", pid, "checkpoint", "cp.md"), "project checkpoint\n")

	plan, err := BuildPlan(root, projectDir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	got := map[string]bool{}
	for _, f := range plan.Files {
		got[f.Path] = true
	}
	if !got[gUser] {
		t.Errorf("global user file not enumerated")
	}
	if !got[pProj] {
		t.Errorf("project file not enumerated")
	}
	if plan.FilesBefore != 2 {
		t.Errorf("FilesBefore = %d, want 2 (sessions + checkpoint excluded); files=%v", plan.FilesBefore, plan.Files)
	}
	if plan.BytesBefore == 0 {
		t.Errorf("BytesBefore = 0, want >0")
	}
}

func TestBuildPlanEmptyRoot(t *testing.T) {
	plan, err := BuildPlan(filepath.Join(t.TempDir(), "does-not-exist"), "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.FilesBefore != 0 || len(plan.Files) != 0 {
		t.Errorf("empty root should yield empty plan, got %+v", plan)
	}
}

func TestExactDedupGrouping(t *testing.T) {
	root := t.TempDir()
	same := "identical memory body\nline two\n"
	a := writeMemFile(t, root, "global/user/a.md", same)
	b := writeMemFile(t, root, "global/reference/b.md", same)
	writeMemFile(t, root, "global/notes/c.md", "a totally different unique body\n")

	plan, err := BuildPlan(root, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.DedupeGroups) != 1 {
		t.Fatalf("DedupeGroups = %d, want 1: %+v", len(plan.DedupeGroups), plan.DedupeGroups)
	}
	g := plan.DedupeGroups[0]
	if len(g.Paths) != 2 {
		t.Fatalf("group paths = %v, want [a b]", g.Paths)
	}
	want := map[string]bool{a: true, b: true}
	for _, p := range g.Paths {
		if !want[p] {
			t.Errorf("unexpected path in dedupe group: %q", p)
		}
	}
}

func TestPathValidation(t *testing.T) {
	root := t.TempDir()
	projectDir := t.TempDir()

	// A real file the memory references (relative to projectDir).
	existingRel := "src/main.go"
	writeMemFile(t, projectDir, existingRel, "package main\n") // reuse helper; writes under projectDir

	body := "See `src/main.go` for the entrypoint.\n" +
		"Old helper lived at `src/gone/removed.go` but was deleted.\n" +
		"Reference: https://example.com/docs and [site](https://pkg.go.dev/net/http).\n" +
		"Email me at mailto:dev@example.com.\n"
	writeMemFile(t, root, "global/project/notes.md", body)

	plan, err := BuildPlan(root, projectDir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	var flagged []string
	for _, r := range plan.InvalidPathRefs {
		flagged = append(flagged, r.Ref)
	}

	// Missing local path must be flagged.
	if !containsRef(flagged, "src/gone/removed.go") {
		t.Errorf("missing path src/gone/removed.go not flagged; flagged=%v", flagged)
	}
	// Existing local path must NOT be flagged.
	if containsRef(flagged, "src/main.go") {
		t.Errorf("existing path src/main.go wrongly flagged; flagged=%v", flagged)
	}
	// URLs / external refs must NEVER be flagged.
	for _, r := range flagged {
		if wantsURLReject(r) {
			t.Errorf("external reference wrongly flagged as invalid local path: %q", r)
		}
	}
}

func containsRef(refs []string, want string) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}

func wantsURLReject(r string) bool {
	for _, bad := range []string{"http", "https", "mailto", "example.com", "pkg.go.dev"} {
		if strings.HasPrefix(r, bad) {
			return true
		}
	}
	return false
}

func TestPathValidationIgnoresProseSlashes(t *testing.T) {
	root := t.TempDir()
	projectDir := t.TempDir()
	// Prose tokens with slashes but no file extension must not be flagged as
	// missing local paths.
	body := "We support TCP/IP and read/write access; input/output is N/A here.\n"
	writeMemFile(t, root, "global/notes/prose.md", body)

	plan, err := BuildPlan(root, projectDir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.InvalidPathRefs) != 0 {
		t.Errorf("prose slashes wrongly flagged as paths: %+v", plan.InvalidPathRefs)
	}
}

func TestNearDupPairingThreshold(t *testing.T) {
	root := t.TempDir()
	// Two highly-overlapping (but not identical) bodies -> should pair.
	writeMemFile(t, root, "global/user/a.md",
		"the quick brown fox jumps over the lazy dog near the river bank today\n")
	writeMemFile(t, root, "global/user/b.md",
		"the quick brown fox jumps over the lazy dog near the river bank tomorrow\n")
	// A dissimilar body -> should not pair with the others.
	writeMemFile(t, root, "global/notes/c.md",
		"completely unrelated content about database indexing and query planning\n")

	plan, err := BuildPlan(root, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.NearDupPairs) != 1 {
		t.Fatalf("NearDupPairs = %d, want exactly 1: %+v", len(plan.NearDupPairs), plan.NearDupPairs)
	}
	p := plan.NearDupPairs[0]
	if p.Similarity < NearDupThreshold {
		t.Errorf("paired similarity %v below threshold %v", p.Similarity, NearDupThreshold)
	}
	// The dissimilar file must not appear in any pair.
	for _, pr := range plan.NearDupPairs {
		if filepath.Base(pr.A) == "c.md" || filepath.Base(pr.B) == "c.md" {
			t.Errorf("dissimilar file c.md wrongly paired: %+v", pr)
		}
	}
}

func TestNearDupSkipsExactDuplicates(t *testing.T) {
	root := t.TempDir()
	same := "one two three four five six seven eight nine ten\n"
	writeMemFile(t, root, "global/user/a.md", same)
	writeMemFile(t, root, "global/user/b.md", same)

	plan, err := BuildPlan(root, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.NearDupPairs) != 0 {
		t.Errorf("exact duplicates should be handled by dedupe, not near-dup pairs: %+v", plan.NearDupPairs)
	}
	if len(plan.DedupeGroups) != 1 {
		t.Errorf("DedupeGroups = %d, want 1", len(plan.DedupeGroups))
	}
}
