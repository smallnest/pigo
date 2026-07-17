// Package plugin implements pigo's external plugin system (US-016, #132): an
// executable written in any language registers custom tools (and, later, slash
// commands) with pigo without touching pigo's source. pigo launches each plugin
// as a child process and speaks line-delimited JSON-RPC 2.0 over its stdio,
// reusing internal/jsonrpc as the transport.
//
// Protocol (client = pigo, server = plugin):
//
//   - initialize            → Manifest {name, version, tools[], commands[]}
//     The handshake. The plugin declares everything it offers up front.
//   - tools/call {name, arguments} → CallResult {content, isError}
//     pigo forwards a tool invocation; the plugin runs it and returns the text.
//   - shutdown (notification)
//     Sent on Close so a well-behaved plugin can exit before stdin EOF.
//
// A plugin that crashes or misbehaves is isolated: its Start failure is logged
// and skipped (other plugins still load), and a tool call against a dead plugin
// returns an error result rather than propagating up.
//
// This file defines the wire types exchanged during the handshake and tool call.
package plugin

import "encoding/json"

// Manifest is the plugin's self-description, returned from the initialize call.
type Manifest struct {
	// Name identifies the plugin; used to namespace its tools and in diagnostics.
	Name string `json:"name"`
	// Version is an optional free-form version string for diagnostics.
	Version string `json:"version,omitempty"`
	// Tools are the tools this plugin registers with the agent.
	Tools []ToolSpec `json:"tools,omitempty"`
	// Commands are the slash commands this plugin registers.
	Commands []CommandSpec `json:"commands,omitempty"`
}

// ToolSpec declares one tool a plugin exposes. Schema is the JSON Schema for the
// tool's arguments, passed through verbatim to the agent's tool registry.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// CommandSpec declares one slash command a plugin exposes. Prompt is the text
// injected as the next user prompt when the command is invoked (matching the
// declarative-command convention); it may be empty if the plugin handles the
// command by other means.
type CommandSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt,omitempty"`
}

// CallParams is the parameter object for a tools/call request.
type CallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// CallResult is the reply to a tools/call request. Content is the tool output as
// text; IsError marks a tool-level failure (distinct from a transport error).
type CallResult struct {
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
}
