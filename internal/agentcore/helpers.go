package agentcore

import (
	"context"
	"encoding/json"
	"strings"
)

// ContentToText flattens text blocks of a content list into a single string,
// the lowest-common-denominator representation accepted by every OpenAI-
// compatible gateway. Non-text blocks (thinking, tool calls) are surfaced
// through their own fields, so they are skipped here.
func ContentToText(list ContentList) string {
	var b strings.Builder
	for _, c := range list {
		if tc, ok := c.(TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// LastAssistantOf returns a pointer to the last AssistantMessage in msgs, or nil.
func LastAssistantOf(msgs []AgentMessage) *AssistantMessage {
	for i := len(msgs) - 1; i >= 0; i-- {
		if a, ok := msgs[i].(AssistantMessage); ok {
			return &a
		}
	}
	return nil
}

// EmitFunc emits a loop-level AgentEvent honoring cancellation.
type EmitFunc func(ctx context.Context, ev AgentEvent) error

// PrepareArgumentsFunc optionally rewrites a tool's raw arguments before schema
// validation (e.g. injecting defaults). An error aborts the call with an error
// result. Optional (nil = identity).
type PrepareArgumentsFunc func(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error)

// BeforeToolCallDecision is the optional result of the beforeToolCall hook. When
// Block is true the tool is not executed and an error result is produced;
// Content/Details override the default block message when set.
type BeforeToolCallDecision struct {
	Block   bool
	Content *ContentList
	Details *any
}

// BeforeToolCallFunc runs after validation and may block the call (permission /
// sandbox checks, FR-4/FR-26). Returning nil allows the call. Optional.
type BeforeToolCallFunc func(ctx context.Context, call AgentToolCall) *BeforeToolCallDecision

// AfterToolCallFunc runs after execution and may override the result
// field-by-field via AfterToolCallResult (FR-5, no deep merge). Optional.
type AfterToolCallFunc func(ctx context.Context, call AgentToolCall, result AgentToolResult, isError bool) *AfterToolCallResult
