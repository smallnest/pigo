// Tests for reasoning/thinking wiring across both wire protocols (issues
// #240-#244): request encoders forwarding ThinkingLevel, the Anthropic
// thinking-block echo on multi-turn tool-use messages, OpenAI reasoning_content
// stream decoding, the max_tokens fallback, and strict-gateway empty-content
// handling.
package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// decodeBody unmarshals an encoded request body into a generic map for asserts.
func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return m
}

// TestOpenAIReasoningEffortEncoding verifies ThinkingLevel maps to
// reasoning_effort, and that off/unset omits the field entirely (#240).
func TestOpenAIReasoningEffortEncoding(t *testing.T) {
	base := CompletionRequest{Model: "o3-mini", Context: LlmContext{}}

	// off / unset → no reasoning_effort.
	for _, lvl := range []agentcore.ThinkingLevel{"", agentcore.ThinkingOff} {
		base.Config.ThinkingLevel = lvl
		b, err := encodeOpenAIRequest(base)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if _, ok := decodeBody(t, b)["reasoning_effort"]; ok {
			t.Errorf("level %q: reasoning_effort should be omitted", lvl)
		}
	}

	cases := map[agentcore.ThinkingLevel]string{
		agentcore.ThinkingMinimal: "minimal",
		agentcore.ThinkingLow:     "low",
		agentcore.ThinkingMedium:  "medium",
		agentcore.ThinkingHigh:    "high",
		agentcore.ThinkingXHigh:   "high",
	}
	for lvl, want := range cases {
		base.Config.ThinkingLevel = lvl
		b, err := encodeOpenAIRequest(base)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if got := decodeBody(t, b)["reasoning_effort"]; got != want {
			t.Errorf("level %q: reasoning_effort = %v, want %q", lvl, got, want)
		}
	}
}

// TestAnthropicThinkingEncoding verifies ThinkingLevel enables the thinking
// block with a budget, and off/unset omits it (#240).
func TestAnthropicThinkingEncoding(t *testing.T) {
	base := CompletionRequest{Model: "claude-x", Context: LlmContext{}}

	base.Config.ThinkingLevel = agentcore.ThinkingOff
	b, _ := encodeAnthropicRequest(base, nil)
	if _, ok := decodeBody(t, b)["thinking"]; ok {
		t.Error("off: thinking block should be omitted")
	}

	base.Config.ThinkingLevel = agentcore.ThinkingMedium
	b, _ = encodeAnthropicRequest(base, nil)
	th, ok := decodeBody(t, b)["thinking"].(map[string]any)
	if !ok {
		t.Fatal("medium: thinking block missing")
	}
	if th["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", th["type"])
	}
	if bt, _ := th["budget_tokens"].(float64); bt <= 0 {
		t.Errorf("thinking.budget_tokens = %v, want > 0", th["budget_tokens"])
	}
}

// TestAnthropicMaxTokensFallback verifies the fallback prefers the model's
// MaxOutputTokens and otherwise uses the coding-friendly 8192 default (#243).
func TestAnthropicMaxTokensFallback(t *testing.T) {
	req := CompletionRequest{Model: "claude-x", Context: LlmContext{}}

	// No model metadata → 8192 default.
	b, _ := encodeAnthropicRequest(req, nil)
	if got := decodeBody(t, b)["max_tokens"].(float64); got != 8192 {
		t.Errorf("default max_tokens = %v, want 8192", got)
	}

	// Model with a declared cap → that cap.
	models := []Model{{ID: "claude-x", MaxOutputTokens: 12000}}
	b, _ = encodeAnthropicRequest(req, models)
	if got := decodeBody(t, b)["max_tokens"].(float64); got != 12000 {
		t.Errorf("model-cap max_tokens = %v, want 12000", got)
	}

	// Explicit Extra hint still wins.
	req.Config.Extra = map[string]any{"max_tokens": 2000}
	b, _ = encodeAnthropicRequest(req, models)
	if got := decodeBody(t, b)["max_tokens"].(float64); got != 2000 {
		t.Errorf("explicit max_tokens = %v, want 2000", got)
	}
}

// TestAnthropicThinkingBlockEcho verifies a multi-turn assistant message with a
// thinking block + tool call re-emits the thinking block (with signature) ahead
// of the tool_use block (#241).
func TestAnthropicThinkingBlockEcho(t *testing.T) {
	think := agentcore.NewThinkingContent("let me reason")
	think.ThinkingSignature = "sig-abc"
	msg := agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			think,
			agentcore.NewToolCallContent("call_1", "read", json.RawMessage(`{"path":"x"}`)),
		},
	}
	entry := encodeAnthropicMessage(msg)
	blocks, ok := entry["content"].([]map[string]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("want 2 content blocks, got %#v", entry["content"])
	}
	if blocks[0]["type"] != "thinking" {
		t.Errorf("first block type = %v, want thinking", blocks[0]["type"])
	}
	if blocks[0]["signature"] != "sig-abc" {
		t.Errorf("thinking signature = %v, want sig-abc", blocks[0]["signature"])
	}
	if blocks[1]["type"] != "tool_use" {
		t.Errorf("second block type = %v, want tool_use", blocks[1]["type"])
	}
}

// TestAnthropicRedactedThinkingEcho verifies redacted thinking round-trips as a
// redacted_thinking block carrying the signature as data (#241).
func TestAnthropicRedactedThinkingEcho(t *testing.T) {
	think := agentcore.ThinkingContent{Type: agentcore.ContentTypeThinking, Redacted: true, ThinkingSignature: "redacted-data"}
	msg := agentcore.AssistantMessage{Content: agentcore.ContentList{think}}
	entry := encodeAnthropicMessage(msg)
	blocks := entry["content"].([]map[string]any)
	if blocks[0]["type"] != "redacted_thinking" || blocks[0]["data"] != "redacted-data" {
		t.Errorf("redacted block = %#v", blocks[0])
	}
}

// TestOpenAIAssistantContentNullWithToolCalls verifies a tool-call-only
// assistant turn sends content:null (not ""), while a text-only turn keeps its
// text (#244).
func TestOpenAIAssistantContentNullWithToolCalls(t *testing.T) {
	toolOnly := agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			agentcore.NewToolCallContent("call_1", "read", json.RawMessage(`{}`)),
		},
	}
	entry := encodeOpenAIMessage(toolOnly)[0]
	if entry["content"] != nil {
		t.Errorf("tool-only content = %#v, want nil", entry["content"])
	}
	if _, ok := entry["tool_calls"]; !ok {
		t.Error("tool_calls missing")
	}

	textOnly := agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("hi")},
	}
	if got := encodeOpenAIMessage(textOnly)[0]["content"]; got != "hi" {
		t.Errorf("text content = %#v, want hi", got)
	}
}

// TestAnthropicEmptyAssistantNoEmptyText verifies an assistant message with no
// usable content does not emit an empty-string text block (#244).
func TestAnthropicEmptyAssistantNoEmptyText(t *testing.T) {
	msg := agentcore.AssistantMessage{Content: agentcore.ContentList{agentcore.NewTextContent("")}}
	entry := encodeAnthropicMessage(msg)
	blocks := entry["content"].([]map[string]any)
	if len(blocks) != 1 {
		t.Fatalf("want 1 fallback block, got %d", len(blocks))
	}
	if txt, _ := blocks[0]["text"].(string); strings.TrimSpace(txt) == "" && txt == "" {
		t.Errorf("fallback text block is empty string, want non-empty placeholder")
	}
}

// TestAnthropicThinkingRaisesMaxTokens verifies max_tokens is lifted above the
// thinking budget: Anthropic requires budget_tokens < max_tokens, so a low cap
// must be raised to leave headroom for the visible reply (#243).
func TestAnthropicThinkingRaisesMaxTokens(t *testing.T) {
	req := CompletionRequest{Model: "claude-x", Context: LlmContext{}}
	req.Config.ThinkingLevel = agentcore.ThinkingXHigh // budget 32768

	// Default cap (8192) is below the budget → must be raised above it.
	b, _ := encodeAnthropicRequest(req, nil)
	body := decodeBody(t, b)
	budget := body["thinking"].(map[string]any)["budget_tokens"].(float64)
	maxTok := body["max_tokens"].(float64)
	if maxTok <= budget {
		t.Errorf("max_tokens = %v, want > budget_tokens %v", maxTok, budget)
	}

	// A caller cap already above budget+headroom is left untouched.
	req.Config.Extra = map[string]any{"max_tokens": 100000}
	b, _ = encodeAnthropicRequest(req, nil)
	if got := decodeBody(t, b)["max_tokens"].(float64); got != 100000 {
		t.Errorf("max_tokens = %v, want caller value 100000", got)
	}
}

// TestOpenAIReasoningContentDecoding verifies the decoder accumulates
// reasoning_content into a ThinkingContent block ahead of text (#242).
func TestOpenAIReasoningContentDecoding(t *testing.T) {
	d := NewOpenAIDecoder()
	chunks := []string{
		`{"id":"c1","choices":[{"delta":{"reasoning_content":"think "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"harder"}}]}`,
		`{"choices":[{"delta":{"content":"answer"}}]}`,
		`{"choices":[{"finish_reason":"stop"}]}`,
	}
	var events []StreamEvent
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		events = append(events, evs...)
	}
	done, _ := d.Finish()
	events = append(events, done...)

	var final agentcore.AssistantMessage
	for _, e := range events {
		if de, ok := e.(StreamDoneEvent); ok {
			final = de.Message
		}
	}
	if len(final.Content) != 2 {
		t.Fatalf("want thinking+text, got %d blocks: %#v", len(final.Content), final.Content)
	}
	th, ok := final.Content[0].(agentcore.ThinkingContent)
	if !ok || th.Thinking != "think harder" {
		t.Errorf("block[0] = %#v, want thinking 'think harder'", final.Content[0])
	}
	txt, ok := final.Content[1].(agentcore.TextContent)
	if !ok || txt.Text != "answer" {
		t.Errorf("block[1] = %#v, want text 'answer'", final.Content[1])
	}

	sawThinking := false
	for _, e := range events {
		if _, ok := e.(StreamThinkingEvent); ok {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Error("expected at least one StreamThinkingEvent")
	}
}
