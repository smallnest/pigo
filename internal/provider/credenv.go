// This file implements the credential-hygiene primitives from issue #568
// (ported from dsh's credential handling):
//
//   - ScrubEnv: derive a child-process environment with credential-shaped
//     variables removed, so a spawned process (sub-agent, plugin) does not
//     inherit secrets it does not need. Mirrors dsh's stdio bridge, which
//     "deliberately removes ambient variables whose names usually identify
//     credentials and all DSH_* variables"; pigo scrubs credential-shaped
//     names and all PIGO_* internals. Explicit allow entries are re-injected
//     last, so a parent can still hand a child exactly the one key it needs.
//   - CredentialFile: a named-credential store at $PIGO_HOME/.credentials.yaml
//     (mode 0600). Config files reference a credential by NAME; the literal
//     secret lives only here, mirroring dsh's write-only keys + "settings
//     retain only its credential reference".
package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// credentialNameSuffixes are the name shapes that almost always carry a secret.
// Matching is case-insensitive against the variable name.
var credentialNameSuffixes = []string{
	"_API_KEY", "_APIKEY", "_AUTH", "_CREDENTIALS",
}

// credentialNameSubstrings catch names that carry a secret-shaped word in the
// middle (e.g. AWS_SECRET_ACCESS_KEY has no matching suffix, GATEWAY_TOKEN_FILE
// carries TOKEN mid-name). Matching is deliberately fail-closed: an over-scrubbed
// name can always be re-injected through the allow list.
var credentialNameSubstrings = []string{
	"PASSWORD", "CREDENTIAL", "PRIVATE_KEY", "SECRET", "TOKEN",
}

// ScrubsCredentialVar reports whether an environment variable name is
// credential-shaped and should be stripped from child-process environments.
// All PIGO_* internals are scrubbed too (they are pigo's own state, not a
// child's business); PATH/HOME and other ordinary names never match.
func ScrubsCredentialVar(name string) bool {
	n := strings.ToUpper(name)
	if strings.HasPrefix(n, "PIGO_") {
		return true
	}
	for _, suffix := range credentialNameSuffixes {
		if strings.HasSuffix(n, suffix) {
			return true
		}
	}
	for _, sub := range credentialNameSubstrings {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// ScrubEnv filters a parent environment for a child process: every
// credential-shaped variable and every PIGO_* internal is dropped, then the
// allow entries are appended (so a parent can hand a child exactly the one
// credential it needs, under a name that would otherwise be scrubbed).
// Malformed parent entries (no "=") are dropped. The result is safe to assign
// to os/exec's cmd.Env.
func ScrubEnv(parent []string, allow map[string]string) []string {
	out := make([]string, 0, len(parent)+len(allow))
	for _, entry := range parent {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		if ScrubsCredentialVar(name) {
			continue
		}
		out = append(out, entry)
	}
	for _, name := range sortedKeys(allow) {
		out = append(out, name+"="+allow[name])
	}
	return out
}

// sortedKeys returns map keys in deterministic order so scrubbed environments
// are stable (and tests can assert on them).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// LoadCredentialFile reads a named-credential file (YAML mapping of
// name → secret). The file is expected to be mode 0600; callers can use
// CredentialFilePermissionsWarn to check.
func LoadCredentialFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("credential file %s: %w", path, err)
	}
	return out, nil
}

// CredentialFilePermissionsWarn reports whether the credential file's mode is
// more permissive than 0600 (group/other access), so the caller can warn
// without failing.
func CredentialFilePermissionsWarn(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0o077 != 0
}

// CredentialFilePath returns the named-credential file location:
// $PIGO_HOME/.credentials.yaml when PIGO_HOME is set, else
// ~/.pigo/.credentials.yaml. An unresolvable home returns "".
func CredentialFilePath() string {
	if dir := os.Getenv("PIGO_HOME"); dir != "" {
		return filepath.Join(dir, ".credentials.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pigo", ".credentials.yaml")
}

// ResolveCredentialReference loads the named-credential file and returns the
// secret stored under name. Config files should carry the name only; the
// literal secret exists here alone.
func ResolveCredentialReference(path, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("empty credential reference")
	}
	creds, err := LoadCredentialFile(path)
	if err != nil {
		return "", err
	}
	key, ok := creds[name]
	if !ok || key == "" {
		return "", fmt.Errorf("credential %q not found in %s", name, path)
	}
	return key, nil
}
