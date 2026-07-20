package pkgmgr

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDistributeSkill verifies a skill package copies into skillsDir/<name>/
// with its SKILL.md and supporting files, discoverable by LoadSkillsDir's
// nested layout.
func TestDistributeSkill(t *testing.T) {
	skills := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", skills)

	pkg := writePkg(t, `{"name":"pi-writing","version":"1.0.0","pi":{"type":"skill"}}`, map[string]string{
		"SKILL.md":          "---\nname: writing\ndescription: help writing\n---\nbody",
		"references/tips.md": "tips",
	})

	files, err := DistributeSkill(pkg, "pi-writing")
	if err != nil {
		t.Fatalf("DistributeSkill: %v", err)
	}

	// SKILL.md must land at <skillsDir>/<name>/SKILL.md (nested layout).
	skillMd := filepath.Join(skills, "pi-writing", "SKILL.md")
	if _, err := os.Stat(skillMd); err != nil {
		t.Fatalf("SKILL.md not placed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skills, "pi-writing", "references", "tips.md")); err != nil {
		t.Errorf("supporting file not copied: %v", err)
	}

	var sawSkillMd bool
	for _, f := range files {
		if f == skillMd {
			sawSkillMd = true
		}
	}
	if !sawSkillMd {
		t.Errorf("created files %v missing SKILL.md", files)
	}
}

// TestDistributeSkillReinstallReplaces verifies reinstalling clears stale files.
func TestDistributeSkillReinstallReplaces(t *testing.T) {
	skills := t.TempDir()
	t.Setenv("PIGO_SKILLS_DIR", skills)

	pkg1 := writePkg(t, `{"name":"pi-s","version":"1.0.0"}`, map[string]string{
		"SKILL.md": "---\nname: s\ndescription: d\n---\n",
		"old.md":   "old",
	})
	if _, err := DistributeSkill(pkg1, "pi-s"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	pkg2 := writePkg(t, `{"name":"pi-s","version":"2.0.0"}`, map[string]string{
		"SKILL.md": "---\nname: s\ndescription: d2\n---\n",
	})
	if _, err := DistributeSkill(pkg2, "pi-s"); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skills, "pi-s", "old.md")); !os.IsNotExist(err) {
		t.Errorf("stale skill file survived reinstall: %v", err)
	}
}

// TestDistributeSkillNoSkillMd verifies a package without SKILL.md errors.
func TestDistributeSkillNoSkillMd(t *testing.T) {
	t.Setenv("PIGO_SKILLS_DIR", t.TempDir())
	pkg := writePkg(t, `{"name":"pi-noskill","version":"1.0.0"}`, nil)
	if _, err := DistributeSkill(pkg, "pi-noskill"); err == nil {
		t.Fatal("DistributeSkill without SKILL.md = nil error, want error")
	}
}
