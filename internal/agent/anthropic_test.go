package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A recorded Anthropic Messages API SSE stream covering a text block, a
// thinking block (with signature), and a tool_use block, ending with a
// tool_use stop reason and output-token usage. Trimmed but structurally
// faithful to the real wire format.
const anthropicToolUseSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01ABC","model":"claude-opus-4-8","usage":{"input_tokens":42,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think. "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Use the tool."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sigABC"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"I'll check "}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"the weather."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":" \"SF\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":57}}

event: message_stop
data: {"type":"message_stop"}

`

// feedSSE splits a recorded SSE body into events (blank-line separated),
// extracts each `data:` payload, and drives the decoder exactly as the
// transport pump would, returning all emitted events plus the final message.
func feedSSE(t *testing.T, dec Decoder, body string) ([]StreamEvent, AssistantMessage) {
	t.Helper()
	var events []StreamEvent
	for _, block := range strings.Split(body, "\n\n") {
		var payload strings.Builder
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(line, "data:") {
				payload.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if payload.Len() == 0 {
			continue
		}
		// [DONE] is a transport-level terminator, not a decoder payload.
		if payload.String() == "[DONE]" {
			continue
		}
		evs, err := dec.Decode([]byte(payload.String()))
		if err != nil {
			t.Fatalf("decode %q: %v", payload.String(), err)
		}
		events = append(events, evs...)
	}
	finalEvents, err := dec.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	events = append(events, finalEvents...)

	var final AssistantMessage
	for _, ev := range events {
		if d, ok := ev.(StreamDoneEvent); ok {
			final = d.Message
		}
	}
	return events, final
}

func TestAnthropicDecoderToolUseStream(t *testing.T) {
	dec := NewAnthropicDecoder()
	events, final := feedSSE(t, dec, anthropicToolUseSSE)

	// The first emitted event must be a start event.
	if len(events) == 0 || events[0].EventKind() != StreamEventStart {
		t.Fatalf("expected a start event first, got %v", eventKinds(events))
	}
	// The last emitted event must be the terminal done event.
	if events[len(events)-1].EventKind() != StreamEventDone {
		t.Fatalf("expected a done event last, got %v", eventKinds(events))
	}

	// Stop reason: tool_use.
	if final.StopReason != StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", final.StopReason)
	}
	// Usage: input from message_start, output from message_delta.
	if final.Usage == nil || final.Usage.InputTokens != 42 || final.Usage.OutputTokens != 57 {
		t.Errorf("usage = %+v, want input=42 output=57", final.Usage)
	}
	// Response identity from message_start.
	if final.ResponseID != "msg_01ABC" || final.ResponseModel != "claude-opus-4-8" {
		t.Errorf("response id/model = %q/%q", final.ResponseID, final.ResponseModel)
	}

	// Content blocks in index order: thinking, text, tool_use.
	if len(final.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d: %+v", len(final.Content), final.Content)
	}
	th, ok := final.Content[0].(ThinkingContent)
	if !ok || th.Thinking != "Let me think. Use the tool." {
		t.Errorf("thinking block = %+v", final.Content[0])
	}
	if th.ThinkingSignature != "sigABC" {
		t.Errorf("thinking signature = %q, want sigABC", th.ThinkingSignature)
	}
	txt, ok := final.Content[1].(TextContent)
	if !ok || txt.Text != "I'll check the weather." {
		t.Errorf("text block = %+v", final.Content[1])
	}
	tool, ok := final.Content[2].(ToolCallContent)
	if !ok || tool.Name != "get_weather" || tool.ID != "toolu_01" {
		t.Fatalf("tool block = %+v", final.Content[2])
	}
	// tool_use JSON must have accumulated into valid arguments.
	var args map[string]string
	if err := json.Unmarshal(tool.Arguments, &args); err != nil {
		t.Fatalf("tool arguments not valid JSON %q: %v", tool.Arguments, err)
	}
	if args["city"] != "SF" {
		t.Errorf("tool arguments = %v, want city=SF", args)
	}
}

func TestAnthropicDecoderTextOnlyEndTurn(t *testing.T) {
	body := `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":10,"output_tokens":0}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

data: {"type":"message_stop"}

`
	dec := NewAnthropicDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn", final.StopReason)
	}
	if len(final.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(final.Content))
	}
	txt, ok := final.Content[0].(TextContent)
	if !ok || txt.Text != "Hello world" {
		t.Errorf("text = %+v", final.Content[0])
	}
}

func TestAnthropicDecoderMaxTokensMapsToLength(t *testing.T) {
	body := `data: {"type":"message_start","message":{"id":"m","model":"c","usage":{"input_tokens":5,"output_tokens":0}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"truncated"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":100}}

data: {"type":"message_stop"}

`
	dec := NewAnthropicDecoder()
	_, final := feedSSE(t, dec, body)
	if final.StopReason != StopReasonLength {
		t.Errorf("max_tokens must map to length, got %q", final.StopReason)
	}
}

// TestAnthropicDecoderErrorEvent verifies an Anthropic `error` event becomes a
// decode error (which the transport turns into a terminal error event), never
// a panic.
func TestAnthropicDecoderErrorEvent(t *testing.T) {
	dec := NewAnthropicDecoder()
	_, err := dec.Decode([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	if err == nil {
		t.Fatal("error event must return a decode error")
	}
	if !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("error should name the type, got %v", err)
	}
}

// TestAnthropicDecoderMalformedPayload verifies invalid JSON is a returned
// error (rides the stream as terminal error), not a panic.
func TestAnthropicDecoderMalformedPayload(t *testing.T) {
	dec := NewAnthropicDecoder()
	if _, err := dec.Decode([]byte(`{not json`)); err == nil {
		t.Fatal("malformed payload must return an error")
	}
}

// TestAnthropicDecoderFinishFlushesPartial verifies a stream cut short (no
// message_stop) still yields a done event on Finish so the partial isn't lost.
func TestAnthropicDecoderFinishFlushesPartial(t *testing.T) {
	body := `data: {"type":"message_start","message":{"id":"m","model":"c","usage":{"input_tokens":5,"output_tokens":0}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

`
	dec := NewAnthropicDecoder()
	events, final := feedSSE(t, dec, body)
	if events[len(events)-1].EventKind() != StreamEventDone {
		t.Fatalf("Finish must emit a terminal done event, got %v", eventKinds(events))
	}
	// No message_delta arrived → default end_turn.
	if final.StopReason != StopReasonEndTurn {
		t.Errorf("cut-short stream should default to end_turn, got %q", final.StopReason)
	}
	if len(final.Content) != 1 {
		t.Fatalf("expected the partial text block, got %+v", final.Content)
	}
}

// TestAnthropicDecoderThroughTransport wires the decoder through the real
// transport pump against a recorded SSE server, exercising the full path.
func TestAnthropicDecoderThroughTransport(t *testing.T) {
	srv := sseServer(t, anthropicToolUseSSE)
	defer srv.Close()

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    NewAnthropicDecoder(),
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
	if final.StopReason != StopReasonToolUse {
		t.Errorf("stop reason via transport = %q, want tool_use", final.StopReason)
	}
	if len(final.Content) != 3 {
		t.Errorf("expected 3 content blocks via transport, got %d", len(final.Content))
	}
	if kinds[len(kinds)-1] != StreamEventDone {
		t.Errorf("stream must end with done, got %v", kinds)
	}
}

func eventKinds(events []StreamEvent) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.EventKind()
	}
	return out
}
