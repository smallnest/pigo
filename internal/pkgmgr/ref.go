// This file parses and validates the package reference a user passes to
// `pigo install` (#155). pi packages are published to npm, so a reference looks
// like `npm:pi-mcp-adapter`, `npm:@scope/name`, or either form with a version
// suffix (`npm:pi-mcp-adapter@1.2.3`). Parsing is deliberately strict: an
// unsupported source prefix or an invalid npm package name is rejected up front,
// so the install command fails fast with a clear message rather than handing a
// bad name to npm.
//
// Only the `npm:` source is supported this release (PRD Non-Goals exclude
// github:/file:), so Registry is effectively always "npm"; it is kept explicit
// so a future source can be added without changing callers.
package pkgmgr

import (
	"fmt"
	"strings"
)

// Registry identifies where a package is fetched from. Only npm is supported.
type Registry string

// RegistryNPM is the npm registry, the only supported source this release.
const RegistryNPM Registry = "npm"

// PackageRef is a parsed, validated install reference.
type PackageRef struct {
	// Registry is the source registry (always RegistryNPM this release).
	Registry Registry
	// Name is the bare package name, e.g. "pi-mcp-adapter" or "@scope/name".
	Name string
	// Version is the requested version (the part after '@'), or "" for latest.
	Version string
	// Raw is the original reference as typed, e.g. "npm:pi-mcp-adapter@1.2.3".
	Raw string
}

// String returns the canonical reference, reconstructed from the parsed parts.
func (r PackageRef) String() string {
	s := string(r.Registry) + ":" + r.Name
	if r.Version != "" {
		s += "@" + r.Version
	}
	return s
}

// ParsePackageRef parses an install reference of the form `npm:<name>[@version]`,
// where <name> is a plain (`pi-mcp-adapter`) or scoped (`@scope/name`) npm
// package name. It returns an error when the source prefix is missing or
// unsupported, or when the package name is invalid.
func ParsePackageRef(ref string) (PackageRef, error) {
	raw := strings.TrimSpace(ref)
	prefix, rest, found := strings.Cut(raw, ":")
	if !found || prefix == "" {
		return PackageRef{}, fmt.Errorf("unsupported package source, expected npm:<name>")
	}
	if Registry(prefix) != RegistryNPM {
		return PackageRef{}, fmt.Errorf("unsupported package source %q, expected npm:<name>", prefix)
	}
	if rest == "" {
		return PackageRef{}, fmt.Errorf("missing package name, expected npm:<name>")
	}

	name, version := splitNameVersion(rest)
	if err := validateNPMName(name); err != nil {
		return PackageRef{}, err
	}
	return PackageRef{
		Registry: RegistryNPM,
		Name:     name,
		Version:  version,
		Raw:      raw,
	}, nil
}

// splitNameVersion separates a name from an optional trailing "@version". A
// leading '@' (scoped package) is preserved: the split happens on the LAST '@'
// only when it is not the scope marker, so "@scope/name@1.2.3" yields
// ("@scope/name", "1.2.3") and "@scope/name" yields ("@scope/name", "").
func splitNameVersion(s string) (name, version string) {
	// For a scoped name, the first char is '@' and is not a version separator.
	searchFrom := 0
	if strings.HasPrefix(s, "@") {
		searchFrom = 1
	}
	if idx := strings.LastIndex(s[searchFrom:], "@"); idx >= 0 {
		at := searchFrom + idx
		return s[:at], s[at+1:]
	}
	return s, ""
}

// validateNPMName checks a bare npm package name against the parts of the npm
// naming rules that matter for safely handing the name to the npm CLI: non-empty,
// no whitespace or control characters, no shell-hostile characters, reasonable
// length, and — for scoped names — a well-formed "@scope/name" shape. It does not
// aim to replicate every nuance of npm's validate-npm-package-name; it rejects
// the classes of input that would be unsafe or clearly wrong.
func validateNPMName(name string) error {
	if name == "" {
		return fmt.Errorf("invalid npm package name: empty")
	}
	if len(name) > 214 {
		return fmt.Errorf("invalid npm package name %q: exceeds 214 characters", name)
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("invalid npm package name %q: must be lowercase", name)
	}
	for _, r := range name {
		if r <= ' ' || r == '\x7f' {
			return fmt.Errorf("invalid npm package name %q: contains whitespace or control character", name)
		}
		switch r {
		case '"', '\'', '\\', '`', '$', '(', ')', '<', '>', '|', ';', '&', '*', '?', '#', '%', '^', '{', '}', '[', ']', ',', '!', '~', '=', '+', ':':
			return fmt.Errorf("invalid npm package name %q: contains illegal character %q", name, string(r))
		}
	}

	if strings.HasPrefix(name, "@") {
		return validateScopedName(name)
	}
	// Unscoped names may not start with '.' or '_'.
	if name[0] == '.' || name[0] == '_' {
		return fmt.Errorf("invalid npm package name %q: may not start with '.' or '_'", name)
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("invalid npm package name %q: only scoped names may contain '/'", name)
	}
	return nil
}

// validateScopedName validates the "@scope/name" shape for a scoped package.
func validateScopedName(name string) error {
	rest := strings.TrimPrefix(name, "@")
	scope, pkg, found := strings.Cut(rest, "/")
	if !found {
		return fmt.Errorf("invalid scoped package name %q: expected @scope/name", name)
	}
	if scope == "" || pkg == "" {
		return fmt.Errorf("invalid scoped package name %q: empty scope or name", name)
	}
	if strings.Contains(pkg, "/") {
		return fmt.Errorf("invalid scoped package name %q: too many '/' separators", name)
	}
	if pkg[0] == '.' || pkg[0] == '_' {
		return fmt.Errorf("invalid scoped package name %q: name may not start with '.' or '_'", name)
	}
	return nil
}
