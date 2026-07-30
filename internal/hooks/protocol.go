// This file defines the process contract between pigo and a user hook command:
// what pigo writes to the hook's stdin (HookInput), what pigo reads back from
// its stdout (HookOutput), and the internal merged decision the dispatcher
// produces (HookDecision). It also defines the exit-code semantics parsing.
//
// The wire contract follows Claude Code: exit 0 = allow, exit 2 = block (with
// stderr as the reason), any other non-zero = execution failure. On exit 0 a
// well-formed JSON stdout is parsed as HookOutput; a non-JSON stdout is a no-op.
package hooks

import (
	"encoding/json"
	"strings"
)

// HookInput is the JSON payload pigo writes to a hook command's stdin. It
// carries only observable, non-secret fields (FR-17) — never API keys or
// credentials. Per-event fields are omitempty so a payload only contains the
// fields relevant to its event type.
type HookInput struct {
	EventType    string          `json:"event_type"`
	SessionID    string          `json:"session_id,omitempty"`
	ProjectDir   string          `json:"project_dir,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`     // Pre/PostToolUse
	ToolInput    json.RawMessage `json:"tool_input,omitempty"`    // Pre/PostToolUse
	ToolResponse json.RawMessage `json:"tool_response,omitempty"` // PostToolUse
	Prompt       string          `json:"prompt,omitempty"`        // UserPromptSubmit
	StopReason   string          `json:"stop_reason,omitempty"`   // Stop/SessionEnd
	Source       string          `json:"source,omitempty"`        // SessionStart (startup/resume)
	Trigger      string          `json:"trigger,omitempty"`       // PreCompact (manual/auto)
	Message      string          `json:"message,omitempty"`       // Notification
}

// HookOutput is the optional JSON a hook may print to stdout to influence the
// agent. A non-JSON stdout on exit 0 is treated as an empty HookOutput (no
// operation). Decision "block" is equivalent to exiting with code 2.
type HookOutput struct {
	Decision          string          `json:"decision,omitempty"` // "block" | "approve" | ""
	Reason            string          `json:"reason,omitempty"`
	AdditionalContext string          `json:"additionalContext,omitempty"`
	Continue          *bool           `json:"continue,omitempty"`
	UpdatedInput      json.RawMessage `json:"updatedInput,omitempty"` // PreToolUse: rewrite tool args
}

// blocks reports whether this output requests a block. A decision of "block"
// blocks; an explicit continue=false also blocks. "approve" and the empty
// decision allow.
func (o HookOutput) blocks() bool {
	if strings.EqualFold(o.Decision, "block") {
		return true
	}
	if o.Continue != nil && !*o.Continue {
		return true
	}
	return false
}

// HookDecision is the dispatcher's merged result after running all matched
// hooks for one event. Block is set if any hook blocked; Reason accumulates the
// blocking reasons; AdditionalContext accumulates injected context in order;
// UpdatedInput holds the last-provided rewrite (last writer wins, §5.4).
type HookDecision struct {
	Block             bool
	Reason            string
	AdditionalContext string
	UpdatedInput      json.RawMessage
}

// parseHookOutput parses a hook's stdout into a HookOutput. On exit 0 a
// non-JSON body is a no-op (returns the zero value, ok=false). Empty/whitespace
// stdout is also a no-op. A valid JSON object is parsed and ok is true.
func parseHookOutput(stdout []byte) (HookOutput, bool) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return HookOutput{}, false
	}
	var out HookOutput
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return HookOutput{}, false
	}
	return out, true
}
