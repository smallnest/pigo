package builtinskills

import (
	"os"
	"path"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// mapSet builds a Set backed by an in-memory tree so bootstrap can be exercised
// without the embedded FS. Each skill gets a SKILL.md plus one support file.
func mapSet(name, version string, skills ...string) Set {
	files := fstest.MapFS{}
	for _, s := range skills {
		files["skills/"+s+"/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: " + s + "\n---\nbody")}
		files["skills/"+s+"/support.txt"] = &fstest.MapFile{Data: []byte("aux for " + s)}
	}
	return Set{Name: name, Version: version, Root: "skills", Skills: skills, FS: files}
}

// TestBootstrapInstallsSkills verifies a fresh bootstrap lays every named skill
// down under skillsDir with its SKILL.md and support files intact.
func TestBootstrapInstallsSkills(t *testing.T) {
	home := t.TempDir()
	skills := t.TempDir()
	set := mapSet("test-set", "v1", "alpha", "beta")

	bootstrap([]Set{set}, home, skills, nil)

	for _, name := range []string{"alpha", "beta"} {
		md := filepath.Join(skills, name, "SKILL.md")
		if _, err := os.Stat(md); err != nil {
			t.Errorf("expected %s installed: %v", md, err)
		}
		aux := filepath.Join(skills, name, "support.txt")
		if _, err := os.Stat(aux); err != nil {
			t.Errorf("expected support file %s: %v", aux, err)
		}
	}
	// State recorded so a re-run is a no-op.
	if _, err := os.Stat(filepath.Join(home, stateFileName)); err != nil {
		t.Errorf("expected state file written: %v", err)
	}
}

// TestBootstrapSkipsWhenAlreadyInstalled verifies a second bootstrap at the same
// version does not touch an existing (possibly user-edited) skill.
func TestBootstrapSkipsWhenAlreadyInstalled(t *testing.T) {
	home := t.TempDir()
	skills := t.TempDir()
	set := mapSet("test-set", "v1", "alpha")

	bootstrap([]Set{set}, home, skills, nil)

	// Simulate a user edit, then re-run: the edit must survive.
	md := filepath.Join(skills, "alpha", "SKILL.md")
	if err := os.WriteFile(md, []byte("user edited"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	bootstrap([]Set{set}, home, skills, nil)

	got, err := os.ReadFile(md)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "user edited" {
		t.Errorf("SKILL.md = %q, want user edit preserved", got)
	}
}

// TestBootstrapDoesNotClobberPreexistingSkill verifies a skill directory that
// already exists before the first bootstrap is left untouched (never overwritten).
func TestBootstrapDoesNotClobberPreexistingSkill(t *testing.T) {
	home := t.TempDir()
	skills := t.TempDir()
	// Pre-place a user's own "alpha" skill.
	if err := os.MkdirAll(filepath.Join(skills, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(skills, "alpha", "SKILL.md")
	if err := os.WriteFile(mine, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	bootstrap([]Set{mapSet("test-set", "v1", "alpha", "beta")}, home, skills, nil)

	got, _ := os.ReadFile(mine)
	if string(got) != "mine" {
		t.Errorf("preexisting alpha overwritten: %q", got)
	}
	// The other skill still installs.
	if _, err := os.Stat(filepath.Join(skills, "beta", "SKILL.md")); err != nil {
		t.Errorf("beta should install alongside preexisting alpha: %v", err)
	}
}

// TestBootstrapVersionBumpReinstallsMissing verifies bumping a set's Version
// re-triggers installation of skills that are missing, without disturbing ones
// already present.
func TestBootstrapVersionBumpReinstallsMissing(t *testing.T) {
	home := t.TempDir()
	skills := t.TempDir()

	bootstrap([]Set{mapSet("test-set", "v1", "alpha")}, home, skills, nil)

	// v2 adds "beta"; alpha is already present and must stay.
	bootstrap([]Set{mapSet("test-set", "v2", "alpha", "beta")}, home, skills, nil)

	if _, err := os.Stat(filepath.Join(skills, "beta", "SKILL.md")); err != nil {
		t.Errorf("version bump should install newly added beta: %v", err)
	}
}

// TestBootstrapEmptyHomeIsNoop verifies an unresolved home/skills dir disables
// bootstrap without panicking or erroring.
func TestBootstrapEmptyHomeIsNoop(t *testing.T) {
	bootstrap([]Set{mapSet("s", "v1", "alpha")}, "", "", nil)
	bootstrap([]Set{mapSet("s", "v1", "alpha")}, t.TempDir(), "", nil)
}

// TestBootstrapEmptyVersionInstalls verifies a Set with a blank Version is not
// mistaken for "already installed" (the zero-value state lookup also equals "")
// and so its skills are installed on a fresh run.
func TestBootstrapEmptyVersionInstalls(t *testing.T) {
	home := t.TempDir()
	skills := t.TempDir()

	bootstrap([]Set{mapSet("test-set", "", "alpha")}, home, skills, nil)

	if _, err := os.Stat(filepath.Join(skills, "alpha", "SKILL.md")); err != nil {
		t.Errorf("blank-version set should still install: %v", err)
	}
}

// TestManifestEmbedsAllSkills verifies every skill named in the real manifest is
// actually embedded (each has a SKILL.md), catching a missing-copy regression.
func TestManifestEmbedsAllSkills(t *testing.T) {
	for _, set := range Manifest() {
		for _, name := range set.Skills {
			md := path.Join(set.Root, name, "SKILL.md")
			if _, err := set.FS.Open(md); err != nil {
				t.Errorf("%s/%s: embedded SKILL.md missing: %v", set.Name, name, err)
			}
		}
	}
}

// TestBootstrapInstallsRealManifest verifies the embedded manifest installs its
// full skill set into a temp skills dir — the end-to-end offline install path.
func TestBootstrapInstallsRealManifest(t *testing.T) {
	home := t.TempDir()
	skills := t.TempDir()

	Bootstrap(home, skills, nil)

	for _, set := range Manifest() {
		for _, name := range set.Skills {
			if _, err := os.Stat(filepath.Join(skills, name, "SKILL.md")); err != nil {
				t.Errorf("skill %q not installed: %v", name, err)
			}
		}
	}
}
