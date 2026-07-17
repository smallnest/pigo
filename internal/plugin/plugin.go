// This file implements the plugin client (US-016, #132): it launches a plugin
// executable, performs the initialize handshake, and exposes the plugin's
// declared tools as agentcore.AgentTool values that forward invocations over
// JSON-RPC. Crash isolation lives here — a call against a plugin whose process
// has died returns an error result, never a panic.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/jsonrpc"
)

// initTimeout bounds the initialize handshake so a plugin that never replies
// cannot hang plugin discovery.
const initTimeout = 10 * time.Second

// Plugin is a running plugin: its JSON-RPC client plus the manifest it declared
// during initialize.
type Plugin struct {
	Manifest Manifest
	client   *jsonrpc.Client
}

// Load starts the plugin executable at path (with optional args) and performs
// the initialize handshake. stderr, when non-nil, receives the plugin's stderr
// for logging. The caller must Close the returned Plugin.
func Load(command string, args []string, stderr io.Writer) (*Plugin, error) {
	client, err := jsonrpc.NewClient(jsonrpc.Config{
		Command: command,
		Args:    args,
		Stderr:  stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("plugin: launch %q: %w", command, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	raw, err := client.Call(ctx, "initialize", nil)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("plugin: initialize %q: %w", command, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("plugin: decode manifest from %q: %w", command, err)
	}
	if m.Name == "" {
		_ = client.Close()
		return nil, fmt.Errorf("plugin %q: manifest has empty name", command)
	}
	return &Plugin{Manifest: m, client: client}, nil
}

// Tools adapts each tool the plugin declared into an agentcore.AgentTool that
// forwards Execute over JSON-RPC.
func (p *Plugin) Tools() []agentcore.AgentTool {
	out := make([]agentcore.AgentTool, 0, len(p.Manifest.Tools))
	for _, spec := range p.Manifest.Tools {
		out = append(out, &pluginTool{plugin: p, spec: spec})
	}
	return out
}

// Close shuts the plugin down: it sends a best-effort shutdown notification then
// closes the transport (which closes stdin and, if needed, kills the child).
func (p *Plugin) Close() error {
	_ = p.client.Notify("shutdown", nil)
	return p.client.Close()
}

// call forwards a tool invocation to the plugin and returns its result.
func (p *Plugin) call(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	raw, err := p.client.Call(ctx, "tools/call", CallParams{Name: name, Arguments: args})
	if err != nil {
		return CallResult{}, err
	}
	var res CallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return CallResult{}, fmt.Errorf("plugin %q: decode result for %q: %w", p.Manifest.Name, name, err)
	}
	return res, nil
}

// pluginTool adapts one plugin-declared tool to the agentcore.AgentTool
// interface. All invocations are forwarded to the owning plugin over RPC.
type pluginTool struct {
	plugin *Plugin
	spec   ToolSpec
}

// Name implements AgentTool.
func (t *pluginTool) Name() string { return t.spec.Name }

// Description implements AgentTool.
func (t *pluginTool) Description() string { return t.spec.Description }

// Schema implements AgentTool. An empty schema declared by the plugin degrades
// to a permissive object schema so registration never fails.
func (t *pluginTool) Schema() json.RawMessage {
	if len(t.spec.Schema) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return t.spec.Schema
}

// ExecutionMode implements AgentTool. Plugin calls cross a process boundary and
// have unknown side effects, so they run sequentially to be safe.
func (t *pluginTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool by forwarding the call to the plugin process. A
// transport error (e.g. the plugin crashed) is isolated: it degrades to an error
// result so a dead plugin cannot take down the agent loop.
func (t *pluginTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	res, err := t.plugin.call(ctx, t.spec.Name, args)
	if err != nil {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(
				fmt.Sprintf("%s: plugin call failed: %v", t.spec.Name, err))},
		}, nil
	}
	result := agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(res.Content)},
	}
	if res.IsError {
		result.Details = map[string]any{"isError": true}
	}
	return result, nil
}
