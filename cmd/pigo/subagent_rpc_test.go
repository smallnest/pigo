package main

// Tests for the sub-agent RPC subprocess mode (US-019, #135): the pure
// filterBuiltinTools helper and the runSubAgentRPC validation branches
// (method-not-found, invalid params, parse error) that return RPC errors before
// any provider is resolved. The happy-path transport is covered in
// internal/runtime via a compiled helper binary; RunSubAgentOnce (the agent
// core) is covered there with a faux provider.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/jsonrpc"
	"github.com/smallnest/pigo/internal/runtime"
)

// TestFilterBuiltinTools verifies the subprocess tool filter: an empty name
// list keeps all builtins, a subset keeps only the named tools, and unknown
// names are silently ignored.
func TestFilterBuiltinTools(t *testing.T) {
	all := builtinTools(t.TempDir(), false)
	if len(all) == 0 {
		t.Fatal("builtinTools returned no tools")
	}
	namesOf := func(ts []agentcore.AgentTool) []string {
		out := make([]string, len(ts))
		for i, tl := range ts {
			out[i] = tl.Name()
		}
		return out
	}

	if got := filterBuiltinTools(all, nil); len(got) != len(all) {
		t.Errorf("nil names kept %d, want all %d", len(got), len(all))
	}
	if got := filterBuiltinTools(all, []string{}); len(got) != len(all) {
		t.Errorf("empty names kept %d, want all %d", len(got), len(all))
	}

	got := filterBuiltinTools(all, []string{"read", "grep"})
	if len(got) != 2 {
		t.Fatalf("subset kept %d, want 2: %v", len(got), namesOf(got))
	}
	gotNames := namesOf(got)
	if gotNames[0] != "read" || gotNames[1] != "grep" {
		t.Errorf("subset names = %v, want [read grep]", gotNames)
	}

	// Unknown names are ignored, known ones kept.
	got = filterBuiltinTools(all, []string{"read", "does-not-exist"})
	if len(got) != 1 || got[0].Name() != "read" {
		t.Errorf("unknown-name filter kept %v, want [read]", namesOf(got))
	}
}

// TestRunSubAgentRPCValidation verifies the subprocess returns JSON-RPC errors
// for malformed requests without reaching provider resolution: a parse error,
// an unknown method, and missing prompt/model params. These branches are the
// server's contract for bad input and need not involve a provider.
func TestRunSubAgentRPCValidation(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantCode int
		wantMsg  string
	}{
		{"parse error", "not-json", -32700, "parse error"},
		{"method not found", `{"jsonrpc":"2.0","id":1,"method":"other","params":{}}`, -32601, "method not found"},
		{"missing prompt", `{"jsonrpc":"2.0","id":2,"method":"` + runtime.SubAgentRPCMethod + `","params":{"model":"x"}}`, -32602, "prompt and model are required"},
		{"missing model", `{"jsonrpc":"2.0","id":3,"method":"` + runtime.SubAgentRPCMethod + `","params":{"prompt":"x"}}`, -32602, "prompt and model are required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := runSubAgentRPC(context.Background(), strings.NewReader(c.line+"\n"), &out, &errOut)
			if code != 0 {
				t.Errorf("exit code = %d, want 0 (validation errors are RPC responses, not non-zero exits)", code)
			}
			line, _ := out.ReadString('\n')
			if line == "" {
				t.Fatal("no response written")
			}
			var resp jsonrpc.Response
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				t.Fatalf("unmarshal response %q: %v", line, err)
			}
			if resp.Error == nil {
				t.Fatalf("response has no error: %s", line)
			}
			if resp.Error.Code != c.wantCode {
				t.Errorf("error code = %d, want %d (msg=%q)", resp.Error.Code, c.wantCode, resp.Error.Message)
			}
			if !strings.Contains(resp.Error.Message, c.wantMsg) {
				t.Errorf("error msg = %q, want it to contain %q", resp.Error.Message, c.wantMsg)
			}
		})
	}
}

// TestRunSubAgentRPCScannerError verifies a stdin read error (a line exceeding
// the scanner cap) is surfaced on stderr and yields a non-zero exit, rather
// than a silent clean exit.
func TestRunSubAgentRPCScannerError(t *testing.T) {
	// A line longer than the 16 MiB scanner cap triggers a scanner error.
	huge := strings.Repeat("a", 17*1024*1024)
	var out, errOut bytes.Buffer
	code := runSubAgentRPC(context.Background(), strings.NewReader(huge), &out, &errOut)
	if code == 0 {
		t.Error("exit code = 0 on scanner error, want non-zero")
	}
	if !strings.Contains(errOut.String(), "subagent-rpc stdin") {
		t.Errorf("stderr = %q, want it to mention 'subagent-rpc stdin'", errOut.String())
	}
}
