// This file distributes a classified pi extension (including MCP adapters) into
// pigo's plugins directory so internal/plugin.Discover picks it up (#158).
//
// plugin.Discover launches every *executable regular file* directly inside
// $PIGO_HOME/plugins, ignoring subdirectories. An npm extension, however, is a
// whole package tree with a "bin" entry pointing at its real entrypoint, and
// that entrypoint usually needs its sibling files present to run. So we cannot
// just drop a single file in.
//
// The layout we lay down reconciles the two:
//
//	$PIGO_HOME/plugins/<name>.pkg/     ← full extracted package (a dir; Discover skips it)
//	$PIGO_HOME/plugins/<name>          ← executable launcher (a file; Discover runs it)
//
// The launcher is a tiny shell script that execs the package's bin entrypoint,
// forwarding argv. The bin file is made executable and relies on its own
// shebang (matching how npm itself installs bins), so both Node scripts and
// native binaries work. Every file laid down is returned so the lockfile can
// remove exactly what was created on uninstall.
package pkgmgr

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// DistributeExtension copies the extension package at pkgDir into the plugins
// directory and writes a launcher that plugin.Discover will run. It returns the
// absolute paths of every file (and the payload dir) it created, for the
// lockfile. An empty plugins dir (home unavailable) is an error.
func DistributeExtension(pkgDir, name string) ([]string, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("pkgmgr: extension install is not supported on windows yet")
	}
	pluginsDir := PluginsDir()
	if pluginsDir == "" {
		return nil, fmt.Errorf("pkgmgr: cannot resolve plugins dir (PIGO_HOME/home unavailable)")
	}

	binRel, err := extensionBin(pkgDir, name)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, fmt.Errorf("pkgmgr: create plugins dir: %w", err)
	}

	payloadDir := filepath.Join(pluginsDir, name+".pkg")
	// A stale payload from a prior install must not shadow the new one.
	if err := os.RemoveAll(payloadDir); err != nil {
		return nil, fmt.Errorf("pkgmgr: clear old payload %q: %w", payloadDir, err)
	}
	created, err := copyTree(pkgDir, payloadDir)
	if err != nil {
		return nil, err
	}

	// Make the bin entrypoint executable; it carries its own shebang.
	binAbs := filepath.Join(payloadDir, filepath.FromSlash(binRel))
	if err := os.Chmod(binAbs, 0o755); err != nil {
		return nil, fmt.Errorf("pkgmgr: chmod bin %q: %w", binAbs, err)
	}

	launcher := filepath.Join(pluginsDir, name)
	script := fmt.Sprintf("#!/bin/sh\nexec %s \"$@\"\n", shellQuote(binAbs))
	if err := os.WriteFile(launcher, []byte(script), 0o755); err != nil {
		return nil, fmt.Errorf("pkgmgr: write launcher %q: %w", launcher, err)
	}

	created = append(created, payloadDir, launcher)
	return created, nil
}

// extensionBin resolves the package's bin entrypoint (relative to the package
// root) from package.json. npm's "bin" is either a string (single bin) or an
// object mapping command name → path; for the object form we prefer the entry
// keyed by the package name, else any single entry.
func extensionBin(pkgDir, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return "", fmt.Errorf("pkgmgr: read package.json: %w", err)
	}
	var pj struct {
		Bin json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(data, &pj); err != nil {
		return "", fmt.Errorf("pkgmgr: parse package.json: %w", err)
	}
	if len(pj.Bin) == 0 {
		return "", fmt.Errorf("pkgmgr: extension %q has no bin entry in package.json", name)
	}
	// String form: a single bin path.
	var s string
	if err := json.Unmarshal(pj.Bin, &s); err == nil && s != "" {
		return s, nil
	}
	// Object form: {command: path}.
	var m map[string]string
	if err := json.Unmarshal(pj.Bin, &m); err == nil && len(m) > 0 {
		if p, ok := m[name]; ok && p != "" {
			return p, nil
		}
		// npm-scoped name: bin key is often the unscoped base name.
		if p, ok := m[filepath.Base(name)]; ok && p != "" {
			return p, nil
		}
		for _, p := range m {
			if p != "" {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("pkgmgr: extension %q has an unreadable bin entry", name)
}

// copyTree recursively copies src into dst (created), preserving file modes and
// relative structure. It returns the absolute paths of every regular file it
// wrote (not directories), so callers can record precisely what was laid down.
func copyTree(src, dst string) ([]string, error) {
	var files []string
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			if err := copyFile(path, target, info.Mode()); err != nil {
				return err
			}
			files = append(files, target)
			return nil
		default:
			// Skip symlinks/devices — npm packages are files + dirs.
			return nil
		}
	})
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: copy package tree: %w", err)
	}
	return files, nil
}

// copyFile copies a single regular file from src to dst with the given mode.
func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// shellQuote wraps s in single quotes for safe embedding in a /bin/sh script,
// escaping any embedded single quotes.
func shellQuote(s string) string {
	quoted := make([]byte, 0, len(s)+2)
	quoted = append(quoted, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			quoted = append(quoted, '\'', '\\', '\'', '\'')
			continue
		}
		quoted = append(quoted, s[i])
	}
	quoted = append(quoted, '\'')
	return string(quoted)
}
