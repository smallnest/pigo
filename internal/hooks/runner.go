// This file implements Runner.Run: executing a single hook command via the
// system shell with the event payload on stdin, a per-hook timeout, bounded
// output capture, and exit-code classification. It is the one place that forks
// a process, so all isolation guarantees (timeout kill, output cap, error
// containment) live here.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// MaxOutputBytes caps the bytes captured from a hook's stdout and stderr each
// (FR-13). Output beyond this is dropped and the truncation is flagged; the
// captured prefix is still parsed so a hook that prints a small JSON decision
// followed by noise still works.
const MaxOutputBytes = 1 << 20 // 1 MB

// blockExitCode is the exit code that signals a block (Claude Code semantics):
// the command exited 2, stderr carries the reason.
const blockExitCode = 2

// Runner executes hook commands. Shell defaults to "sh" with a "-c" flag; it is
// a field so tests can substitute a shell and future platforms can override it.
// ProjectDir is the working directory hook commands run in (the project root).
// ExtraEnv is appended to the process environment (PIGO_* variables).
type Runner struct {
	Shell      string
	ProjectDir string
	WarnLog    io.Writer
}

// Run executes one hook, writing input as a single-line JSON document to the
// command's stdin. It returns the parsed HookOutput and a non-nil error only
// for an execution *failure* (could not start, timed out, or exited non-zero
// and non-2). A clean exit 0 or a block (exit 2) both return err == nil; the
// caller distinguishes a block via HookOutput.blocks(). The PIGO_* environment
// variables are injected on top of the current process environment.
func (r *Runner) Run(ctx context.Context, h HookConfig, input HookInput) (HookOutput, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return HookOutput{}, fmt.Errorf("marshal hook input: %w", err)
	}

	timeout := time.Duration(h.TimeoutSeconds()) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := r.Shell
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.CommandContext(runCtx, shell, "-c", h.Command)
	cmd.Dir = r.ProjectDir
	cmd.Env = r.env(input)
	cmd.Stdin = bytes.NewReader(payload)

	var stdout, stderr cappedBuffer
	stdout.limit = MaxOutputBytes
	stderr.limit = MaxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	if stdout.truncated() || stderr.truncated() {
		warnf(r.WarnLog, "pigo: hooks: output from command %q exceeded %d bytes and was truncated\n", h.Command, MaxOutputBytes)
	}

	// Timeout: the context deadline fired and the process was killed.
	if runCtx.Err() == context.DeadlineExceeded {
		return HookOutput{}, fmt.Errorf("hook timed out after %s", timeout)
	}

	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			// Could not start (ENOENT etc.) or was killed.
			return HookOutput{}, fmt.Errorf("hook failed to run: %w", runErr)
		}
	}

	switch exitCode {
	case 0:
		out, _ := parseHookOutput(stdout.Bytes())
		return out, nil
	case blockExitCode:
		// Block: prefer a JSON decision if present, else synthesize one from
		// stderr as the reason.
		if out, ok := parseHookOutput(stdout.Bytes()); ok {
			if out.Reason == "" {
				out.Reason = strings.TrimSpace(string(stderr.Bytes()))
			}
			out.Decision = "block"
			return out, nil
		}
		return HookOutput{Decision: "block", Reason: strings.TrimSpace(string(stderr.Bytes()))}, nil
	default:
		return HookOutput{}, fmt.Errorf("hook exited with code %d: %s", exitCode, strings.TrimSpace(string(stderr.Bytes())))
	}
}

// env builds the command environment: the current process environment plus the
// PIGO_* variables derived from the input.
func (r *Runner) env(input HookInput) []string {
	env := append([]string(nil), os.Environ()...)
	env = append(env,
		"PIGO_SESSION_ID="+input.SessionID,
		"PIGO_PROJECT_DIR="+r.ProjectDir,
		"PIGO_EVENT_TYPE="+input.EventType,
	)
	return env
}

// cappedBuffer is an io.Writer that stores at most limit bytes and counts how
// many it dropped, so hook output cannot exhaust memory (FR-13).
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.limit <= 0 {
		return c.buf.Write(p)
	}
	room := c.limit - c.buf.Len()
	if room <= 0 {
		c.dropped += len(p)
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.dropped += len(p) - room
		return len(p), nil
	}
	return c.buf.Write(p)
}

// Bytes returns the captured (possibly truncated) output.
func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }

// truncated reports whether any output was dropped by the cap.
func (c *cappedBuffer) truncated() bool { return c.dropped > 0 }
