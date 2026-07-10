package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// A recorded OpenAI Chat Completions SSE stream covering a text delta followed
// by a two-fragment tool call, ending with finish_reason=tool_calls and a final
// usage-only chunk. Trimmed but structurally faithful to the real wire format
// (the transport strips the `data:` prefix and the trailing [DONE]).
const openaiToolCallSSE = `data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"delta":{"role":"assistant"}}]}

data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"delta":{"content":"Let me "}}]}

data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"delta":{"content":"check."}}]}

data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}

data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" \"SF\"}"}}]}}]}

data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":8}}

data: [DONE]

`

func TestOpenAIDecoderToolCallStream(t *testing.T) {
	dec := NewOpenAIDecoder()
	events, final := feedSSE(t, dec, openaiToolCallSSE)

	// The last emitted event must be the terminal done event.
	if len(events) == 0 || events[len(events)-1].EventKind() != StreamEventDone {
		t.Fatalf("expected a done event last, got %v", eventKinds(events))
	}
	// Stop reason: tool_calls → tool_use.
	if final.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", final.StopReason)
	}
	// Usage: prompt→input, completion→output.
	if final.Usage == nil || final.Usage.InputTokens != 11 || final.Usage.OutputTokens != 8 {
		t.Errorf("usage = %+v, want input=11 output=8", final.Usage)
	}
	// Response identity.
	if final.ResponseID != "chatcmpl-1" || final.ResponseModel != "gpt-4o" {
		t.Errorf("response id/model = %q/%q", final.ResponseID, final.ResponseModel)
	}

	// Content blocks: text first, then the tool call.
	if len(final.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d: %+v", len(final.Content), final.Content)
	}
	txt, ok := final.Content[0].(agentcore.TextContent)
	if !ok || txt.Text != "Let me check." {
		t.Errorf("text block = %+v", final.Content[0])
	}
	tool, ok := final.Content[1].(agentcore.ToolCallContent)
	if !ok || tool.Name != "get_weather" || tool.ID != "call_1" {
		t.Fatalf("tool block = %+v", final.Content[1])
	}
	// tool_call arguments must have accumulated into valid JSON across fragments.
	var args map[string]string
	if err := json.Unmarshal(tool.Arguments, &args); err != nil {
		t.Fatalf("tool arguments not valid JSON %q: %v", tool.Arguments, err)
	}
	if args["city"] != "SF" {
		t.Errorf("tool arguments = %v, want city=SF", args)
	}
}

func TestOpenAIDecoderTextOnlyStop(t *testing.T) {
	body := `data: {"id":"c1","model":"gpt-4o","choices":[{"delta":{"content":"Hello"}}]}

data: {"id":"c1","model":"gpt-4o","choices":[{"delta":{"content":" world"}}]}

data: {"id":"c1","model":"gpt-4o","choices":[{"delta":{},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}

data: [DONE]

`
	dec := NewOpenAIDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn", final.StopReason)
	}
	if len(final.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(final.Content))
	}
	txt, ok := final.Content[0].(agentcore.TextContent)
	if !ok || txt.Text != "Hello world" {
		t.Errorf("text = %+v", final.Content[0])
	}
}

func TestOpenAIDecoderLengthMapsToLength(t *testing.T) {
	body := `data: {"id":"c","model":"m","choices":[{"delta":{"content":"truncated"}}]}

data: {"id":"c","model":"m","choices":[{"delta":{},"finish_reason":"length"}]}

data: [DONE]

`
	dec := NewOpenAIDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != agentcore.StopReasonLength {
		t.Errorf("length finish_reason must map to length, got %q", final.StopReason)
	}
}

// TestOpenAIDecoderInlineError verifies an inline error object becomes a decode
// error (which the transport turns into a terminal error event), never a panic.
func TestOpenAIDecoderInlineError(t *testing.T) {
	dec := NewOpenAIDecoder()
	_, err := dec.Decode([]byte(`{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`))
	if err == nil {
		t.Fatal("inline error object must return a decode error")
	}
	if !strings.Contains(err.Error(), "rate_limit_exceeded") {
		t.Errorf("error should name the type, got %v", err)
	}
}

// TestOpenAIDecoderMalformedPayload verifies invalid JSON is a returned error
// (rides the stream as terminal error), not a panic.
func TestOpenAIDecoderMalformedPayload(t *testing.T) {
	dec := NewOpenAIDecoder()
	if _, err := dec.Decode([]byte(`{not json`)); err == nil {
		t.Fatal("malformed payload must return an error")
	}
}

// TestOpenAIDecoderFinishFlushesPartial verifies a stream cut short (no
// finish_reason) still yields a done event on Finish, defaulting to end_turn.
func TestOpenAIDecoderFinishFlushesPartial(t *testing.T) {
	body := `data: {"id":"c","model":"m","choices":[{"delta":{"content":"partial"}}]}

`
	dec := NewOpenAIDecoder()
	events, final := feedSSE(t, dec, body)
	if events[len(events)-1].EventKind() != StreamEventDone {
		t.Fatalf("Finish must emit a terminal done event, got %v", eventKinds(events))
	}
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("cut-short stream should default to end_turn, got %q", final.StopReason)
	}
	if len(final.Content) != 1 {
		t.Fatalf("expected the partial text block, got %+v", final.Content)
	}
}

// TestOpenAIDecoderThroughTransport wires the decoder through the real transport
// pump against a recorded SSE server, exercising the full path including the
// [DONE] terminator handling.
func TestOpenAIDecoderThroughTransport(t *testing.T) {
	srv := sseServer(t, openaiToolCallSSE)
	defer srv.Close()

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    NewOpenAIDecoder(),
	})
	if err != nil {
		t.Fatalf("StreamRequest: %v", err)
	}
	var kinds []string
	for ev := range stream.Events() {
		kinds = append(kinds, ev.EventKind())
	}
	final, resErr := stream.Result(context.Background())
	if resErr != nil {
		t.Fatalf("result: %v", resErr)
	}
	if final.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason via transport = %q, want tool_use", final.StopReason)
	}
	if len(final.Content) != 2 {
		t.Errorf("expected 2 content blocks via transport, got %d", len(final.Content))
	}
	if kinds[len(kinds)-1] != StreamEventDone {
		t.Errorf("stream must end with done, got %v", kinds)
	}
}
