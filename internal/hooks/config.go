// Package hooks implements pigo's user-extensible lifecycle hook system: a
// config-driven way to run shell commands at agent lifecycle points (tool
// calls, prompt submission, session start/end, etc.) without writing Go or
// compiling a plugin. It is a leaf package depending only on the standard
// library, so it can be composed into the runtime/cli layers without creating
// an import cycle.
//
// This file defines the configuration types and their validation. A hook is a
// single shell command; a matcher binds a group of hooks to an event (and,
// for tool events, a tool-name pattern); a HookSet maps each event type to its
// matchers. The types carry JSON tags so they can live directly in the layered
// config.json.
package hooks

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// DefaultTimeoutSeconds is the per-hook execution timeout when HookConfig.Timeout
// is nil (FR-11). A slow or hung hook is killed after this many seconds and
// treated as a failure (fail-open for blocking hooks).
const DefaultTimeoutSeconds = 60

// CommandType is the only hook type supported in v1: a shell command. Other
// types (embedded script engines, WASM) are explicitly out of scope.
const CommandType = "command"

// HookConfig is a single hook command.
type HookConfig struct {
	Type    string `json:"type"`              // v1: fixed "command"
	Command string `json:"command"`           // handed to the system shell
	Timeout *int   `json:"timeout,omitempty"` // seconds; nil = DefaultTimeoutSeconds
}

// HookMatcherConfig binds a group of hooks to a matcher. An empty (or "*")
// matcher applies to every trigger of the event; otherwise it is matched
// against the tool name (see matcher.go).
type HookMatcherConfig struct {
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []HookConfig `json:"hooks"`
}

// HookSet maps an event type (e.g. "PreToolUse") to its matcher list. It is
// the shape stored in a ConfigLayer and in the resolved Config.
type HookSet map[string][]HookMatcherConfig

// TimeoutSeconds returns the effective timeout for the hook: its own Timeout
// when set to a positive value, otherwise DefaultTimeoutSeconds. A non-positive
// override is ignored so a misconfigured 0/negative value cannot disable the
// timeout guard.
func (h HookConfig) TimeoutSeconds() int {
	if h.Timeout != nil && *h.Timeout > 0 {
		return *h.Timeout
	}
	return DefaultTimeoutSeconds
}

// Validate reports whether the hook is well-formed: the type must be "command"
// (empty is accepted and treated as "command" for convenience) and the command
// must be non-empty. An invalid hook is rejected at load time and skipped with
// a warning rather than executed.
func (h HookConfig) Validate() error {
	if h.Type != "" && h.Type != CommandType {
		return errors.New("hook type must be \"command\"")
	}
	if strings.TrimSpace(h.Command) == "" {
		return errors.New("hook command must not be empty")
	}
	return nil
}

// warnf writes a formatted warning to w when w is non-nil. Hook failures and
// misconfigurations are surfaced this way (mirroring plugin.EventNotifier's
// warnLog) so a bad hook never interrupts the agent.
func warnf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...)
}
