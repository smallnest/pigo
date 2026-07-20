package pkgmgr

import "testing"

// TestParsePlainName verifies a plain npm reference parses.
func TestParsePlainName(t *testing.T) {
	r, err := ParsePackageRef("npm:pi-mcp-adapter")
	if err != nil {
		t.Fatalf("ParsePackageRef: %v", err)
	}
	if r.Registry != RegistryNPM {
		t.Errorf("Registry = %q, want npm", r.Registry)
	}
	if r.Name != "pi-mcp-adapter" {
		t.Errorf("Name = %q, want pi-mcp-adapter", r.Name)
	}
	if r.Version != "" {
		t.Errorf("Version = %q, want empty", r.Version)
	}
}

// TestParseScopedName verifies a scoped npm reference parses with the leading
// '@' preserved and no false version split.
func TestParseScopedName(t *testing.T) {
	r, err := ParsePackageRef("npm:@gotgenes/pi-subagents")
	if err != nil {
		t.Fatalf("ParsePackageRef: %v", err)
	}
	if r.Name != "@gotgenes/pi-subagents" {
		t.Errorf("Name = %q, want @gotgenes/pi-subagents", r.Name)
	}
	if r.Version != "" {
		t.Errorf("Version = %q, want empty", r.Version)
	}
}

// TestParseWithVersion verifies the "@version" suffix splits off correctly for
// both plain and scoped names.
func TestParseWithVersion(t *testing.T) {
	cases := []struct {
		ref, name, version string
	}{
		{"npm:pi-mcp-adapter@1.2.3", "pi-mcp-adapter", "1.2.3"},
		{"npm:@scope/name@0.1.0", "@scope/name", "0.1.0"},
		{"npm:@scope/name", "@scope/name", ""},
		{"npm:pkg@latest", "pkg", "latest"},
	}
	for _, c := range cases {
		r, err := ParsePackageRef(c.ref)
		if err != nil {
			t.Errorf("ParsePackageRef(%q): %v", c.ref, err)
			continue
		}
		if r.Name != c.name || r.Version != c.version {
			t.Errorf("ParsePackageRef(%q) = {%q, %q}, want {%q, %q}", c.ref, r.Name, r.Version, c.name, c.version)
		}
	}
}

// TestParseMissingPrefix verifies a reference without npm: is rejected.
func TestParseMissingPrefix(t *testing.T) {
	for _, ref := range []string{"pi-mcp-adapter", ""} {
		if _, err := ParsePackageRef(ref); err == nil {
			t.Errorf("ParsePackageRef(%q) = nil error, want error", ref)
		}
	}
}

// TestParseUnsupportedPrefix verifies non-npm sources are rejected (github:/file:).
func TestParseUnsupportedPrefix(t *testing.T) {
	for _, ref := range []string{"github:owner/repo", "file:./local", "pypi:foo"} {
		if _, err := ParsePackageRef(ref); err == nil {
			t.Errorf("ParsePackageRef(%q) = nil error, want error", ref)
		}
	}
}

// TestParseInvalidName verifies illegal npm names are rejected.
func TestParseInvalidName(t *testing.T) {
	cases := []string{
		"npm:has space",       // whitespace
		"npm:UPPER",           // uppercase
		"npm:bad;rm -rf",      // shell metacharacters
		"npm:.hidden",         // leading dot
		"npm:_underscore",     // leading underscore
		"npm:@scope",          // scope without name
		"npm:@/name",          // empty scope
		"npm:@scope/",         // empty name
		"npm:a/b/c",           // too many slashes (unscoped with slash)
		"npm:",                // empty name after prefix
	}
	for _, ref := range cases {
		if _, err := ParsePackageRef(ref); err == nil {
			t.Errorf("ParsePackageRef(%q) = nil error, want error", ref)
		}
	}
}

// TestRefStringRoundTrips verifies String reconstructs the canonical reference.
func TestRefStringRoundTrips(t *testing.T) {
	cases := []string{
		"npm:pi-mcp-adapter",
		"npm:pi-mcp-adapter@1.2.3",
		"npm:@scope/name@0.1.0",
	}
	for _, ref := range cases {
		r, err := ParsePackageRef(ref)
		if err != nil {
			t.Fatalf("ParsePackageRef(%q): %v", ref, err)
		}
		if got := r.String(); got != ref {
			t.Errorf("String() = %q, want %q", got, ref)
		}
	}
}

// TestParseTrimsWhitespace verifies surrounding whitespace is tolerated.
func TestParseTrimsWhitespace(t *testing.T) {
	r, err := ParsePackageRef("  npm:pi-web-access  ")
	if err != nil {
		t.Fatalf("ParsePackageRef: %v", err)
	}
	if r.Name != "pi-web-access" {
		t.Errorf("Name = %q, want pi-web-access", r.Name)
	}
}
