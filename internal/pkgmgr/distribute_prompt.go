// This file distributes a classified pi prompt/command package into pigo's
// commands directory so runtime.LoadUserCommandsDir picks it up (#160).
//
// pigo loads declarative slash commands from $PIGO_HOME/commands/*.md
// (non-recursively): each markdown file defines a "/name" command, named after
// the file, whose body is the prompt template. A pi prompt package ships one or
// more such templates, conventionally under a "commands/" subdirectory (the
// same convention pigo's loader mirrors); some packages place the .md files at
// the package root.
//
// Distribution copies those .md files (flattened, since the loader is
// non-recursive) into $PIGO_HOME/commands/. Every file laid down is returned so
// the lockfile can remove precisely what was installed.
package pkgmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DistributePrompt copies the prompt package's command templates at pkgDir into
// the commands directory. It looks first in "<pkgDir>/commands" for *.md files
// and falls back to *.md at the package root. It returns the absolute paths of
// every file it created, for the lockfile. An unresolvable commands dir, or a
// package with no command templates, is an error.
func DistributePrompt(pkgDir, name string) ([]string, error) {
	commandsDir := CommandsDir()
	if commandsDir == "" {
		return nil, fmt.Errorf("pkgmgr: cannot resolve commands dir (PIGO_HOME/home unavailable)")
	}

	srcDir := filepath.Join(pkgDir, "commands")
	if !dirExists(srcDir) {
		srcDir = pkgDir // fall back to root-level *.md
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: read prompt source dir: %w", err)
	}

	var mds []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		// A root-level README.md is not a command template; skip it when we've
		// fallen back to the package root.
		if srcDir == pkgDir && strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		mds = append(mds, e.Name())
	}
	if len(mds) == 0 {
		return nil, fmt.Errorf("pkgmgr: prompt %q has no command templates (*.md)", name)
	}

	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		return nil, fmt.Errorf("pkgmgr: create commands dir: %w", err)
	}

	created := make([]string, 0, len(mds))
	for _, md := range mds {
		src := filepath.Join(srcDir, md)
		dst := filepath.Join(commandsDir, md)
		info, statErr := os.Stat(src)
		if statErr != nil {
			return created, fmt.Errorf("pkgmgr: stat command %q: %w", src, statErr)
		}
		if err := copyFile(src, dst, info.Mode()); err != nil {
			return created, fmt.Errorf("pkgmgr: copy command %q: %w", md, err)
		}
		created = append(created, dst)
	}
	return created, nil
}
