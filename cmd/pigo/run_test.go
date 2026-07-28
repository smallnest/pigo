package main

// Tests for the run-assembly seam (architecture deepening ②): dispatch is now a
// pure function of (options, writers) → exit code, so the CLI's branching —
// output-mode parsing, the "no prompt on a pipe" guard — can be exercised
// without spawning a provider or re-parsing the global flag set.

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestDispatchListSessionsEmpty verifies --list-sessions is a standalone action
// that succeeds (exit 0) and prints the empty-store message, using an isolated
// PIGO_HOME so it never touches the real session store.
func TestDispatchListSessionsEmpty(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	code := dispatch(context.Background(), cliOptions{listSessions: true}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (errOut=%q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "no sessions") {
		t.Errorf("out = %q, want the empty-store message", out.String())
	}
}

// TestDispatchContinueNoSessions verifies --continue with an empty store is an
// error (exit 1) that says there is nothing to continue, rather than starting a
// blank REPL.
func TestDispatchContinueNoSessions(t *testing.T) {
	t.Setenv("PIGO_HOME", t.TempDir())
	// Ensure a non-terminal path is not taken before the continue guard: continue
	// resolves the id first and errors when the store is empty.
	var out, errOut bytes.Buffer
	code := dispatch(context.Background(), cliOptions{continueLast: true}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (errOut=%q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no sessions to continue") {
		t.Errorf("errOut = %q, want the no-sessions-to-continue message", errOut.String())
	}
}

// TestDispatchNoPromptNonTerminal verifies the CI/pipe guard: no prompt, no
// resume, and a non-terminal stdout is a usage error (exit 2) with a diagnostic
// on errOut — reachable now that dispatch takes its writers as parameters.
func TestDispatchNoPromptNonTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(context.Background(), cliOptions{}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "no prompt") {
		t.Errorf("errOut = %q, want it to mention the missing prompt", errOut.String())
	}
}

// TestDispatchBadOutputFormat verifies an unknown --output-format is rejected
// (exit 2) before any provider work, naming the offending value.
func TestDispatchBadOutputFormat(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(context.Background(), cliOptions{prompt: "hi", outputFmt: "yaml"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "yaml") {
		t.Errorf("errOut = %q, want it to name the bad format", errOut.String())
	}
}
