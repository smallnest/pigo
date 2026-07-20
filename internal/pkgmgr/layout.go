// This file defines pigo's install directory layout (#154): where each pi
// package type is placed so pigo's existing discovery mechanisms load it with
// no extra configuration. The paths intentionally match the conventions already
// used elsewhere in cmd/pigo and internal/*:
//
//   - extensions → $PIGO_HOME/plugins   (internal/plugin.Discover)
//   - prompts    → $PIGO_HOME/commands  (runtime.LoadUserCommandsDir)
//   - themes     → $PIGO_HOME/themes    (no runtime consumer yet)
//   - skills     → skills dir           (~/.agents/skills, PIGO_SKILLS_DIR override)
//
// Skills are the one exception to the $PIGO_HOME root: pigo loads skills from
// ~/.agents/skills (overridable with PIGO_SKILLS_DIR), so SkillsDir honors that
// rather than nesting under $PIGO_HOME.
package pkgmgr

import (
	"os"
	"path/filepath"
)

// Home returns the pigo home directory: $PIGO_HOME, or ~/.pigo when unset. It
// returns "" when the home directory cannot be resolved and no override is set,
// matching trust.DefaultPath's "unavailable" contract.
func Home() string {
	if dir := os.Getenv("PIGO_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pigo")
}

// PluginsDir returns $PIGO_HOME/plugins, where installed extensions (including
// MCP adapters) are laid down for internal/plugin.Discover. It returns "" when
// Home is unavailable.
func PluginsDir() string {
	h := Home()
	if h == "" {
		return ""
	}
	return filepath.Join(h, "plugins")
}

// CommandsDir returns $PIGO_HOME/commands, where installed prompt/command
// templates are laid down for runtime.LoadUserCommandsDir. It returns "" when
// Home is unavailable.
func CommandsDir() string {
	h := Home()
	if h == "" {
		return ""
	}
	return filepath.Join(h, "commands")
}

// ThemesDir returns $PIGO_HOME/themes, where installed themes are stored. pigo
// has no theme runtime yet, so this is a holding location for a future consumer.
// It returns "" when Home is unavailable.
func ThemesDir() string {
	h := Home()
	if h == "" {
		return ""
	}
	return filepath.Join(h, "themes")
}

// SkillsDir returns the directory installed skills are placed in: PIGO_SKILLS_DIR
// when set, else ~/.agents/skills — matching cmd/pigo's skill loader. It returns
// "" when the home directory cannot be resolved and no override is set.
func SkillsDir() string {
	if dir := os.Getenv("PIGO_SKILLS_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agents", "skills")
}

// DirForType returns the install directory for a package type, or "" when the
// type is unknown or the underlying home directory is unavailable.
func DirForType(t PackageType) string {
	switch t {
	case TypeExtension:
		return PluginsDir()
	case TypePrompt:
		return CommandsDir()
	case TypeTheme:
		return ThemesDir()
	case TypeSkill:
		return SkillsDir()
	default:
		return ""
	}
}
