package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Result reports what a Reconcile pass changed: Indexed counts rows inserted or
// updated (new or changed files), Pruned counts rows deleted because their file
// no longer exists on disk.
type Result struct {
	Indexed int
	Pruned  int
}

// walkMemoryDir recursively collects every *.md file under root. A missing root
// (ENOENT) yields an empty slice and no error, so reconcile is safe to run
// before the memory directory has been created.
func walkMemoryDir(root string) ([]string, error) {
	var out []string
	var recurse func(dir string) error
	recurse = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, entry := range entries {
			full := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := recurse(full); err != nil {
					return err
				}
			} else if entry.Type().IsRegular() && isMarkdown(entry.Name()) {
				out = append(out, full)
			}
		}
		return nil
	}
	if err := recurse(root); err != nil {
		return nil, err
	}
	return out, nil
}

// walkCcRoot collects every <base>/<slug>/memory/**/*.md file. A missing base
// (ENOENT) yields an empty slice; slugs without a memory subdirectory are
// silently skipped.
func walkCcRoot(base string) ([]string, error) {
	slugs, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, entry := range slugs {
		if !entry.IsDir() {
			continue
		}
		memoryDir := filepath.Join(base, entry.Name(), "memory")
		info, err := os.Stat(memoryDir)
		if err != nil || !info.IsDir() {
			continue
		}
		files, err := walkMemoryDir(memoryDir)
		if err != nil {
			return nil, err
		}
		out = append(out, files...)
	}
	return out, nil
}

// Reconcile performs a lazy sync between the memory files on disk and the
// memory_index table. It walks both the mimo root and (when configured) the cc
// base, prunes rows whose file no longer exists, and indexes new or changed
// files. Unchanged files are skipped via a size-mtime fingerprint. The FTS
// index is kept consistent by the memory_ai/ad/au triggers, so only
// memory_index is touched here.
func (s *Store) Reconcile() (Result, error) {
	var res Result

	// Collect disk paths from BOTH roots BEFORE pruning. Pruning per-root would
	// wrongly wipe the other root's rows, because each walk's set is missing the
	// other root's paths.
	mimoFiles, err := walkMemoryDir(s.root)
	if err != nil {
		return res, fmt.Errorf("memory: walk mimo root %q: %w", s.root, err)
	}
	var ccFiles []string
	if s.ccBase != "" {
		ccFiles, err = walkCcRoot(s.ccBase)
		if err != nil {
			return res, fmt.Errorf("memory: walk cc base %q: %w", s.ccBase, err)
		}
	}

	diskPaths := make(map[string]struct{}, len(mimoFiles)+len(ccFiles))
	for _, p := range mimoFiles {
		diskPaths[filepath.Clean(p)] = struct{}{}
	}
	for _, p := range ccFiles {
		diskPaths[filepath.Clean(p)] = struct{}{}
	}

	// Load existing {path -> fingerprint} from memory_index.
	existing, err := s.loadFingerprints()
	if err != nil {
		return res, err
	}

	// PRUNE: delete rows whose path is no longer on disk.
	for p := range existing {
		if _, ok := diskPaths[p]; ok {
			continue
		}
		if _, err := s.db.Exec(`DELETE FROM memory_index WHERE path = ?`, p); err != nil {
			return res, fmt.Errorf("memory: prune %q: %w", p, err)
		}
		res.Pruned++
	}

	// INDEX: mimo files use parsePath and keep loc.Type.
	for _, p := range mimoFiles {
		loc, ok := parsePath(s.root, p)
		if !ok {
			continue
		}
		updated, err := s.indexFile(loc, loc.Type, false, existing[filepath.Clean(p)])
		if err != nil {
			return res, err
		}
		if updated {
			res.Indexed++
		}
	}

	// INDEX: cc files use parseCcPath; final type is derived from frontmatter.
	for _, p := range ccFiles {
		loc, ok := parseCcPath(s.ccBase, p)
		if !ok {
			continue
		}
		updated, err := s.indexFile(loc, loc.Type, true, existing[filepath.Clean(p)])
		if err != nil {
			return res, err
		}
		if updated {
			res.Indexed++
		}
	}

	return res, nil
}

// loadFingerprints returns the current {path -> fingerprint} map from
// memory_index.
func (s *Store) loadFingerprints() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT path, fingerprint FROM memory_index`)
	if err != nil {
		return nil, fmt.Errorf("memory: load fingerprints: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var path, fp string
		if err := rows.Scan(&path, &fp); err != nil {
			return nil, fmt.Errorf("memory: scan fingerprint: %w", err)
		}
		out[path] = fp
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: iterate fingerprints: %w", err)
	}
	return out, nil
}

// indexFile stats loc.Path, computes its size-mtime fingerprint, and upserts the
// row when the fingerprint differs from oldFingerprint. It returns true when a
// row was inserted or updated. A file that vanished between the walk and the
// stat (ENOENT) is silently skipped. For cc files (isCc) the semantic type is
// derived from the file's YAML frontmatter, falling back to defaultType.
func (s *Store) indexFile(loc *Locator, defaultType Type, isCc bool, oldFingerprint string) (bool, error) {
	info, err := os.Stat(loc.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("memory: stat %q: %w", loc.Path, err)
	}

	fingerprint := fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano())
	if oldFingerprint == fingerprint {
		return false, nil // hit: unchanged file
	}

	raw, err := os.ReadFile(loc.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("memory: read %q: %w", loc.Path, err)
	}
	body := string(raw)

	finalType := defaultType
	if isCc {
		finalType = parseCcFrontmatterType(body)
	}

	now := time.Now().UnixNano()
	const upsert = `
INSERT INTO memory_index (path, scope, scope_id, type, body, fingerprint, last_indexed_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
  scope = excluded.scope,
  scope_id = excluded.scope_id,
  type = excluded.type,
  body = excluded.body,
  fingerprint = excluded.fingerprint,
  last_indexed_at = excluded.last_indexed_at`
	if _, err := s.db.Exec(upsert,
		loc.Path, string(loc.Scope), loc.ScopeID, string(finalType), body, fingerprint, now,
	); err != nil {
		return false, fmt.Errorf("memory: upsert %q: %w", loc.Path, err)
	}
	return true, nil
}
