package dream

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// NearDupThreshold is the normalized-token Jaccard similarity at or above which
// two memory files are emitted as a near-duplicate candidate pair for the later
// LLM merge-decision step (spec §5.1.1). It is deliberately conservative: this
// node only PAIRS candidates, it never merges, so a false-positive pair costs
// only one extra LLM comparison while a missed pair silently loses a merge
// opportunity.
const NearDupThreshold = 0.7

// MemoryFile is one enumerated memory file under the global or the active
// project scope, with the derived metadata the deterministic plan needs. Body
// is retained so the near-duplicate pairing can tokenize it without a second
// read.
type MemoryFile struct {
	Path        string // absolute path on disk
	Scope       string // "global" | "projects"
	Type        string // layout <type> segment ("" for a file directly under the scope root)
	Size        int64  // byte size of the file content
	ContentHash string // sha256 hex of the raw file content
	Body        string // full file content (used for path extraction + tokenization)
}

// DedupeGroup is a set of two or more files whose content is byte-identical
// (same ContentHash). The apply node keeps one representative and removes the
// rest; Deduped in the Report counts len(Paths)-1 per group.
type DedupeGroup struct {
	Hash  string   `json:"hash"`
	Paths []string `json:"paths"`
}

// InvalidPathRef is a local filesystem path mentioned in a memory file's body
// that no longer exists on disk. External references (URLs, mailto:, etc.) are
// never recorded here (spec §5.2 / PRD US-004).
type InvalidPathRef struct {
	File string `json:"file"` // memory file containing the reference
	Ref  string `json:"ref"`  // the original (unresolved) reference text
}

// NearDupPair is a candidate pair of files whose token-set similarity is at or
// above NearDupThreshold but whose content is not byte-identical. It is a
// suggestion for the LLM to decide whether an actual merge is warranted; this
// node performs no merge.
type NearDupPair struct {
	A          string  `json:"a"`
	B          string  `json:"b"`
	Similarity float64 `json:"similarity"`
}

// Plan is the plain-data output of the deterministic half of a /dream run. It
// carries no behavior and makes no LLM calls; the later apply node consumes it
// to drive merges/prunes and to compute the final Report counters.
type Plan struct {
	Files           []MemoryFile     `json:"files"`
	DedupeGroups    []DedupeGroup    `json:"dedupe_groups"`
	InvalidPathRefs []InvalidPathRef `json:"invalid_path_refs"`
	NearDupPairs    []NearDupPair    `json:"near_dup_pairs"`
	BytesBefore     int64            `json:"bytes_before"`
	FilesBefore     int              `json:"files_before"`
}

// BuildPlan enumerates the consolidation-eligible memory files (global scope +
// the given project's scope) under memoryRoot and computes the deterministic
// consolidation plan: exact-dedup groups, invalid local path references, and
// near-duplicate candidate pairs. It excludes the sessions scope entirely and
// any file whose layout type is "checkpoint" (session-transient state, not
// long-term memory — spec §1.3 / §5.1.1).
//
// projectDir is the working directory of the active project; its stable project
// id (matching internal/memory's resolveProjectId) selects the projects
// sub-scope. An empty projectDir restricts the plan to the global scope. A
// missing memoryRoot or scope directory is not an error — it yields an empty
// plan, mirroring memory.Reconcile's tolerance of an absent root.
func BuildPlan(memoryRoot, projectDir string) (Plan, error) {
	var plan Plan

	globalRoot := filepath.Join(memoryRoot, "global")
	globalFiles, err := enumerateScope(globalRoot, "global")
	if err != nil {
		return Plan{}, err
	}
	files := globalFiles

	if projectDir != "" {
		projectRoot := filepath.Join(memoryRoot, "projects", projectID(projectDir))
		projFiles, err := enumerateScope(projectRoot, "projects")
		if err != nil {
			return Plan{}, err
		}
		files = append(files, projFiles...)
	}

	plan.Files = files
	plan.FilesBefore = len(files)
	for _, f := range files {
		plan.BytesBefore += f.Size
	}

	plan.DedupeGroups = dedupeGroups(files)
	plan.InvalidPathRefs = invalidPathRefs(files, projectDir)
	plan.NearDupPairs = nearDupPairs(files)

	return plan, nil
}

// enumerateScope walks scopeRoot for *.md files, deriving each file's layout
// type from its first path segment relative to scopeRoot. Files under a
// "checkpoint" type directory are skipped. A missing scopeRoot yields no files
// and no error.
func enumerateScope(scopeRoot, scope string) ([]MemoryFile, error) {
	var out []MemoryFile
	err := filepath.WalkDir(scopeRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(scopeRoot, path)
		if relErr != nil {
			return nil
		}
		segs := strings.Split(filepath.ToSlash(rel), "/")
		typ := ""
		if len(segs) >= 2 {
			typ = strings.ToLower(segs[0])
		}
		if typ == "checkpoint" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				return nil
			}
			return readErr
		}
		sum := sha256.Sum256(raw)
		out = append(out, MemoryFile{
			Path:        filepath.Clean(path),
			Scope:       scope,
			Type:        typ,
			Size:        int64(len(raw)),
			ContentHash: hex.EncodeToString(sum[:]),
			Body:        string(raw),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Deterministic order regardless of directory iteration order.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// dedupeGroups groups files by identical content hash, returning only groups
// with two or more members (a file with a unique hash is not a duplicate). The
// result is stable: groups are ordered by hash, paths within a group by path.
func dedupeGroups(files []MemoryFile) []DedupeGroup {
	byHash := make(map[string][]string)
	for _, f := range files {
		byHash[f.ContentHash] = append(byHash[f.ContentHash], f.Path)
	}
	var groups []DedupeGroup
	for hash, paths := range byHash {
		if len(paths) < 2 {
			continue
		}
		sort.Strings(paths)
		groups = append(groups, DedupeGroup{Hash: hash, Paths: paths})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Hash < groups[j].Hash })
	return groups
}

// invalidPathRefs scans each file's body for local filesystem path references
// and records the ones that no longer exist. Relative references are resolved
// against projectDir; "~/" against the user home dir. URLs and other external
// references are never considered (see extractLocalPathRefs).
func invalidPathRefs(files []MemoryFile, projectDir string) []InvalidPathRef {
	var out []InvalidPathRef
	for _, f := range files {
		seen := make(map[string]struct{})
		for _, ref := range extractLocalPathRefs(f.Body) {
			if _, dup := seen[ref]; dup {
				continue
			}
			seen[ref] = struct{}{}
			// A bare relative reference is only meaningful against a project
			// base. Without one (global-only plan) skip it rather than
			// resolving against an arbitrary cwd and flagging spuriously.
			isRelative := !filepath.IsAbs(ref) && !strings.HasPrefix(ref, "~/")
			if isRelative && projectDir == "" {
				continue
			}
			resolved := resolveRef(ref, projectDir)
			if _, err := os.Stat(resolved); err != nil && os.IsNotExist(err) {
				out = append(out, InvalidPathRef{File: f.Path, Ref: ref})
			}
		}
	}
	return out
}

// nearDupPairs emits candidate pairs whose normalized-token Jaccard similarity
// is at or above NearDupThreshold. Pairs of byte-identical files are skipped:
// those are exact duplicates handled by dedupeGroups, not near-duplicates.
func nearDupPairs(files []MemoryFile) []NearDupPair {
	tokens := make([]map[string]struct{}, len(files))
	for i, f := range files {
		tokens[i] = tokenize(f.Body)
	}
	var out []NearDupPair
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if files[i].ContentHash == files[j].ContentHash {
				continue
			}
			sim := jaccard(tokens[i], tokens[j])
			if sim >= NearDupThreshold {
				out = append(out, NearDupPair{A: files[i].Path, B: files[j].Path, Similarity: sim})
			}
		}
	}
	return out
}

// projectID derives the stable projects-scope id from a project directory,
// mirroring internal/memory.resolveProjectId: the first 12 hex chars of
// sha256(absPath). The path is made absolute first so relative and absolute
// forms of the same directory map to the same id.
func projectID(projectDir string) string {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:12]
}

// extractLocalPathRefs returns the local filesystem path references found in
// body. It splits on whitespace and common Markdown/code delimiters (so
// `path`, [text](path) and bare tokens are all captured), then keeps only
// tokens that look like a local path while rejecting URLs and other external
// references.
func extractLocalPathRefs(body string) []string {
	fields := strings.FieldsFunc(body, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '`', '(', ')', '[', ']', '{', '}', '"', '\'', '<', '>', ',', ';', '|':
			return true
		}
		return false
	})
	var out []string
	for _, tok := range fields {
		// Trim trailing sentence punctuation that commonly abuts a path.
		tok = strings.TrimRight(tok, ".:!?")
		if isLocalPathRef(tok) {
			out = append(out, tok)
		}
	}
	return out
}

// isLocalPathRef reports whether tok looks like a local filesystem path rather
// than a URL or other external reference. It accepts absolute ("/..."),
// explicitly relative ("./", "../") and home-relative ("~/") tokens outright;
// a bare relative token (no leading marker) must contain a separator AND a file
// extension in its last segment, so ordinary prose like "TCP/IP", "read/write"
// or "and/or" is not mistaken for a path. URL-schemed tokens are rejected.
func isLocalPathRef(tok string) bool {
	if tok == "" {
		return false
	}
	if strings.Contains(tok, "://") {
		return false
	}
	lower := strings.ToLower(tok)
	for _, scheme := range []string{"http:", "https:", "ftp:", "ftps:", "file:", "mailto:", "ssh:", "git:", "www."} {
		if strings.HasPrefix(lower, scheme) {
			return false
		}
	}
	switch {
	case strings.HasPrefix(tok, "/"),
		strings.HasPrefix(tok, "./"),
		strings.HasPrefix(tok, "../"),
		strings.HasPrefix(tok, "~/"):
		return true
	}
	// A bare relative token needs both a separator and an extension on its last
	// segment to be treated as a path (avoids flagging prose like "TCP/IP").
	if !strings.Contains(tok, "/") {
		return false
	}
	last := tok[strings.LastIndex(tok, "/")+1:]
	return strings.Contains(last, ".") && !strings.HasSuffix(last, ".")
}

// resolveRef turns a reference into an absolute path for existence checking:
// "~/" expands to the user home dir, absolute paths pass through, and relative
// paths resolve against projectDir (or the current dir when projectDir is "").
func resolveRef(ref, projectDir string) string {
	if strings.HasPrefix(ref, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ref[2:])
		}
	}
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref)
	}
	return filepath.Join(projectDir, ref)
}

// tokenize lowercases body and splits it into the set of alphanumeric tokens
// used for near-duplicate similarity. Punctuation and Markdown syntax are
// discarded so formatting differences do not affect the score.
func tokenize(body string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, tok := range strings.FieldsFunc(strings.ToLower(body), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		set[tok] = struct{}{}
	}
	return set
}

// jaccard returns |a∩b| / |a∪b|. Two empty sets are dissimilar (0) rather than
// identical, so blank files are never paired as near-duplicates.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
