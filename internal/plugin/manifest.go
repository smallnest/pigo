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
//   - event {type, data} (notification)
//     pigo pushes a subscribed agent lifecycle event (US-017, #133). One-way,
//     fire-and-forget: the plugin never replies and a slow plugin is isolated.
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
	// Events lists the agent lifecycle event types this plugin subscribes to
	// (US-017, #133). pigo delivers only these via one-way `event` notifications;
	// an empty list means the plugin observes no events. Valid values are the
	// agentcore.Event* discriminants (e.g. "agent_start", "tool_execution_end").
	Events []string `json:"events,omitempty"`
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

// CommandCallParams is the parameter object for a commands/call request. It
// mirrors CallParams' naming (name/arguments) so a plugin can decode tool and
// command invocations with the same conventions. Name is the command's name;
// Args carries its free-form arguments (e.g. the text following the slash
// command) as raw JSON, passed through verbatim to the plugin.
type CommandCallParams struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments"`
}

// CommandCallResult is the reply to a commands/call request. Prompt is the text
// injected as the next agent turn (matching the declarative-command
// convention); it may be empty if the command produces no prompt. Notifications
// are messages the plugin asks pigo to surface to the user out of band from the
// prompt.
type CommandCallResult struct {
	Prompt        string                `json:"prompt,omitempty"`
	Notifications []CommandNotification `json:"notifications,omitempty"`
}

// CommandNotification is a single message a command asks pigo to surface to the
// user. Message is the human-readable text; Type is an optional severity or
// category hint (e.g. "info", "warning", "error") that pigo may use to style
// the message.
type CommandNotification struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

// EventParams is the parameter object for an `event` notification. Type is the
// event discriminant (an agentcore.Event* value); Data carries a small,
// wire-safe payload for that event (never secrets — see plugin.EventData).
type EventParams struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}
