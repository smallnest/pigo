// This file implements the sandbox permission policy engine (US-026): a pure
// function gate Evaluate(ctx, Request) Decision returning allow / prompt / deny.
// It backs the beforeToolCall hook (see SandboxGate) and enforces a read-all /
// write-jail posture: reads are broadly permitted, writes are confined to the
// workspace jail, .git is protected, and symlinks are resolved before the
// boundary check so a link cannot escape the jail. The gate NEVER fails open:
// on any ambiguity it denies (or prompts) rather than allowing.
package agenttool

import (
	"context"
	"path/filepath"
	"strings"
)

// Decision is the outcome of a policy evaluation. The zero value is DecisionDeny
// so a policy that forgets to set an outcome fails closed, never open.
type Decision int

const (
	// DecisionDeny blocks the operation outright.
	DecisionDeny Decision = iota
	// DecisionPrompt requires per-operation human approval before proceeding.
	DecisionPrompt
	// DecisionAllow permits the operation.
	DecisionAllow
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionPrompt:
		return "prompt"
	default:
		return "deny"
	}
}

// AccessKind classifies what a Request wants to do, so the policy can apply the
// read-all / write-jail posture.
type AccessKind int

const (
	// AccessRead is a read-only operation (broadly allowed).
	AccessRead AccessKind = iota
	// AccessWrite mutates the filesystem (confined to the jail).
	AccessWrite
	// AccessExec runs a shell command (classified by risk).
	AccessExec
	// AccessNetwork performs network I/O (allow-list only).
	AccessNetwork
)

// Request is the input to Evaluate. It is a plain value so evaluation is a pure
// function: the same Request always yields the same Decision under a Policy.
type Request struct {
	Kind AccessKind
	// Path is the target file/dir for read/write requests (may be relative).
	Path string
	// Command is the shell command line for exec requests.
	Command string
	// Host is the target host for network requests.
	Host string
}

// Policy is the declarative permission configuration. All slices are optional;
// an empty Policy applies only the built-in posture (read-all, write-jail to
// Root, protect .git, deny exec/network unless allow-listed).
type Policy struct {
	// Root is the workspace jail root for writes. Empty defaults to CWD at
	// evaluation time (resolved via resolveWithin's rules).
	Root string
	// DenyPaths are path prefixes (relative to Root or absolute) that are denied
	// for ALL access kinds, overriding any allow. Checked first.
	DenyPaths []string
	// AllowWritePaths are additional absolute/relative prefixes writable beyond
	// the jail (escape hatch, e.g. a temp dir). Use sparingly.
	AllowWritePaths []string
	// AllowCommands are command names (argv[0] base) permitted for exec. Empty
	// means "classify by risk" (safe commands allow, risky prompt, dangerous deny).
	AllowCommands []string
	// DenyCommands are command names always denied, overriding classification.
	DenyCommands []string
	// AllowHosts are hostnames permitted for network access. Empty denies all
	// network (fail closed).
	AllowHosts []string
	// ProtectGit, when true (the default via NewPolicy), denies writes anywhere
	// under a .git directory.
	ProtectGit bool
	// PromptOnRiskyExec, when true, returns DecisionPrompt (rather than deny) for
	// commands classified as risky-but-not-dangerous.
	PromptOnRiskyExec bool
}

// NewPolicy returns a Policy with the safe default posture: .git protected,
// risky commands prompt rather than deny.
func NewPolicy(root string) *Policy {
	return &Policy{
		Root:              root,
		ProtectGit:        true,
		PromptOnRiskyExec: true,
	}
}

// Evaluate is the pure policy gate. It never returns DecisionAllow when the
// request is ambiguous or resolution fails — it fails closed.
func (p *Policy) Evaluate(ctx context.Context, req Request) Decision {
	switch req.Kind {
	case AccessRead:
		return p.evalRead(req)
	case AccessWrite:
		return p.evalWrite(req)
	case AccessExec:
		return p.evalExec(req)
	case AccessNetwork:
		return p.evalNetwork(req)
	default:
		return DecisionDeny
	}
}

// evalRead implements the read-all posture: reads are allowed unless the path
// is explicitly denied. Resolution failure denies (fail closed).
func (p *Policy) evalRead(req Request) Decision {
	full, ok := p.resolve(req.Path)
	if !ok {
		return DecisionDeny
	}
	if p.pathDenied(full) {
		return DecisionDeny
	}
	return DecisionAllow
}

// evalWrite implements the write-jail posture: a write is allowed only if the
// resolved (symlink-followed) path stays within Root or an AllowWritePaths
// prefix, is not denied, and is not inside a protected .git.
func (p *Policy) evalWrite(req Request) Decision {
	full, ok := p.resolve(req.Path)
	if !ok {
		return DecisionDeny
	}
	if p.pathDenied(full) {
		return DecisionDeny
	}
	if p.ProtectGit && underGit(full) {
		return DecisionDeny
	}
	if p.withinJail(full) {
		return DecisionAllow
	}
	for _, ap := range p.AllowWritePaths {
		if abs, ok := p.resolve(ap); ok && hasPathPrefix(full, abs) {
			return DecisionAllow
		}
	}
	return DecisionDeny
}

// evalExec classifies the command. DenyCommands always deny; AllowCommands (if
// non-empty) gate to an allow-list; otherwise the AST risk classifier decides.
func (p *Policy) evalExec(req Request) Decision {
	if strings.TrimSpace(req.Command) == "" {
		return DecisionDeny
	}
	names := commandNames(req.Command)
	for _, n := range names {
		if containsFold(p.DenyCommands, n) {
			return DecisionDeny
		}
	}
	if len(p.AllowCommands) > 0 {
		for _, n := range names {
			if !containsFold(p.AllowCommands, n) {
				return DecisionDeny
			}
		}
		return DecisionAllow
	}
	switch classifyCommandRisk(req.Command) {
	case riskDangerous:
		return DecisionDeny
	case riskRisky:
		if p.PromptOnRiskyExec {
			return DecisionPrompt
		}
		return DecisionDeny
	default:
		return DecisionAllow
	}
}

// evalNetwork allows only allow-listed hosts (fail closed on empty list).
func (p *Policy) evalNetwork(req Request) Decision {
	if req.Host == "" || len(p.AllowHosts) == 0 {
		return DecisionDeny
	}
	if containsFold(p.AllowHosts, req.Host) {
		return DecisionAllow
	}
	return DecisionDeny
}

// resolve turns p into an absolute, symlink-followed path bounded conceptually
// by Root. It returns ok=false on any failure so callers fail closed. Symlinks
// are resolved (EvalSymlinks) so a link inside the jail that points outside is
// caught by the subsequent withinJail check. When the path does not yet exist
// (a write target), EvalSymlinks is applied to the deepest existing ancestor.
func (p *Policy) resolve(path string) (string, bool) {
	root := p.rootAbs()
	if root == "" {
		return "", false
	}
	var full string
	if filepath.IsAbs(path) {
		full = filepath.Clean(path)
	} else {
		full = filepath.Join(root, path)
	}
	return evalSymlinksLenient(full), true
}

// rootAbs returns the absolute jail root, defaulting to CWD when Root is empty.
func (p *Policy) rootAbs() string {
	root := p.Root
	if root == "" {
		wd, err := getwd()
		if err != nil {
			return ""
		}
		root = wd
	}
	abs, err := filepathAbs(root)
	if err != nil {
		return ""
	}
	return evalSymlinksLenient(abs)
}

// withinJail reports whether full stays within the resolved Root.
func (p *Policy) withinJail(full string) bool {
	root := p.rootAbs()
	if root == "" {
		return false
	}
	return hasPathPrefix(full, root)
}

// pathDenied reports whether full matches any DenyPaths prefix (checked first,
// overriding all allows).
func (p *Policy) pathDenied(full string) bool {
	for _, d := range p.DenyPaths {
		if abs, ok := p.resolve(d); ok && hasPathPrefix(full, abs) {
			return true
		}
	}
	return false
}

// hasPathPrefix reports whether path is prefix itself or lies beneath it,
// comparing whole path segments so "/a/bc" is not treated as under "/a/b".
func hasPathPrefix(path, prefix string) bool {
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+string(filepath.Separator))
}

// underGit reports whether full has a .git path segment (a .git dir or file).
func underGit(full string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(full), "/") {
		if seg == ".git" {
			return true
		}
	}
	return false
}

// containsFold reports whether list contains s (case-insensitive).
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
