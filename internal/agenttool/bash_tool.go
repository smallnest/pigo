// This file implements the bash tool (US-018): run a shell command, streaming
// stdout/stderr back as tool_execution_update partials, honoring a timeout and
// context cancellation (which kills the child process group). A non-zero exit
// is surfaced as an error (isError) whose message carries the captured output.
package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/smallnest/pigo/internal/agentcore"
)

// bashDefaultTimeout bounds a command that does not specify one.
const bashDefaultTimeout = 2 * time.Minute

// bashMaxTimeout caps any requested timeout.
const bashMaxTimeout = 10 * time.Minute

// bashMaxOutputBytes caps how many bytes of combined stdout/stderr the bash tool
// returns to the model. A single command can emit megabytes (build logs, a big
// cat), which — unlike the timeout cap — would otherwise flow into context whole
// and blow the window. Output past this size is truncated to a head + tail
// preview (see truncateBashOutput), mirroring search's searchMaxResults/"[truncated
// …]" convention. This is the tool's own inner cap; a later executor-layer budget
// may impose a stricter outer limit.
const bashMaxOutputBytes = 30_000

// truncateBashOutput caps s at bashMaxOutputBytes. When s is longer it keeps a
// head and a tail preview (split evenly) joined by a "[truncated N bytes]" marker,
// so both the start and the end of the output survive. Cut points are pulled back
// to UTF-8 rune boundaries so no partial rune is emitted; N counts the raw bytes
// dropped from the middle.
func truncateBashOutput(s string) string {
	if len(s) <= bashMaxOutputBytes {
		return s
	}
	half := bashMaxOutputBytes / 2
	head := trimUTF8Prefix(s[:half])
	tail := trimUTF8Suffix(s[len(s)-half:])
	removed := len(s) - len(head) - len(tail)
	return head + fmt.Sprintf("\n[truncated %d bytes]\n", removed) + tail
}

// trimUTF8Prefix drops trailing bytes of s that form an incomplete rune, so the
// returned prefix ends on a rune boundary.
func trimUTF8Prefix(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// trimUTF8Suffix drops leading bytes of s that form an incomplete rune, so the
// returned suffix starts on a rune boundary.
func trimUTF8Suffix(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[1:]
			continue
		}
		break
	}
	return s
}

// BashTool runs shell commands. Dir bounds the working directory (empty = the
// process CWD). Shell selects the interpreter (empty = "bash -c").
type BashTool struct {
	// Dir is the working directory for commands. Empty uses the process CWD.
	Dir string
	// Shell is the interpreter path. Empty defaults to "bash".
	Shell string
}

// bashToolArgs is the decoded argument shape for BashTool.
type bashToolArgs struct {
	// Command is the shell command line to run.
	Command string `json:"command"`
	// TimeoutMs optionally overrides the default timeout (milliseconds).
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

// Name implements AgentTool.
func (t *BashTool) Name() string { return "bash" }

// Description implements AgentTool.
func (t *BashTool) Description() string {
	return "Run a shell command, streaming stdout/stderr. Supports a timeout " +
		"and cancellation. A non-zero exit code is reported as an error."
}

// Schema implements AgentTool.
func (t *BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command":    {"type": "string", "description": "Shell command line to run."},
    "timeout_ms": {"type": "integer", "description": "Timeout in milliseconds (capped at 10 minutes).", "minimum": 0}
  },
  "required": ["command"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Commands can have side effects → sequential.
func (t *BashTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *BashTool) shell() string {
	if t.Shell != "" {
		return t.Shell
	}
	return "bash"
}

// streamWriter forwards each written chunk to onUpdate as a growing partial
// result while accumulating the full output. It is safe for concurrent use so
// stdout and stderr can share the same combined buffer.
type streamWriter struct {
	mu       *sync.Mutex
	buf      *bytes.Buffer
	onUpdate agentcore.ToolUpdateFunc
}

func (w streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf.Write(p)
	snapshot := w.buf.String()
	w.mu.Unlock()
	if w.onUpdate != nil {
		w.onUpdate(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(snapshot)}})
	}
	return len(p), nil
}

// Execute implements AgentTool. It streams combined stdout/stderr via onUpdate,
// enforces a timeout, and kills the process on context cancellation. A non-zero
// exit returns a Go error (→ isError) carrying the exit code and output.
func (t *BashTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[bashToolArgs](args, "bash")
	if bad != nil {
		return *bad, nil
	}
	if a.Command == "" {
		return errorResult("bash: command is required"), nil
	}

	timeout := bashDefaultTimeout
	if a.TimeoutMs > 0 {
		timeout = time.Duration(a.TimeoutMs) * time.Millisecond
	}
	if timeout > bashMaxTimeout {
		timeout = bashMaxTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, t.shell(), "-c", a.Command)
	if t.Dir != "" {
		cmd.Dir = t.Dir
	}

	var mu sync.Mutex
	var combined bytes.Buffer
	sw := streamWriter{mu: &mu, buf: &combined, onUpdate: onUpdate}
	cmd.Stdout = sw
	cmd.Stderr = sw

	err := cmd.Run()

	mu.Lock()
	output := combined.String()
	mu.Unlock()

	// Cap the output before it enters any ToolResult / error message, so a single
	// command's huge output cannot blow the model's context. Truncation keeps a
	// head + tail preview with a "[truncated N bytes]" marker in the middle.
	output = truncateBashOutput(output)

	// Context cancellation / timeout takes precedence in the message.
	if runCtx.Err() == context.DeadlineExceeded {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(output)}},
			fmt.Errorf("bash: command timed out after %s\n%s", timeout, output)
	}
	if ctx.Err() == context.Canceled {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(output)}},
			fmt.Errorf("bash: command canceled\n%s", output)
	}

	if err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
		return agentcore.AgentToolResult{
				Content: agentcore.ContentList{agentcore.NewTextContent(output)},
				Details: map[string]any{"exitCode": exitCode},
			},
			fmt.Errorf("bash: command exited with code %d\n%s", exitCode, output)
	}

	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(output)},
		Details: map[string]any{"exitCode": 0},
	}, nil
}
