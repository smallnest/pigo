// This file implements hook matching: given an event type and (for tool
// events) a tool name, MatchHooks returns the flat, ordered list of hooks that
// should run. Matcher semantics follow Claude Code to keep the learning curve
// low: empty or "*" matches all, an exact tool name matches only that tool, a
// "|"-separated list matches any listed tool, and anything else is compiled as
// a Go regexp against the tool name.
package hooks

import (
	"io"
	"regexp"
	"strings"
)

// MatchHooks returns every valid hook under eventType whose matcher matches
// toolName, preserving config order (layer order + declaration order within a
// layer). For events that do not carry a tool name (toolName == ""), matchers
// are ignored and all hooks under the event fire. Invalid hooks (see
// HookConfig.Validate) are skipped; a matcher whose regexp fails to compile is
// skipped with a warning on warnLog (when non-nil).
func (s HookSet) MatchHooks(eventType, toolName string, warnLog io.Writer) []HookConfig {
	matchers := s[eventType]
	if len(matchers) == 0 {
		return nil
	}
	var out []HookConfig
	for _, m := range matchers {
		if !matcherApplies(m.Matcher, toolName, warnLog) {
			continue
		}
		for _, h := range m.Hooks {
			if err := h.Validate(); err != nil {
				warnf(warnLog, "pigo: hooks: skipping invalid hook: %v\n", err)
				continue
			}
			out = append(out, h)
		}
	}
	return out
}

// matcherApplies reports whether a matcher pattern matches the given tool name.
// An empty tool name (event without a tool) always matches, so tool-agnostic
// events fire every hook regardless of matcher.
func matcherApplies(pattern, toolName string, warnLog io.Writer) bool {
	if toolName == "" {
		return true
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	// "|"-separated multi-value: any exact tool name matches. This also covers
	// the single-exact-name case (no "|").
	parts := strings.Split(pattern, "|")
	exactCandidate := true
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == toolName {
			return true
		}
		if !isPlainToolName(p) {
			exactCandidate = false
		}
	}
	// If every alternative was a plain tool name, this was an exact/multi-value
	// matcher that simply did not match — do not fall through to regexp.
	if exactCandidate {
		return false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		warnf(warnLog, "pigo: hooks: skipping matcher with invalid regexp %q: %v\n", pattern, err)
		return false
	}
	return re.MatchString(toolName)
}

// isPlainToolName reports whether s looks like a literal tool name rather than
// a regexp — i.e. it contains no regexp metacharacters. Used to decide whether
// a "|"-split alternative should be treated as an exact match or as part of a
// regexp alternation.
func isPlainToolName(s string) bool {
	return !strings.ContainsAny(s, ".*+?()[]{}^$\\")
}
