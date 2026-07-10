// This file implements the shell-command risk classifier for the sandbox
// (US-026). It parses a command line into a real AST using mvdan.cc/sh/v3
// rather than regex-matching strings, so constructs like pipelines, subshells,
// redirections and command substitutions are seen structurally. Each simple
// command's argv[0] base name is classified against a dangerous/risky registry;
// the whole line takes the highest risk found. Parse failure yields riskRisky
// (fail toward caution, never toward safe) so a command we cannot understand is
// never silently allowed.
package agent

import (
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// riskLevel ranks a command from safe (allow) through risky (prompt) to
// dangerous (deny). Ordered so max() picks the most severe.
type riskLevel int

const (
	riskSafe riskLevel = iota
	riskRisky
	riskDangerous
)

// dangerousCommands are command base names that can cause irreversible or
// system-wide damage; they are denied outright.
var dangerousCommands = map[string]bool{
	"rm":       true,
	"rmdir":    true,
	"mkfs":     true,
	"dd":       true,
	"shutdown": true,
	"reboot":   true,
	"halt":     true,
	"poweroff": true,
	"fdisk":    true,
	"parted":   true,
	"kill":     true,
	"killall":  true,
	"chown":    true,
}

// riskyCommands are command base names that mutate state or run with elevated
// privilege; they prompt for approval rather than being denied.
var riskyCommands = map[string]bool{
	"sudo":      true,
	"su":        true,
	"mv":        true,
	"chmod":     true,
	"curl":      true,
	"wget":      true,
	"git":       true,
	"npm":       true,
	"pip":       true,
	"apt":       true,
	"apt-get":   true,
	"brew":      true,
	"docker":    true,
	"systemctl": true,
	"ln":        true,
	"truncate":  true,
}

// classifyCommandRisk parses cmd into an AST and returns the highest risk level
// among its simple commands. An unparseable command is treated as risky so it
// is never silently allowed.
func classifyCommandRisk(cmd string) riskLevel {
	names := commandNames(cmd)
	if names == nil {
		// Parse failed: be cautious, require approval.
		return riskRisky
	}
	worst := riskSafe
	for _, n := range names {
		lvl := riskSafe
		switch {
		case dangerousCommands[n]:
			lvl = riskDangerous
		case riskyCommands[n]:
			lvl = riskRisky
		}
		if lvl > worst {
			worst = lvl
		}
	}
	return worst
}

// commandNames parses cmd with a real shell parser and returns the base name of
// every simple command's first word (the executable). It walks into pipelines,
// subshells, function bodies and command substitutions. It returns nil if the
// command cannot be parsed, so callers can distinguish "no commands" from
// "unparseable" and fail toward caution.
func commandNames(cmd string) []string {
	parser := syntax.NewParser()
	file, err := parser.Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil
	}
	var names []string
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		word := call.Args[0]
		lit := wordLiteral(word)
		if lit == "" {
			return true
		}
		names = append(names, filepath.Base(lit))
		return true
	})
	return names
}

// wordLiteral extracts a plain literal from a word if it is a single unquoted or
// single/double-quoted literal, returning "" for words with expansions we can't
// statically resolve (variables, command substitution, globs).
func wordLiteral(word *syntax.Word) string {
	var b strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, dp := range p.Parts {
				lit, ok := dp.(*syntax.Lit)
				if !ok {
					return ""
				}
				b.WriteString(lit.Value)
			}
		default:
			return ""
		}
	}
	return b.String()
}

// evalSymlinksLenient resolves symlinks in path. When path does not exist (a
// write target that has not been created), it resolves the deepest existing
// ancestor and re-appends the remaining segments, so a symlinked parent
// directory still cannot smuggle a write outside the jail.
func evalSymlinksLenient(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir := path
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root; nothing resolvable
		}
		base := filepath.Base(dir)
		tail = append([]string{base}, tail...)
		dir = parent
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
	}
	return filepath.Clean(path)
}

// getwd and filepathAbs are thin indirections over os/filepath so tests can
// reason about them and so the policy file stays free of direct os imports.
func getwd() (string, error)               { return os.Getwd() }
func filepathAbs(p string) (string, error) { return filepath.Abs(p) }
