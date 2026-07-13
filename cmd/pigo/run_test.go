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

	"github.com/smallnest/pigo/internal/runtime"
)

// TestParseOutputMode covers the three accepted spellings and one rejection,
// pinning the flag contract the headless driver depends on.
func TestParseOutputMode(t *testing.T) {
	cases := []struct {
		in      string
		want    runtime.HeadlessMode
		wantErr bool
	}{
		{"text", runtime.PrintMode, false},
		{"", runtime.PrintMode, false},
		{"stream-json", runtime.StreamJSONMode, false},
		{"yaml", 0, true},
	}
	for _, c := range cases {
		got, err := parseOutputMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseOutputMode(%q): want error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseOutputMode(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseOutputMode(%q) = %v, want %v", c.in, got, c.want)
		}
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
