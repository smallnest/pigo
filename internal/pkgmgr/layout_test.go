package pkgmgr

import (
	"path/filepath"
	"testing"
)

// TestHomeHonorsPIGOHOME verifies Home prefers PIGO_HOME over the default.
func TestHomeHonorsPIGOHOME(t *testing.T) {
	t.Setenv("PIGO_HOME", "/custom/pigo")
	if got := Home(); got != "/custom/pigo" {
		t.Errorf("Home() = %q, want /custom/pigo", got)
	}
}

// TestTypeDirsUnderHome verifies the plugins/commands/themes dirs nest under
// $PIGO_HOME.
func TestTypeDirsUnderHome(t *testing.T) {
	t.Setenv("PIGO_HOME", "/custom/pigo")
	cases := map[PackageType]string{
		TypeExtension: "/custom/pigo/plugins",
		TypePrompt:    "/custom/pigo/commands",
		TypeTheme:     "/custom/pigo/themes",
	}
	for typ, want := range cases {
		if got := DirForType(typ); got != want {
			t.Errorf("DirForType(%s) = %q, want %q", typ, got, want)
		}
	}
}

// TestSkillsDirHonorsOverride verifies skills use PIGO_SKILLS_DIR, not $PIGO_HOME.
func TestSkillsDirHonorsOverride(t *testing.T) {
	t.Setenv("PIGO_SKILLS_DIR", "/custom/skills")
	if got := SkillsDir(); got != "/custom/skills" {
		t.Errorf("SkillsDir() = %q, want /custom/skills", got)
	}
	if got := DirForType(TypeSkill); got != "/custom/skills" {
		t.Errorf("DirForType(skill) = %q, want /custom/skills", got)
	}
}

// TestSkillsDirDefault verifies skills default to ~/.agents/skills.
func TestSkillsDirDefault(t *testing.T) {
	t.Setenv("PIGO_SKILLS_DIR", "")
	t.Setenv("HOME", "/home/tester")
	want := filepath.Join("/home/tester", ".agents", "skills")
	if got := SkillsDir(); got != want {
		t.Errorf("SkillsDir() = %q, want %q", got, want)
	}
}

// TestDirForUnknownType verifies an unknown type yields "".
func TestDirForUnknownType(t *testing.T) {
	if got := DirForType(PackageType("bogus")); got != "" {
		t.Errorf("DirForType(bogus) = %q, want empty", got)
	}
}
