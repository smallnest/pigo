// This file holds the interactive pieces of project trust (US-018, #134). The
// trust store itself lives in internal/trust; here we have the REPL-only parts:
//
//   - the first-run trust dialog (ensureTrustPrompt), shown when the cwd has no
//     saved decision;
//   - the /trust command (registerTrustCommand), which saves or reports the
//     current project's decision;
//   - the BeforeToolCall hook (trustBeforeToolCall) that asks before side-effect
//     tools (bash/write/edit) run in an untrusted directory.
//
// All prompts share the REPL's single *bufio.Reader so input typed ahead is
// never split between the main loop and a confirmation. Headless mode is
// unaffected: trust is a REPL safety feature and headless is an explicit,
// non-interactive invocation.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/trust"
)

// sideEffectTools are the built-in tools with filesystem or process side
// effects that trust gates. Read-only tools (read/grep/find), in-memory tools
// (todo), and network-read tools (webfetch) are never gated.
var sideEffectTools = map[string]bool{
	"bash":  true,
	"write": true,
	"edit":  true,
}

// ensureTrustPrompt runs the first-run trust dialog when cwd has no saved
// decision (NearestTrustDecision reports Found=false). When a decision already
// exists (trusted/untrusted/null) it is a no-op: the user already answered, so
// pigo does not re-ask on every launch. mgr==nil disables trust entirely.
func ensureTrustPrompt(out io.Writer, in *bufio.Reader, mgr *trust.Manager, cwd string) {
	if mgr == nil {
		return
	}
	if res := mgr.NearestTrustDecision(cwd); res.Found {
		return
	}
	fmt.Fprintf(out, "\nFirst time in this directory: %s\n", cwd)
	fmt.Fprintln(out, "pigo runs side-effect tools (bash, write, edit) here. Choose a trust level:")
	fmt.Fprintln(out, "  1) Trust     - remember as trusted (tools run without asking)")
	fmt.Fprintln(out, "  2) Just once - trust only for this session (default)")
	fmt.Fprintln(out, "  3) Reject    - do not trust (tools ask each time)")

	// Default to "just once" (2): it keeps the REPL usable without persisting
	// a trust grant the user did not explicitly confirm.
	choice := readMenuChoice(out, in, "Enter choice [1-3]: ", 3, 2)
	switch choice {
	case 1:
		target := chooseScope(out, in, cwd)
		if err := mgr.SetDecision(target, trust.Trusted); err != nil {
			fmt.Fprintf(out, "pigo: could not save trust decision: %v\n", err)
			return
		}
		fmt.Fprintf(out, "Trusted %s (saved to %s).\n", cwd, target)
	case 3:
		target := chooseScope(out, in, cwd)
		if err := mgr.SetDecision(target, trust.Untrusted); err != nil {
			fmt.Fprintf(out, "pigo: could not save trust decision: %v\n", err)
			return
		}
		fmt.Fprintf(out, "Marked %s untrusted (saved to %s). Side-effect tools will ask.\n", cwd, target)
	default: // 2 = just once
		mgr.SetSessionTrust(cwd)
		fmt.Fprintf(out, "Trusted %s for this session only (not saved).\n", cwd)
	}
}

// chooseScope asks whether to save the decision for the current directory or
// its parent, returning the chosen path. On empty/EOF it defaults to the
// current directory.
func chooseScope(out io.Writer, in *bufio.Reader, cwd string) string {
	parent := filepath.Dir(filepath.Clean(cwd))
	if parent == filepath.Clean(cwd) {
		// cwd is the filesystem root: there is no parent, so do not offer one.
		return cwd
	}
	fmt.Fprintln(out, "Save for:")
	fmt.Fprintf(out, "  1) This directory (%s)\n", cwd)
	fmt.Fprintf(out, "  2) Parent directory (%s)\n", parent)
	if readMenuChoice(out, in, "Enter [1-2]: ", 2, 1) == 2 {
		return parent
	}
	return cwd
}

// readMenuChoice prompts and reads a 1..max integer, re-prompting on invalid
// input. An empty line or EOF returns def so the dialog never deadlocks on
// missing input.
func readMenuChoice(out io.Writer, in *bufio.Reader, prompt string, max, def int) int {
	for {
		fmt.Fprint(out, prompt)
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return def
		}
		s := strings.TrimSpace(line)
		if s == "" {
			return def
		}
		if n, parseErr := strconv.Atoi(s); parseErr == nil && n >= 1 && n <= max {
			return n
		}
		fmt.Fprintf(out, "  (enter a number 1-%d)\n", max)
		if err != nil {
			return def
		}
	}
}

// registerTrustCommand installs the /trust action command, which saves or
// reports the current project's trust decision. It is an instance built-in
// (AddBuiltin) because its closure captures the trust manager and cwd - state
// out of reach of an init()-time global registration. mgr==nil is a no-op: the
// command is not installed, so /trust reports unknown when trust is disabled.
func registerTrustCommand(reg *runtime.SlashRegistry, mgr *trust.Manager, cwd string) {
	if mgr == nil {
		return
	}
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "trust",
		Description: "view or set this project's trust: /trust [on|off|once|status]",
		Action: func(args string) string {
			switch strings.TrimSpace(strings.ToLower(args)) {
			case "", "on":
				if err := mgr.SetDecision(cwd, trust.Trusted); err != nil {
					return fmt.Sprintf("pigo: could not save trust: %v", err)
				}
				return fmt.Sprintf("trusted %s (saved)", cwd)
			case "off":
				// Clear any active session grant first: IsTrusted checks
				// session before the persisted decision, so without this an
				// "always" granted earlier in the session would keep the dir
				// trusted until restart and the message below would be a lie.
				mgr.ClearSessionTrust(cwd)
				if err := mgr.SetDecision(cwd, trust.Untrusted); err != nil {
					return fmt.Sprintf("pigo: could not save trust: %v", err)
				}
				return fmt.Sprintf("marked %s untrusted (saved); side-effect tools will ask", cwd)
			case "once":
				mgr.SetSessionTrust(cwd)
				return fmt.Sprintf("trusted %s for this session only (not saved)", cwd)
			case "status":
				res := mgr.NearestTrustDecision(cwd)
				if !res.Found {
					return fmt.Sprintf("%s: undecided (no saved decision)", cwd)
				}
				return fmt.Sprintf("%s: %s (decision saved for %s)", cwd, res.Decision, res.Path)
			default:
				return "usage: /trust [on|off|once|status]  (default: on)"
			}
		},
	})
}

// trustBeforeToolCall builds the permission hook that gates side-effect tools
// (bash/write/edit) on the cwd's trust decision. In a trusted directory the
// call is allowed (nil). Otherwise the user is prompted; "always" grants
// session trust so subsequent side-effect calls skip the prompt. mgr==nil
// returns nil (trust disabled, no gating).
//
// Concurrency: bash/write/edit are all ToolExecutionSequential, so the batch
// runs serially and this hook fires on the run-loop producer goroutine - never
// concurrently with itself. mu serializes prompts anyway as cheap insurance
// should a side-effect tool ever become parallel; it is nil-safe (callers that
// wire trust without a mutex simply get no serialization). The prompt writes to
// out from the producer goroutine; this is safe because streamRun uses an
// unbuffered event stream (EventBuffer=0, the default), so by the time the hook
// runs the main goroutine has already drained the assistant's streamed text and
// is idle in DrainStream. Do not raise EventBuffer above 0 while trust gating
// is wired without routing the prompt through the main goroutine.
//
// SIGINT caveat: a Ctrl+C that arrives while the prompt is blocked reading
// stdin cancels the run context but does NOT unblock the read, so the
// interrupt takes effect only after the user answers the prompt. This is safe -
// a post-cancel "yes" still aborts: executeToolCall's emit of
// ToolExecutionStartEvent returns ctx.Err() and the tool never runs. A fix that
// unblocks the read on signal would require injecting input on SIGINT, which is
// out of scope for this change.
func trustBeforeToolCall(mgr *trust.Manager, cwd string, in *bufio.Reader, out io.Writer, mu *sync.Mutex) agentcore.BeforeToolCallFunc {
	if mgr == nil {
		return nil
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if !sideEffectTools[call.Name] {
			return nil
		}
		if mu != nil {
			mu.Lock()
			defer mu.Unlock()
		}
		// Check under the lock: a prior call's "always" in the same batch may
		// have granted session trust, in which case this call skips the prompt.
		// (Today batches with side-effect tools run serially, so this is
		// belt-and-suspenders.)
		if mgr.IsTrusted(cwd) {
			return nil
		}
		allow, always := confirmToolCall(out, in, call)
		if always {
			mgr.SetSessionTrust(cwd)
		}
		if !allow {
			msg := fmt.Sprintf("tool %q blocked: %s is not trusted (use /trust to trust this project)", call.Name, cwd)
			return &agentcore.BeforeToolCallDecision{
				Block:   true,
				Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
			}
		}
		return nil
	}
}

// confirmToolCall asks whether a side-effect tool call may run in an untrusted
// directory. It returns (allow, always): allow runs the call this once; always
// runs it AND grants session trust so subsequent side-effect calls skip the
// prompt. Denial (no/empty/EOF) returns (false, false).
func confirmToolCall(out io.Writer, in *bufio.Reader, call agentcore.AgentToolCall) (allow bool, always bool) {
	fmt.Fprintf(out, "\npigo wants to run %q in an untrusted directory.\n", call.Name)
	if summary := toolCallSummary(call); summary != "" {
		fmt.Fprintf(out, "  %s\n", summary)
	}
	fmt.Fprint(out, "Allow? [y]es / [n]o / [a]lways (trust for this session) [y/N/a]: ")
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, false
	case "a", "always":
		return true, true
	default:
		return false, false
	}
}

// toolCallSummary renders a one-line preview of what a side-effect tool will
// do, so the user can make an informed allow/deny choice. It best-effort
// extracts the bash command or the write/edit path from the arguments; if the
// arguments do not parse it falls back to a truncated raw view.
func toolCallSummary(call agentcore.AgentToolCall) string {
	raw := strings.TrimSpace(string(call.Arguments))
	if raw == "" || raw == "{}" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return truncateForPrompt(raw)
	}
	switch call.Name {
	case "bash":
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			return "command: " + truncateForPrompt(cmd)
		}
	case "write", "edit":
		if p, ok := args["path"].(string); ok && p != "" {
			return "path: " + truncateForPrompt(p)
		}
	}
	return truncateForPrompt(raw)
}

// truncateForPrompt caps a string at maxPromptPreview runes so a confirmation
// prompt stays readable even for large write/edit payloads.
func truncateForPrompt(s string) string {
	const maxPromptPreview = 200
	r := []rune(s)
	if len(r) <= maxPromptPreview {
		return s
	}
	return string(r[:maxPromptPreview]) + " …"
}
