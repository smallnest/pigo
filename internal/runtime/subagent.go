// This file implements single-process sub-agent orchestration (US-027, #45).
//
// A sub-agent is a full agent loop run in a child goroutine with its own
// AgentContext (independent system prompt, message history and tool set),
// launched by the parent through a normal tool call. The child runs to
// completion via StartRun, and its final assistant text is fed back to the
// parent as the tool result — so from the parent loop's perspective a sub-agent
// is just another tool. Because each Execute call spins up an independent
// StartRun with a fresh context, multiple sub-agents can run concurrently (the
// batch executor already runs parallel tool calls in separate goroutines).
//
// There is deliberately NO cross-process RPC: sub-agents are goroutines sharing
// the parent process, matching the spec's "单进程 goroutine" decision.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/smallnest/pigo/internal/agentcore"
)

// SubAgentSpec declares a spawnable sub-agent: its identity (surfaced to the
// model as a tool), the system prompt and tools its child context runs with,
// and a factory for the child's run configuration (provider stream, batch
// registry, hooks). The factory is called once per spawn so each child gets an
// independent RunConfig; NewRunConfig must wire a ToolRegistry consistent with
// Tools.
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
	// (e.g. a read-only researcher sub-agent) and may be empty.
	Tools []agentcore.AgentTool
	// NewRunConfig builds the loop configuration for one child run. It is called
	// per spawn; the returned config's Batch registry should contain Tools.
	NewRunConfig func() RunConfig
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
// child agent loop over the spec's context and returns the child's final text.
type SubAgentTool struct {
	spec SubAgentSpec
}

// NewSubAgentTool builds a sub-agent tool from a spec. NewRunConfig is required
// (it supplies the provider stream that drives the child); a spec without it
// yields a tool whose Execute fails cleanly.
func NewSubAgentTool(spec SubAgentSpec) *SubAgentTool {
	return &SubAgentTool{spec: spec}
}

func (t *SubAgentTool) Name() string { return t.spec.Name }

func (t *SubAgentTool) Description() string { return t.spec.Description }

func (t *SubAgentTool) Schema() json.RawMessage { return subAgentSchema }

// ExecutionMode is parallel: independent sub-agents may run concurrently, since
// each spawns its own context and StartRun goroutine with no shared mutable
// state.
func (t *SubAgentTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// Execute spawns the child agent loop and blocks until it settles, then returns
// the child's final assistant text as the tool result. The parent's ctx governs
// the child, so cancelling the parent run cancels in-flight sub-agents.
func (t *SubAgentTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	if t.spec.NewRunConfig == nil {
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

	childCtx := &agentcore.AgentContext{
		SystemPrompt: t.spec.SystemPrompt,
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(a.Prompt)}},
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
