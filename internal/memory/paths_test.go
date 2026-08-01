package memory

import (
	"path/filepath"
	"testing"
)

func TestParsePathScopesAndTypes(t *testing.T) {
	root := "/mem/root"

	cases := []struct {
		name    string
		path    string
		scope   Scope
		scopeID string
		typ     Type
	}{
		{
			name:  "global with type dir",
			path:  filepath.Join(root, "global", "user", "profile.md"),
			scope: ScopeGlobal, scopeID: "", typ: TypeUser,
		},
		{
			name:  "global unknown type dir -> free",
			path:  filepath.Join(root, "global", "whatever", "x.md"),
			scope: ScopeGlobal, scopeID: "", typ: TypeFree,
		},
		{
			name:  "global file directly under scope -> free",
			path:  filepath.Join(root, "global", "MEMORY.md"),
			scope: ScopeGlobal, scopeID: "", typ: TypeFree,
		},
		{
			name:  "projects with type dir",
			path:  filepath.Join(root, "projects", "abc123", "checkpoint", "c1.md"),
			scope: ScopeProjects, scopeID: "abc123", typ: TypeCheckpoint,
		},
		{
			name:  "projects file directly under id -> free",
			path:  filepath.Join(root, "projects", "abc123", "MEMORY.md"),
			scope: ScopeProjects, scopeID: "abc123", typ: TypeFree,
		},
		{
			name:  "sessions with type dir",
			path:  filepath.Join(root, "sessions", "sess-1", "notes", "n.md"),
			scope: ScopeSessions, scopeID: "sess-1", typ: TypeNotes,
		},
		{
			name:  "sessions progress type",
			path:  filepath.Join(root, "sessions", "sess-1", "progress", "p.md"),
			scope: ScopeSessions, scopeID: "sess-1", typ: TypeProgress,
		},
		{
			name:  "nested file under type dir keeps type",
			path:  filepath.Join(root, "projects", "abc123", "reference", "sub", "r.md"),
			scope: ScopeProjects, scopeID: "abc123", typ: TypeReference,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, ok := parsePath(root, tc.path)
			if !ok {
				t.Fatalf("parsePath(%q) returned ok=false", tc.path)
			}
			if loc.Scope != tc.scope {
				t.Errorf("scope = %q, want %q", loc.Scope, tc.scope)
			}
			if loc.ScopeID != tc.scopeID {
				t.Errorf("scopeID = %q, want %q", loc.ScopeID, tc.scopeID)
			}
			if loc.Type != tc.typ {
				t.Errorf("type = %q, want %q", loc.Type, tc.typ)
			}
			if loc.Path != filepath.Clean(tc.path) {
				t.Errorf("path = %q, want %q", loc.Path, filepath.Clean(tc.path))
			}
		})
	}
}

func TestParsePathOutsideLayout(t *testing.T) {
	root := "/mem/root"
	bad := []string{
		"/other/place/x.md",                         // outside root
		filepath.Join(root, "unknownscope", "x.md"), // not a layout scope
		filepath.Join(root, "global"),               // no file component
		filepath.Join(root, "projects", "abc123"),   // scope id dir, no file
		filepath.Join(root, "global", "user", "x.txt"), // not markdown
	}
	for _, p := range bad {
		if loc, ok := parsePath(root, p); ok {
			t.Errorf("parsePath(%q) = %+v, ok=true; want ok=false", p, loc)
		}
	}
}

func TestParseCcPath(t *testing.T) {
	base := "/home/u/.claude/projects"

	loc, ok := parseCcPath(base, filepath.Join(base, "my-slug", "memory", "some", "note.md"))
	if !ok {
		t.Fatalf("parseCcPath returned ok=false")
	}
	if loc.Scope != ScopeCC {
		t.Errorf("scope = %q, want %q", loc.Scope, ScopeCC)
	}
	if loc.ScopeID != "my-slug" {
		t.Errorf("scopeID = %q, want %q", loc.ScopeID, "my-slug")
	}
	if loc.Type != TypeFree {
		t.Errorf("type = %q, want %q", loc.Type, TypeFree)
	}

	bad := []string{
		filepath.Join(base, "my-slug", "note.md"),            // no memory segment
		filepath.Join(base, "my-slug", "notmemory", "n.md"),  // wrong segment
		filepath.Join(base, "my-slug", "memory", "note.txt"), // not markdown
		"/elsewhere/x/memory/n.md",                           // outside base
	}
	for _, p := range bad {
		if loc, ok := parseCcPath(base, p); ok {
			t.Errorf("parseCcPath(%q) = %+v, ok=true; want ok=false", p, loc)
		}
	}
}

func TestParseCcFrontmatterType(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Type
	}{
		{
			name: "nested metadata.type present",
			body: "---\nmetadata:\n  type: reference\n  other: x\n---\nbody here\n",
			want: TypeReference,
		},
		{
			name: "top-level type present",
			body: "---\ntype: feedback\n---\nbody\n",
			want: TypeFeedback,
		},
		{
			name: "metadata.type wins over top-level",
			body: "---\ntype: free\nmetadata:\n  type: project\n---\n",
			want: TypeProject,
		},
		{
			name: "absent frontmatter",
			body: "no frontmatter here\n",
			want: TypeFree,
		},
		{
			name: "empty frontmatter",
			body: "---\n---\nbody\n",
			want: TypeFree,
		},
		{
			name: "unknown type value",
			body: "---\nmetadata:\n  type: bogus\n---\n",
			want: TypeFree,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCcFrontmatterType(tc.body); got != tc.want {
				t.Errorf("parseCcFrontmatterType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveProjectId(t *testing.T) {
	const p = "/Users/dev/repo"
	a := resolveProjectId(p)
	b := resolveProjectId(p)
	if a != b {
		t.Errorf("resolveProjectId not stable: %q != %q", a, b)
	}
	if len(a) != 12 {
		t.Errorf("resolveProjectId length = %d, want 12", len(a))
	}
	if resolveProjectId("/Users/dev/other") == a {
		t.Errorf("resolveProjectId collided for distinct paths")
	}
}

func TestAssertSafeComponent(t *testing.T) {
	ok := []string{"user", "abc123", "sub/dir/file.md", "checkpoint"}
	for _, s := range ok {
		if err := assertSafeComponent(s); err != nil {
			t.Errorf("assertSafeComponent(%q) = %v, want nil", s, err)
		}
	}

	bad := []string{"", "..", "../etc", "a/../b", "/etc/passwd", "sub/../../x"}
	for _, s := range bad {
		if err := assertSafeComponent(s); err == nil {
			t.Errorf("assertSafeComponent(%q) = nil, want error", s)
		}
	}
}
