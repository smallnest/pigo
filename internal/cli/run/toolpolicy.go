package run

import (
	"fmt"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
)

// ToolPolicy is the user-declared tool boundary for a run: the --allowed-tools
// whitelist and the --disallowed-tools blacklist, already normalized by
// SplitToolNames. It is passed as one value rather than two adjacent []string
// parameters so the two lists cannot be swapped at a call site — silently
// inverting a security boundary is exactly the bug that must be impossible.
//
// The zero value means "no restriction" and every operation on it is a no-op.
type ToolPolicy struct {
	Allow []string
	Deny  []string
}

// NewToolPolicy normalizes raw flag values into a policy.
func NewToolPolicy(allowed, disallowed []string) ToolPolicy {
	return ToolPolicy{Allow: SplitToolNames(allowed), Deny: SplitToolNames(disallowed)}
}

// IsZero reports whether the policy constrains nothing.
func (p ToolPolicy) IsZero() bool { return len(p.Allow) == 0 && len(p.Deny) == 0 }

// SplitToolNames normalizes raw --allowed-tools / --disallowed-tools values into
// a flat list of tool names. Each value may itself be a comma-separated list, so
// `--allowed-tools "read,grep"` and `--allowed-tools read --allowed-tools grep`
// are equivalent. Entries are lowercased and trimmed, and empty entries dropped,
// so `"read, ,grep"` yields [read grep]. Matching is case-insensitive on purpose:
// users coming from Claude Code write `Read`/`Bash`, which must hit pigo's
// `read`/`bash`.
func SplitToolNames(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if n := normalizeToolName(part); n != "" {
				out = append(out, n)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeToolName is the single definition of how a tool name is compared:
// surrounding whitespace is insignificant and case is ignored.
func normalizeToolName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ToolPolicyError reports --allowed-tools / --disallowed-tools entries that name
// no existing tool. It is a usage error — the caller maps it to exit code 2 —
// because silently ignoring a typo is the worst outcome available: the user
// believes a boundary is in force when it is not.
type ToolPolicyError struct {
	// UnknownAllowed and UnknownDisallowed are the unrecognized names from each
	// flag. Both are reported in one error so a user with two typos fixes both in
	// one round rather than one per run.
	UnknownAllowed    []string
	UnknownDisallowed []string
	// Available is the sorted set of names that would have been accepted.
	Available []string
}

func (e *ToolPolicyError) Error() string {
	var parts []string
	if len(e.UnknownAllowed) > 0 {
		parts = append(parts, fmt.Sprintf("--allowed-tools: unknown tool %s", quoteNames(e.UnknownAllowed)))
	}
	if len(e.UnknownDisallowed) > 0 {
		parts = append(parts, fmt.Sprintf("--disallowed-tools: unknown tool %s", quoteNames(e.UnknownDisallowed)))
	}
	return fmt.Sprintf("%s (available: %s)", strings.Join(parts, "; "), strings.Join(e.Available, ", "))
}

// quoteNames renders names as a comma-separated quoted list.
func quoteNames(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(out, ", ")
}

// ValidateToolPolicy checks every allow/deny entry against the assembled tool
// set. It must run AFTER the full set exists (builtins + memory + task +
// plugins), because plugin and memory tool names are only known at runtime;
// validating right after flag parsing would reject legitimate plugin names.
//
// An empty tool set (--no-tools) skips validation entirely: there is nothing to
// constrain, and reporting every name as unknown would be noise.
func ValidateToolPolicy(tools []agentcore.AgentTool, policy ToolPolicy) error {
	if len(tools) == 0 || policy.IsZero() {
		return nil
	}
	known := toolNameSet(tools)
	unknownAllow := unknownNames(known, policy.Allow)
	unknownDeny := unknownNames(known, policy.Deny)
	if len(unknownAllow) == 0 && len(unknownDeny) == 0 {
		return nil
	}
	available := make([]string, 0, len(known))
	for n := range known {
		available = append(available, n)
	}
	sort.Strings(available)
	return &ToolPolicyError{
		UnknownAllowed:    unknownAllow,
		UnknownDisallowed: unknownDeny,
		Available:         available,
	}
}

// unknownNames returns the entries of names absent from known, preserving input
// order and dropping duplicates so a name repeated twice is reported once.
func unknownNames(known map[string]struct{}, names []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		if _, ok := known[n]; ok {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// toolNameSet indexes a tool set by normalized name.
func toolNameSet(tools []agentcore.AgentTool) map[string]struct{} {
	set := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		set[normalizeToolName(t.Name())] = struct{}{}
	}
	return set
}

// ApplyToolPolicy narrows a tool set to the allow list and then removes the deny
// list. Both empty means no restriction and the input is returned unchanged, so
// the default path is a true no-op.
//
// Deny runs after allow, which makes deny win when a name appears on both sides.
// That ordering is deliberate: the fail-closed reading of a contradictory policy
// is "do not run it".
//
// This filters the set handed to the model, so it sits at the tool-registration
// layer — strictly before the BeforeToolCall confirmation gate. A removed tool is
// never advertised and never dispatchable, which is why --approve (which only
// waives per-call confirmation) cannot widen the boundary.
func ApplyToolPolicy(tools []agentcore.AgentTool, policy ToolPolicy) []agentcore.AgentTool {
	if len(tools) == 0 || policy.IsZero() {
		return tools
	}
	allowSet := nameSet(policy.Allow)
	denySet := nameSet(policy.Deny)
	out := make([]agentcore.AgentTool, 0, len(tools))
	for _, t := range tools {
		name := normalizeToolName(t.Name())
		if len(allowSet) > 0 {
			if _, ok := allowSet[name]; !ok {
				continue
			}
		}
		if _, denied := denySet[name]; denied {
			continue
		}
		out = append(out, t)
	}
	return out
}

// nameSet indexes already-normalized names for lookup.
func nameSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

// ChildToolSet builds the tool set for a task sub-agent: the builtins with
// "task" removed (the nesting guard capping delegation depth at one), narrowed by
// the parent's policy.
//
// Inheriting the policy is load-bearing, not a nicety. A child that ignored it
// would be a one-line escape from the boundary: under --disallowed-tools bash the
// model could dispatch a sub-agent and run bash there instead. Any future spawn
// path must route through here for the same reason.
func ChildToolSet(cwd string, policy ToolPolicy) []agentcore.AgentTool {
	return ApplyToolPolicy(BuiltinToolsExcept(cwd, false, "task"), policy)
}
