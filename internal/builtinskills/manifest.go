// Package builtinskills embeds a curated set of skills into the pigo binary and
// installs them into the user's skills directory on first run. This makes a
// baseline of workflow skills (the goal-workflow set plus a couple of standalone
// skills) available as /skill-name commands out of the box, with no manual
// `pigo install` step and no network access — the skill trees are compiled into
// the binary via //go:embed.
//
// The design is deliberately generic (a manifest of skill *sets*) so future
// collections can be added by appending a Set to Manifest and embedding their
// files, without touching the bootstrap/first-run logic in Bootstrap.
package builtinskills

import (
	"embed"
	"io/fs"
)

// skillsFS holds the embedded skill trees under skills/<name>/. The `all:`
// prefix is required so files whose names begin with '_' or '.' (e.g.
// weather/_meta.json) are embedded too — the default pattern skips them.
//
//go:embed all:skills
var skillsFS embed.FS

// Set is one collection of built-in skills sharing a provenance and version.
// Bootstrap installs every skill named in a Set from FS, rooted at Root (the
// path within FS holding the per-skill directories). Adding a new collection is
// a matter of appending a Set here and embedding its files — the install and
// first-run machinery is collection-agnostic.
type Set struct {
	// Name identifies the collection (e.g. "goal-workflow"), used in the
	// bootstrap state record and diagnostics.
	Name string
	// Version marks the collection's revision. It is recorded in the bootstrap
	// state file; a Version newer than the recorded one re-triggers install of
	// any still-missing skills on the next run.
	Version string
	// Root is the directory within FS that contains the per-skill subdirectories.
	Root string
	// Skills lists the skill directory names under Root to install. Each must be
	// a "<name>/" directory holding a SKILL.md.
	Skills []string
	// FS is the filesystem the skills are read from. Production sets use the
	// embedded skillsFS; tests can supply an fstest.MapFS with a synthetic tree.
	FS fs.FS
}

// goalWorkflowSkills is the goal-workflow set (https://goal.rpcx.io) minus
// humanize-it (intentionally excluded), plus the two standalone skills
// architecture-diagram and weather.
var goalWorkflowSkills = []string{
	// goal-workflow core
	"prd", "prd-to-spec", "to-issues", "review-it", "ship-it",
	// goal-workflow bonus (humanize-it intentionally excluded)
	"insight-diagram", "refactor", "modern-go", "note-it",
	"code-to-spec", "smell", "loop-it", "to-design", "graph",
	// standalone additions
	"architecture-diagram", "weather",
}

// Manifest is the single source of truth for the built-in skill collections
// installed on first run. It seeds one collection today; adding a Set is all
// that is needed to bundle another collection.
func Manifest() []Set {
	return []Set{
		{
			Name:    "goal-workflow",
			Version: "2026-07-24",
			Root:    "skills",
			Skills:  goalWorkflowSkills,
			FS:      skillsFS,
		},
	}
}
