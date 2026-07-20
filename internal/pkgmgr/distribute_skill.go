// This file distributes a classified pi skill into pigo's skills directory so
// runtime.LoadSkillsDir picks it up (#159).
//
// pigo loads skills from the skills dir (SkillsDir): ~/.agents/skills, or
// PIGO_SKILLS_DIR when set. LoadSkillsDir recognizes the nested layout
// "<skillsDir>/<name>/SKILL.md" (a directory per skill whose SKILL.md holds the
// YAML frontmatter). An npm skill package is exactly such a bundle — a SKILL.md
// plus its supporting files — so distribution is a straight copy of the package
// tree into "<skillsDir>/<name>/".
//
// As with extensions, every file laid down is returned so the lockfile can
// remove precisely what was installed on uninstall.
package pkgmgr

import (
	"fmt"
	"os"
	"path/filepath"
)

// DistributeSkill copies the skill package at pkgDir into the skills directory
// under a "<name>/" subdirectory, where runtime.LoadSkillsDir discovers it via
// its SKILL.md. It returns the absolute paths of every file (and the skill dir)
// it created, for the lockfile. An unresolvable skills dir, or a package with no
// SKILL.md, is an error.
func DistributeSkill(pkgDir, name string) ([]string, error) {
	skillsDir := SkillsDir()
	if skillsDir == "" {
		return nil, fmt.Errorf("pkgmgr: cannot resolve skills dir (home unavailable)")
	}
	if !fileExists(filepath.Join(pkgDir, "SKILL.md")) {
		return nil, fmt.Errorf("pkgmgr: skill %q has no SKILL.md", name)
	}

	dest := filepath.Join(skillsDir, name)
	// A stale skill from a prior install must not linger alongside the new one.
	if err := os.RemoveAll(dest); err != nil {
		return nil, fmt.Errorf("pkgmgr: clear old skill %q: %w", dest, err)
	}
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("pkgmgr: create skills dir: %w", err)
	}
	files, err := copyTree(pkgDir, dest)
	if err != nil {
		return nil, err
	}
	return append(files, dest), nil
}
