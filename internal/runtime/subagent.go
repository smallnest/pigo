// This file implements sub-agent orchestration (US-027, #45) with an optional
// process-isolation mode (US-019, #135).
//
// A sub-agent is a full agent loop with its own AgentContext (independent system
// prompt, message history and tool set), launched by the parent through a normal
// tool call. The child runs to completion and its final assistant text is fed
// back to the parent as the tool result - so from the parent loop's perspective
// a sub-agent is just another tool.
//
// Two isolation modes are supported, selected by SubAgentSpec.Isolation:
//
//   - Goroutine (default): the child loop runs in-process in a goroutine sharing
//     the parent process, matching the original "单进程 goroutine" decision.
//   - Process: the parent spawns a fresh pigo subprocess (pigo --subagent-rpc)
//     and delegates the run over stdio JSON-RPC (reusing internal/jsonrpc). The
//     child runs in a separate process, so a crash or resource leak in the child
//     cannot affect the parent loop; a crash is surfaced as a tool error. The
//     subprocess resolves its own provider from the model/provider passed in the
//     request and inherits the parent environment for credentials.
//
// Because each Execute call spins up an independent run, multiple sub-agents can
// run concurrently (the batch executor already runs parallel tool calls in
// separate goroutines/processes).
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/jsonrpc"
)

// SubAgentIsolation selects how a sub-agent runs relative to its parent.
type SubAgentIsolation int

const (
	// SubAgentIsolationGoroutine runs the child agent loop in-process in a
	// goroutine. This is the default and the original behavior; it changes
	// nothing about how sub-agents previously ran.
	SubAgentIsolationGoroutine SubAgentIsolation = iota
	// SubAgentIsolationProcess runs the child in a fresh pigo subprocess,
	// delegating the run over stdio JSON-RPC. A subprocess crash is surfaced to
	// the parent as a tool error and never affects the parent loop.
	SubAgentIsolationProcess
)

// SubAgentProcessConfig configures process-isolated sub-agent execution. It
// carries the serializable provider config the subprocess needs to reconstruct
// the run: the in-process Stream/GetAPIKey functions a goroutine-mode
// NewRunConfig returns cannot cross a process boundary, so the parent forwards
// the model (and optional base URL/protocol) and the subprocess resolves the
// provider itself, inheriting the parent environment for API keys.
type SubAgentProcessConfig struct {
	// Command is the executable to spawn. When empty, os.Executable() (the pigo
	// binary itself) is used so a pigo process spawns another pigo.
	Command string
	// Args are appended to the command after the subagent-rpc flag. Rarely
	// needed; reserved for test doubles or non-standard layouts.
	Args []string
	// Model is the model id the subprocess runs against. Required. A preset id
	// (e.g. "openrouter/free", "anthropic/claude-...") or ollama/nvidia-prefixed
	// id resolves its own provider; a custom gateway needs BaseURL/Protocol.
	Model string
	// BaseURL and Protocol override the provider endpoint and wire protocol for
	// custom gateways (Protocol "anthropic"/"openai" forces that wire format).
	// Empty falls back to the same resolution the CLI uses.
	BaseURL  string
	Protocol string
	// ToolNames restricts the subprocess's builtin tool set to the named tools
	// (e.g. a read-only researcher). Empty keeps all builtins. Non-builtin names
	// are ignored: custom/plugin tools cannot cross a process boundary, so a
	// process-isolated child runs with builtins only.
	ToolNames []string
	// Env is the child's environment (os/exec form). When nil the child inherits
	// the parent environment, which is how it picks up provider API keys.
	Env []string
	// Dir is the child's working directory; empty means the parent's.
	Dir string
	// Stderr optionally receives the child's stderr. When nil it is discarded.
	Stderr io.Writer
}

// SubAgentRunParams is the JSON-RPC request payload for a process-isolated
// sub-agent run (the "subagent/run" method). It is the wire contract between
// the parent (SubAgentTool in process mode) and the pigo subprocess
// (cmd/pigo --subagent-rpc).
type SubAgentRunParams struct {
	Prompt       string   `json:"prompt"`
	SystemPrompt string   `json:"systemPrompt,omitempty"`
	Model        string   `json:"model"`
	BaseURL      string   `json:"baseUrl,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	Tools        []string `json:"tools,omitempty"`
}

// SubAgentRunResult is the JSON-RPC response payload carrying the child's final
// assistant text.
type SubAgentRunResult struct {
	Text string `json:"text"`
}

// SubAgentRPCMethod is the JSON-RPC method name the parent calls on the
// subprocess: "subagent/run".
const SubAgentRPCMethod = "subagent/run"

// SubAgentRPCFlag is the command-line flag the parent launches the pigo
// subprocess with so it enters the sub-agent RPC server mode: "--subagent-rpc".
const SubAgentRPCFlag = "--subagent-rpc"

// SubAgentSpec declares a spawnable sub-agent: its identity (surfaced to the
// model as a tool), the system prompt and tools its child context runs with,
// and a factory for the child's run configuration (provider stream, batch
// registry, hooks). The factory is called once per spawn so each child gets an
// independent RunConfig; NewRunConfig must wire a ToolRegistry consistent with
// Tools. It is used by goroutine mode; process mode uses Process instead (the
// subprocess builds its own RunConfig from the serializable provider config).
type SubAgentSpec struct {
	// Name is the tool name the parent invokes to spawn this sub-agent.
	Name string
	// Description is injected into the parent's tool list / capability list so
	// the model knows when to delegate.
	Description string
	// SystemPrompt seeds the child context's system prompt. When empty the child
	// runs with no system prompt.
	SystemPrompt string
	// Tools is the child's independent tool set. It may differ from the parent's
	// (e.g. a read-only researcher sub-agent) and may be empty. In goroutine
	// mode these exact tools run in-process; in process mode only the tools'
	// NAMES are forwarded (the subprocess rebuilds builtins by name).
	Tools []agentcore.AgentTool
	// NewRunConfig builds the loop configuration for one child run in goroutine
	// mode. It is called per spawn; the returned config's Batch registry should
	// contain Tools. Ignored in process mode (the subprocess resolves its own).
	NewRunConfig func() RunConfig
	// Isolation selects goroutine (default) vs process execution. Zero value is
	// goroutine, preserving the original behavior.
	Isolation SubAgentIsolation
	// Process configures process-isolated execution. Required when Isolation is
	// SubAgentIsolationProcess; ignored otherwise.
	Process SubAgentProcessConfig
}

// subAgentArgs is the JSON argument shape for a sub-agent tool call: a single
// free-form prompt describing the delegated task.
type subAgentArgs struct {
	Prompt string `json:"prompt"`
}

// subAgentSchema is the JSON Schema validating a sub-agent invocation.
var subAgentSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "prompt": {
      "type": "string",
      "description": "The task for the sub-agent to perform, described in full since the sub-agent runs with a fresh context."
    }
  },
  "required": ["prompt"],
  "additionalProperties": false
}`)

// SubAgentTool adapts a SubAgentSpec into an AgentTool. Executing it spawns a
// child agent run (in a goroutine or a subprocess, per Isolation) and returns
// the child's final text.
type SubAgentTool struct {
	spec SubAgentSpec
	// processCall, when non-nil, overrides the default subprocess transport for
	// process-isolated mode. Tests inject a fake to exercise the process-mode
	// logic (params shaping, crash-as-error, result forwarding) without building
	// a real binary; production leaves it nil so Execute uses defaultProcessCall.
	processCall func(ctx context.Context, cfg SubAgentProcessConfig, params SubAgentRunParams) (string, error)
}

// NewSubAgentTool builds a sub-agent tool from a spec. In goroutine mode
// NewRunConfig is required (it supplies the provider stream that drives the
// child); in process mode Process.Model is required instead.
func NewSubAgentTool(spec SubAgentSpec) *SubAgentTool {
	return &SubAgentTool{spec: spec}
}

func (t *SubAgentTool) Name() string { return t.spec.Name }

func (t *SubAgentTool) Description() string { return t.spec.Description }

func (t *SubAgentTool) Schema() json.RawMessage { return subAgentSchema }

// ExecutionMode is parallel: independent sub-agents may run concurrently, since
// each spawns its own context and run (goroutine or process) with no shared
// mutable state.
func (t *SubAgentTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// Execute spawns the child agent run and blocks until it settles, then returns
// the child's final assistant text as the tool result. The parent's ctx governs
// the child, so cancelling the parent run cancels in-flight sub-agents (in
// goroutine mode via ctx; in process mode via ctx cancelling the JSON-RPC call
// and Close killing the child).
func (t *SubAgentTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	// Goroutine mode requires NewRunConfig (it supplies the in-process provider
	// stream). Process mode does not - the subprocess resolves its own provider
	// from Process.Model - so the check is guarded to goroutine mode. This
	// preserves the original precedence (nil NewRunConfig reported before an
	// empty prompt) for the unchanged goroutine path.
	if t.spec.Isolation != SubAgentIsolationProcess && t.spec.NewRunConfig == nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: no run configuration", t.spec.Name)
	}
	var a subAgentArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: decode args: %w", t.spec.Name, err)
		}
	}
	if a.Prompt == "" {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: empty prompt", t.spec.Name)
	}

	if t.spec.Isolation == SubAgentIsolationProcess {
		// Process mode returns only the child's final text (the JSON-RPC protocol
		// does not stream partial updates), so onUpdate is intentionally not
		// forwarded here; a caller supplying a sink gets no deltas in this mode.
		return t.executeProcess(ctx, a.Prompt)
	}
	return t.executeGoroutine(ctx, a.Prompt, onUpdate)
}

// executeGoroutine runs the child agent loop in-process and returns its final
// text. This is the default mode and the original sub-agent behavior.
func (t *SubAgentTool) executeGoroutine(ctx context.Context, prompt string, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	childCtx := &agentcore.AgentContext{
		SystemPrompt: t.spec.SystemPrompt,
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(prompt)}},
		},
		Tools: t.spec.Tools,
	}

	stream := StartRun(ctx, childCtx, t.spec.NewRunConfig())
	// Drain events (DrainStream never returns early, so the producer goroutine is
	// never blocked on back-pressure); forward streamed child text as
	// tool-execution updates when a sink is set.
	var h StreamHandler
	if onUpdate != nil {
		h.OnText = func(delta string) {
			onUpdate(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(delta)}})
		}
	}
	final, err := DrainStream(ctx, stream, h)
	if err != nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: %w", t.spec.Name, err)
	}
	text := ""
	if final != nil {
		text = agentcore.ContentToText(final.Content)
	}
	if text == "" {
		text = fmt.Sprintf("(sub-agent %q produced no text output)", t.spec.Name)
	}
	// Surface a failed child run as a tool error so the parent model gets a
	// signal the delegation failed (the tool executor marks the result
	// IsError). A child whose final turn stopped on error/aborted otherwise
	// looks like a successful delegation carrying error text.
	if final != nil && (final.StopReason == agentcore.StopReasonError || final.StopReason == agentcore.StopReasonAborted) {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q failed (%s): %s", t.spec.Name, final.StopReason, text)
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(text)}}, nil
}

// executeProcess runs the child agent loop in a fresh pigo subprocess over stdio
// JSON-RPC and returns its final text. A subprocess crash, transport error, or
// failed child run is surfaced as a tool error; the parent loop is unaffected.
// Streamed child text is not forwarded (the process protocol returns only the
// final result); the parent sees the complete result when the child settles.
func (t *SubAgentTool) executeProcess(ctx context.Context, prompt string) (agentcore.AgentToolResult, error) {
	cfg := t.spec.Process
	if cfg.Model == "" {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: process mode requires Process.Model", t.spec.Name)
	}
	// Forward the child's tool names so the subprocess can rebuild a matching
	// builtin set; an explicit ToolNames list wins over deriving from Tools.
	toolNames := cfg.ToolNames
	if len(toolNames) == 0 {
		for _, tl := range t.spec.Tools {
			toolNames = append(toolNames, tl.Name())
		}
	}
	params := SubAgentRunParams{
		Prompt:       prompt,
		SystemPrompt: t.spec.SystemPrompt,
		Model:        cfg.Model,
		BaseURL:      cfg.BaseURL,
		Protocol:     cfg.Protocol,
		Tools:        toolNames,
	}
	call := t.processCall
	if call == nil {
		call = defaultProcessCall
	}
	text, err := call(ctx, cfg, params)
	if err != nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q (process): %w", t.spec.Name, err)
	}
	if text == "" {
		text = fmt.Sprintf("(sub-agent %q produced no text output)", t.spec.Name)
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(text)}}, nil
}

// defaultProcessCall is the production subprocess transport: it launches the
// pigo binary (or cfg.Command) with the subagent-rpc flag, sends a single
// "subagent/run" JSON-RPC request over the child's stdin, and returns the
// child's final text from the response. The child is closed (killed if it does
// not exit on its own) before returning. A crash, transport error, or RPC error
// is returned as a Go error so executeProcess surfaces it as a tool error.
func defaultProcessCall(ctx context.Context, cfg SubAgentProcessConfig, params SubAgentRunParams) (string, error) {
	command := cfg.Command
	if command == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve pigo executable: %w", err)
		}
		command = exe
	}
	args := append([]string{SubAgentRPCFlag}, cfg.Args...)
	client, err := jsonrpc.NewClient(jsonrpc.Config{
		Command: command,
		Args:    args,
		Env:     cfg.Env,
		Dir:     cfg.Dir,
		Stderr:  cfg.Stderr,
	})
	if err != nil {
		return "", err
	}
	defer client.Close()
	raw, err := client.Call(ctx, SubAgentRPCMethod, params)
	if err != nil {
		return "", err
	}
	var res SubAgentRunResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("decode sub-agent result: %w", err)
	}
	return res.Text, nil
}

// RunSubAgentOnce runs one sub-agent loop to completion and returns the child's
// final assistant text. It is the execution core shared by the process-isolated
// subprocess (cmd/pigo --subagent-rpc): given a resolved RunConfig (provider
// stream, tool registry) and the prompt/system prompt, it builds a fresh child
// context and drains the run. A run whose final turn stopped on error/aborted
// is reported as an error so the subprocess surfaces failure (as an RPC error)
// rather than returning empty text. It does not stream partial updates: the
// process protocol returns only the final result.
func RunSubAgentOnce(ctx context.Context, systemPrompt, prompt string, tools []agentcore.AgentTool, runCfg RunConfig) (string, error) {
	childCtx := &agentcore.AgentContext{
		SystemPrompt: systemPrompt,
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(prompt)}},
		},
		Tools: tools,
	}
	stream := StartRun(ctx, childCtx, runCfg)
	final, err := DrainStream(ctx, stream, StreamHandler{})
	if err != nil {
		return "", err
	}
	text := ""
	if final != nil {
		text = agentcore.ContentToText(final.Content)
	}
	if final != nil && (final.StopReason == agentcore.StopReasonError || final.StopReason == agentcore.StopReasonAborted) {
		// When the loop synthesizes an error turn (e.g. a provider connection
		// failure) the diagnostic lands in ErrorMessage, not Content; fall back
		// to it so the subprocess surfaces the real cause rather than a bare
		// "error" stop reason.
		if text == "" && final.ErrorMessage != "" {
			text = final.ErrorMessage
		}
		if text == "" {
			text = string(final.StopReason)
		}
		return text, fmt.Errorf("sub-agent failed (%s): %s", final.StopReason, text)
	}
	return text, nil
}
