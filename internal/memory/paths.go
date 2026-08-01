package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Locator is the parsed identity of a memory file on disk: which scope it
// belongs to, the scope id (project-id / session-id / slug; "" for global), the
// semantic type, and the absolute path of the file itself.
type Locator struct {
	Scope   Scope
	ScopeID string
	Type    Type
	Path    string
}

// knownTypes maps a layout <type> directory segment (or a frontmatter
// metadata.type value) to its Type. Anything not present here resolves to
// TypeFree.
var knownTypes = map[string]Type{
	string(TypeUser):       TypeUser,
	string(TypeFeedback):   TypeFeedback,
	string(TypeProject):    TypeProject,
	string(TypeReference):  TypeReference,
	string(TypeCheckpoint): TypeCheckpoint,
	string(TypeProgress):   TypeProgress,
	string(TypeNotes):      TypeNotes,
	string(TypeFree):       TypeFree,
}

// scopeForMarker maps a top-level layout directory name to its Scope.
var scopeForMarker = map[string]Scope{
	"global":   ScopeGlobal,
	"projects": ScopeProjects,
	"sessions": ScopeSessions,
}

// typeFromDir maps a <type> directory segment to a Type, defaulting to TypeFree
// for unknown segments.
func typeFromDir(seg string) Type {
	if t, ok := knownTypes[strings.ToLower(seg)]; ok {
		return t
	}
	return TypeFree
}

// parsePath parses an absolute path in the mimo memory layout, relative to root,
// into a Locator. Recognized shapes:
//
//	<root>/global/<type>/*.md               -> ScopeGlobal,   ScopeID="",        Type from <type>
//	<root>/projects/<projectId>/<type>/*.md  -> ScopeProjects, ScopeID=projectId, Type from <type>
//	<root>/sessions/<sessionId>/<type>/*.md  -> ScopeSessions, ScopeID=sessionId, Type from <type>
//
// A .md file directly under a scope dir (e.g. <root>/projects/<id>/MEMORY.md, or
// <root>/global/MEMORY.md) has no <type> segment and resolves to TypeFree.
// Unknown <type> segments also map to TypeFree. It returns (nil, false) for any
// path outside root or outside the layout.
func parsePath(root, absPath string) (*Locator, bool) {
	rel, ok := relComponents(root, absPath)
	if !ok || len(rel) == 0 {
		return nil, false
	}

	scope, ok := scopeForMarker[rel[0]]
	if !ok {
		return nil, false
	}

	// rest is everything below the top-level scope marker.
	rest := rel[1:]

	var scopeID string
	switch scope {
	case ScopeGlobal:
		// rest = [<type>/]<file>.md
	case ScopeProjects, ScopeSessions:
		// rest = <scopeId>/[<type>/]<file>.md
		if len(rest) == 0 {
			return nil, false
		}
		scopeID = rest[0]
		rest = rest[1:]
	default:
		return nil, false
	}

	// A file component is mandatory and must be a Markdown file.
	if len(rest) == 0 || !isMarkdown(rest[len(rest)-1]) {
		return nil, false
	}

	typ := TypeFree
	if len(rest) >= 2 {
		// rest = <type>/.../<file>.md — the first segment names the type.
		typ = typeFromDir(rest[0])
	}

	return &Locator{
		Scope:   scope,
		ScopeID: scopeID,
		Type:    typ,
		Path:    filepath.Clean(absPath),
	}, true
}

// parseCcPath parses a Claude Code layout path relative to ccBase:
//
//	<ccBase>/<slug>/memory/**/*.md -> ScopeCC, ScopeID=<slug>, Type=TypeFree
//
// The real type is derived later from the file's YAML frontmatter via
// parseCcFrontmatterType. It returns (nil, false) for paths outside the layout.
func parseCcPath(ccBase, absPath string) (*Locator, bool) {
	rel, ok := relComponents(ccBase, absPath)
	if !ok {
		return nil, false
	}
	// Need at least <slug>/memory/<file>.md.
	if len(rel) < 3 || rel[1] != "memory" {
		return nil, false
	}
	if !isMarkdown(rel[len(rel)-1]) {
		return nil, false
	}
	return &Locator{
		Scope:   ScopeCC,
		ScopeID: rel[0],
		Type:    TypeFree,
		Path:    filepath.Clean(absPath),
	}, true
}

// ccFrontmatter is the minimal shape read from a Claude Code memory file's
// leading YAML frontmatter: either a nested metadata.type or a top-level type.
type ccFrontmatter struct {
	Type     string `yaml:"type"`
	Metadata struct {
		Type string `yaml:"type"`
	} `yaml:"metadata"`
}

// parseCcFrontmatterType reads the semantic type from a leading YAML frontmatter
// block (a "---" fence at the very start of body). It prefers metadata.type,
// falling back to a top-level type. Absent, malformed, or unrecognized values
// yield TypeFree.
func parseCcFrontmatterType(body string) Type {
	block, ok := frontmatterBlock(body)
	if !ok {
		return TypeFree
	}
	var fm ccFrontmatter
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return TypeFree
	}
	candidate := fm.Metadata.Type
	if candidate == "" {
		candidate = fm.Type
	}
	if t, ok := knownTypes[strings.ToLower(strings.TrimSpace(candidate))]; ok {
		return t
	}
	return TypeFree
}

// resolveProjectId derives a stable project id from an absolute repository path:
// the first 12 hex characters of sha256(absRepoPath). It is deterministic and is
// used as the scope_id for the projects scope.
func resolveProjectId(absRepoPath string) string {
	sum := sha256.Sum256([]byte(absRepoPath))
	return hex.EncodeToString(sum[:])[:12]
}

// assertSafeComponent rejects a caller-supplied path component that would escape
// the memory root: any ".." segment or a leading "/" (absolute path). Empty
// components are also rejected. The write path uses this to sanitize
// scope_id/type/filename before joining them under root.
func assertSafeComponent(name string) error {
	if name == "" {
		return fmt.Errorf("memory: empty path component")
	}
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("memory: unsafe path component %q: leading %q", name, "/")
	}
	for _, seg := range strings.Split(filepath.ToSlash(name), "/") {
		if seg == ".." {
			return fmt.Errorf("memory: unsafe path component %q: %q segment", name, "..")
		}
	}
	return nil
}

// relComponents returns absPath expressed relative to base, split into
// non-empty components. ok is false when absPath is not located under base.
func relComponents(base, absPath string) (parts []string, ok bool) {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(absPath))
	if err != nil {
		return nil, false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return nil, false
	}
	// Escaping base (e.g. "../foo") means the path is outside the layout.
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, false
	}
	return strings.Split(rel, "/"), true
}

// isMarkdown reports whether name has a .md extension (case-insensitive).
func isMarkdown(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".md")
}

// frontmatterBlock returns the raw YAML between a leading "---" fence and the
// next "---" line. ok is false when body has no opening fence at its very start.
func frontmatterBlock(body string) (string, bool) {
	rest := strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(rest, "---\n") {
		return "", false
	}
	rest = strings.TrimPrefix(rest, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
