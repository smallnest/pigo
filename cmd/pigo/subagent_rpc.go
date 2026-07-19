// This file implements the subprocess side of process-isolated sub-agents
// (US-019, #135). Invoked as `pigo --subagent-rpc`, pigo speaks JSON-RPC 2.0
// over stdio: for each "subagent/run" request on stdin it runs a child agent
// loop and writes the result (or an error) to stdout, exiting when stdin
// closes. The parent (SubAgentTool in process mode, internal/runtime) drives it
// via internal/jsonrpc.
//
// It reuses internal/jsonrpc's message types (Request/Response/ID/Version) for
// (de)serialization so the wire format matches the client exactly; the agent
// execution itself is runtime.RunSubAgentOnce.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/jsonrpc"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
)

// runSubAgentRPC is the `pigo --subagent-rpc` entry point. It reads
// newline-delimited JSON-RPC requests from in, runs each sub-agent request, and
// writes one response per request to out. It returns 0 (success) when stdin
// closes; a per-request failure is an RPC error response, not a non-zero exit,
// so the parent can distinguish "the child answered with an error" from "the
// child crashed" (the latter is detected by the parent's transport when stdout
// closes without a response).
func runSubAgentRPC(ctx context.Context, in io.Reader, out, errOut io.Writer) int {
	scanner := bufio.NewScanner(in)
	// A sub-agent prompt can be large; match the jsonrpc client's line cap.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req jsonrpc.Request
		if err := json.Unmarshal(line, &req); err != nil {
			// A parse error carries no id, so the response id is null. The
			// jsonrpc client drops responses with a null id (it cannot correlate
			// them), so this is only observable on the child's stderr; in
			// practice the parent always sends well-formed requests.
			writeSubAgentError(enc, nil, -32700, "parse error: "+err.Error())
			continue
		}
		handleSubAgentRequest(ctx, enc, &req)
	}
	// A scanner error (e.g. a request line exceeding the 16 MiB cap) ends the
	// stream abnormally: surface it on stderr and exit non-zero so the parent's
	// transport sees a diagnostic rather than a silent clean exit.
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(errOut, "pigo: subagent-rpc stdin: %v\n", err)
		return 1
	}
	return 0
}

// handleSubAgentRequest dispatches one JSON-RPC request to the sub-agent runner
// and writes the response. Unknown methods, bad params, provider-resolution
// failures, and failed child runs are all RPC errors so the parent surfaces them
// as tool errors; only a successful run yields a result with the child's text.
func handleSubAgentRequest(ctx context.Context, enc *json.Encoder, req *jsonrpc.Request) {
	if req.Method != runtime.SubAgentRPCMethod {
		writeSubAgentError(enc, req.ID, -32601, "method not found: "+req.Method)
		return
	}
	var params runtime.SubAgentRunParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeSubAgentError(enc, req.ID, -32602, "invalid params: "+err.Error())
			return
		}
	}
	if params.Prompt == "" || params.Model == "" {
		writeSubAgentError(enc, req.ID, -32602, "invalid params: prompt and model are required")
		return
	}
	// Resolve the provider the same way the CLI does, so the subprocess targets
	// the same gateway the parent's NewRunConfig encoded. Credentials come from
	// the inherited environment (the parent's env vars).
	prov, providerName, err := resolveProvider(params.Model, params.BaseURL, params.Protocol)
	if err != nil {
		writeSubAgentError(enc, req.ID, -32603, "resolve provider: "+err.Error())
		return
	}
	cwd, _ := os.Getwd()
	tools := filterBuiltinTools(builtinTools(cwd, false), params.Tools)
	reg := toolRegistry(tools)
	creds := provider.NewCredentialStore(nil) // env-resolved
	runCfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:     params.Model,
			Provider:  providerName,
			Stream:    provider.StreamFnFromProvider(prov),
			GetAPIKey: creds.GetAPIKey,
		},
		Batch: agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
	}
	text, err := runtime.RunSubAgentOnce(ctx, params.SystemPrompt, params.Prompt, tools, runCfg)
	if err != nil {
		// A failed child run is an RPC error so the parent's defaultProcessCall
		// returns a Go error and executeProcess marks the tool result IsError,
		// matching goroutine mode's "failed run -> tool error" behavior.
		writeSubAgentError(enc, req.ID, -32000, err.Error())
		return
	}
	result, _ := json.Marshal(runtime.SubAgentRunResult{Text: text})
	_ = enc.Encode(jsonrpc.Response{JSONRPC: jsonrpc.Version, ID: req.ID, Result: result})
}

// writeSubAgentError writes a JSON-RPC error response with the given id (which
// may be nil for a parse error on an unidentifiable request) and code/message.
func writeSubAgentError(enc *json.Encoder, id *jsonrpc.ID, code int, msg string) {
	_ = enc.Encode(jsonrpc.Response{
		JSONRPC: jsonrpc.Version,
		ID:      id,
		Error:   &jsonrpc.Error{Code: code, Message: msg},
	})
}

// filterBuiltinTools returns the subset of tools whose Name is in names. An
// empty names list keeps all tools. It lets a process-isolated sub-agent
// restrict the child to a subset (e.g. a read-only researcher), matching
// goroutine mode's Tools filtering, without serializing in-process tool objects
// across the process boundary.
func filterBuiltinTools(tools []agentcore.AgentTool, names []string) []agentcore.AgentTool {
	if len(names) == 0 {
		return tools
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var out []agentcore.AgentTool
	for _, t := range tools {
		if want[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}
